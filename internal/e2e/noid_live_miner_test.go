//go:build noid

package e2e

// NOID 桥接 e2e：用【真 NTMminer-noid 二进制】连真池挖假节点。
//
// 与 noid_e2e_test.go 的区别：那边的"矿工"是测试内的 Go 代码（验协议语义），
// 这边跑的是发给矿工的那个**真二进制**——验的是产品本身：
//   - 锄头的 HTTP JSON-RPC wire 与池方言真能对上（字段名/参数形状/Bearer）
//   - 锄头 midstate 引擎算出的 digest 与池侧 cgo 重算【逐字节一致】
//     （不一致 = share 全被判 lowdiff，池侧计数会当场归零 → 测试炸）
//   - 池回 -32001 后锄头继续同高度挖（不是卡死/不是狂刷）
//   - 锄头爆块 → 池上行节点 → 节点端真 PoW 验证通过 → 换模板 → 锄头跟上
//
// 需要环境变量 NTMMINER_NOID_BIN 指向锄头二进制；未设置则 skip（CI/无锄头机器）。
//
//	set NTMMINER_NOID_BIN=C:\ntmbuild\ntmminer-noid\target\release\ntmminer-noid.exe
//	go test -tags noid -run TestNoidLiveMinerBridge -v ./internal/e2e/

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/coininstance"
)

const (
	noidLivePort = 15931
	// noidLiveShareDiff share 难度：慢机（5850U 8 线程 ~1.5 MH/s）也有 ~1.5 share/s。
	noidLiveShareDiff = 1_000_000
	// noidLiveNetDiff 全网难度 = 5× share 难度。
	// ⚠这几个数是【按最慢的对拍机】定的，别按快机调：2026-08-13 首版取 3e7 + 25s 窗口，
	//   7950X（4 线程 ~1.3 MH/s，23s/块）刚好卡线过，5850U（4 线程 783 kH/s，38s/块）
	//   直接超时 FAIL —— 测试对机器性能太敏感就成了假红灯。
	//   现在：5850U 8 线程 ≈1.5 MH/s ⇒ 3.3s/块，45s 窗口期望 13 块，P(零块)≈0.1%。
	noidLiveNetDiff = 5_000_000
	// noidLiveRun 挖矿窗口（达标即提前收工，不会真跑满）。
	noidLiveRun = 45 * time.Second
)

// targetLEFromDiff 难度 → 256-bit LE target（与池 dialect targetLE32 同口径）。
func targetLEFromDiff(diff float64) [32]byte {
	t := cnwork.TargetFromDiff(diff)
	be := t.Bytes()
	if len(be) > 32 {
		be = be[len(be)-32:]
	}
	var out [32]byte
	for i := 0; i < len(be); i++ {
		out[i] = be[len(be)-1-i] // BE → LE
	}
	return out
}

func TestNoidLiveMinerBridge(t *testing.T) {
	bin := os.Getenv("NTMMINER_NOID_BIN")
	if bin == "" {
		t.Skip("未设 NTMMINER_NOID_BIN（指向真 NTMminer-noid 二进制）——跳过桥接 e2e")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("NTMMINER_NOID_BIN=%q 不可用: %v", bin, err)
	}

	node := newFakeNoidNodeTarget(targetLEFromDiff(noidLiveNetDiff))
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx,
		noidTestConfigDiff(node.URL(), noidLivePort, noidLiveShareDiff),
		coininstance.Deps{})
	if err != nil {
		t.Fatalf("启动 NOID 币实例失败: %v", err)
	}
	defer inst.Stop()

	// 先确保池已装载模板（锄头启动就能拿到活），避免把暖机窗口算进挖矿时间。
	probe := newNoidMiner(t, noidLivePort, noidMinerAddr+".probe")
	if _, err := probe.waitTemplate(30 * time.Second); err != nil {
		t.Fatalf("池暖机未完成: %v", err)
	}

	// —— 起真锄头 ——
	runCtx, runCancel := context.WithTimeout(ctx, noidLiveRun+90*time.Second)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, bin,
		"--rpc", fmt.Sprintf("http://127.0.0.1:%d/", noidLivePort),
		"--key", noidMinerAddr+".liverig",
		"--threads", "8", // 够快就行，别把测试机吃满
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动锄头失败: %v", err)
	}

	var logMu sync.Mutex
	var minerLog []string
	drain := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			logMu.Lock()
			minerLog = append(minerLog, line)
			logMu.Unlock()
		}
	}
	go drain(stdout)
	go drain(stderr)

	dumpLog := func() string {
		logMu.Lock()
		defer logMu.Unlock()
		n := len(minerLog)
		if n > 30 {
			return strings.Join(minerLog[n-30:], "\n")
		}
		return strings.Join(minerLog, "\n")
	}

	// —— 挖矿窗口：等到「账本记满 share 且池已爆块」或超时 ——
	deadline := time.Now().Add(noidLiveRun)
	var shares, blocks int
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		snap, err := inst.Ledger().Snapshot(ctx, "noiddemo")
		if err != nil {
			continue
		}
		shares, blocks = snap.WindowShares, snap.BlocksFound
		if shares >= 5 && blocks >= 1 {
			break
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// —— 断言 ——
	if shares < 5 {
		t.Fatalf("真锄头只记了 %d 条 share（期望 ≥5）——wire 对不上或 digest 不一致？\n矿工日志尾部:\n%s",
			shares, dumpLog())
	}
	tplCalls, submitCalls, accepted, badPoW, badParams, unauth := node.stats()
	if blocks < 1 {
		t.Fatalf("窗口内未爆块（share=%d，节点 submit=%d accepted=%d）\n矿工日志尾部:\n%s",
			shares, submitCalls, accepted, dumpLog())
	}
	// ★★真锄头算的 digest 与池侧 cgo 重算必须逐字节一致：
	// 若不一致，锄头认为达标的解在池侧会判 lowdiff，shares 会是 0（上面已挡）；
	// 而池认为达标上行的解在节点侧会判 invalid PoW → badPoW 非 0。
	if badPoW != 0 {
		t.Fatalf("★★节点端真 PoW 验证拒了 %d 个池上行的解（锄头/池/节点三方 digest 不一致）", badPoW)
	}
	if accepted != submitCalls {
		t.Fatalf("★上行 %d 次但节点只接受 %d 个（池的爆块判定与节点不同尺）", submitCalls, accepted)
	}
	// 上行次数 = 爆块数：普通 share 一次都不许上行（烧模板槽）。
	if submitCalls != blocks {
		t.Fatalf("★★上行 submitBlock %d 次 ≠ 池记录的爆块 %d 个（普通 share 泄漏到节点了）",
			submitCalls, blocks)
	}
	if badParams != 0 || unauth != 0 {
		t.Fatalf("★协议契约破了：badParams=%d unauthorized=%d", badParams, unauth)
	}

	// 锄头日志自证：启动 selftest 过 + 用了 midstate 引擎（不是退化路径）。
	logs := dumpLog()
	logMu.Lock()
	full := strings.Join(minerLog, "\n")
	logMu.Unlock()
	if !strings.Contains(full, "startup selftest: PASS") {
		t.Fatalf("锄头未打印启动 selftest PASS（共识自检没跑？）\n%s", logs)
	}
	if !strings.Contains(full, "engine=midstate") {
		t.Fatalf("锄头未走 midstate 引擎\n%s", logs)
	}
	// ★池模式 stale 检测必须启用：池不服务 getChainInfo(-32601)，锄头要回退到
	// 轮询 template_id。没有它，锄头只在每次 solve 后才换工作 —— vardiff 稳态
	// ~10s/share 对上 ~15s 出块 ⇒ 平均三分之一的算力在挖死掉的父块。
	if !strings.Contains(full, "pool template_id") {
		t.Fatalf("★锄头未启用池模式 stale 检测（watchdog 回退失效 = 大量算力挖过期模板）\n%s", logs)
	}
	// ★★而这个回退【绝不能】穿透到节点：真节点是单飞行槽，被第二个调用者轮询
	// getBlockTemplate 会烧掉模板槽。判据 = 假节点侧收到的拉模板次数必须仍然很少
	// （watchdog 每 500ms 打一次池，若穿透到节点，45s 窗口会是 ~90 次）。
	if tplCalls > 10 {
		t.Fatalf("★★打节点拉模板 %d 次——watchdog 的池轮询穿透到节点了（会烧单飞行槽）", tplCalls)
	}

	// 全网难度 vs share 难度的量级关系应体现在「share 多、块少」上。
	if big.NewInt(int64(shares)).Cmp(big.NewInt(int64(blocks))) <= 0 {
		t.Fatalf("share(%d) 应显著多于块(%d)——难度阶梯没生效", shares, blocks)
	}

	t.Logf("✓✓ 真 NTMminer-noid 桥接 e2e：%s 内记账 %d 条 share、爆块 %d 个；"+
		"上行节点 %d 次全部通过节点端真 PoW 验证（badPoW=0）；打节点拉模板 %d 次",
		noidLiveRun, shares, blocks, submitCalls, tplCalls)
}
