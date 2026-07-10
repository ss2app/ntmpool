package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
)

const drgStratumPort = 13951

// 矿工 zs 地址（打款过滤要求 zs1 前缀）。
const drgMinerAddress = "zs1e2eminer00000000000000000000000000000000000000000000000000000"

func drgTestConfig(nodeURL string) config.CoinConfig {
	return config.CoinConfig{
		ID: "drgdemo", Symbol: "DRGX", Adapter: "dragonx-rpc", Algo: "rx/drgtest",
		Nodes:       []config.NodeEndpoint{{URL: nodeURL}},
		PoolAddress: drgVault, // 金库 zs = 打款出账源 + shield 目标
		Ports: []config.PortConfig{{
			Port: drgStratumPort, Mode: "pplns", Dialect: "cryptonote", Enabled: true,
			Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1, MinDiff: 0.5, MaxDiff: 1000, TargetSeconds: 5},
		}},
		Payout: config.PayoutConfig{
			// ★ 用户拍板语义：10 确认即打款（金库垫付；coinbase 100 确认成熟后
			// Maintainer 自动 shield 回补金库）
			Enabled: true, PplnsFactor: 2, FeePercent: 3, MinPayout: "0.01",
			Confirmations: 10, IntervalSec: 600,
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

// drgMineUntilAccepted dragonx 形状 CN 矿工（drg-xmrig 行为子集）：
// 140B blob、滚 [108:112]、[112:140] 原样回显、compact-LE target、
// submit result = 内层假 rx（非 sha256d）、nonce = 完整 32B 字段。
func drgMineUntilAccepted(t *testing.T, addr string, want int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("连接 dragonx stratum 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	enc := json.NewEncoder(conn)
	sc := bufio.NewScanner(conn)

	if err := enc.Encode(map[string]any{
		"id": 1, "jsonrpc": "2.0", "method": "login",
		"params": map[string]any{
			"login": drgMinerAddress, "pass": "x", "agent": "drg-e2e/1.0",
			"rigid": "rig1", "algo": []string{"rx/drgtest"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	type wireJob struct {
		JobID    string `json:"job_id"`
		Blob     string `json:"blob"`
		Target   string `json:"target"`
		Height   uint64 `json:"height"`
		SeedHash string `json:"seed_hash"`
	}
	var (
		sessID  string
		current wireJob
	)
	if !sc.Scan() {
		t.Fatalf("login 无响应: %v", sc.Err())
	}
	var loginResp struct {
		Result struct {
			ID  string  `json:"id"`
			Job wireJob `json:"job"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &loginResp); err != nil || loginResp.Error != nil {
		t.Fatalf("login 失败: %s", sc.Text())
	}
	sessID, current = loginResp.Result.ID, loginResp.Result.Job

	// compact-LE target（drg-xmrig Job::setTarget 4 字节路径）
	parseTarget := func(hexStr string) uint64 {
		tb, err := hex.DecodeString(hexStr)
		if err != nil || len(tb) != 4 {
			t.Fatalf("target 应为 8-hex compact-LE, got %q", hexStr)
		}
		t32 := binary.LittleEndian.Uint32(tb)
		if t32 == 0 {
			t32 = 1
		}
		return ^uint64(0) / (uint64(0xFFFFFFFF) / uint64(t32))
	}

	accepted := 0
	nonce := uint32(0)
	deadline := time.Now().Add(50 * time.Second)
	for accepted < want && time.Now().Before(deadline) {
		blob, err := hex.DecodeString(current.Blob)
		if err != nil || len(blob) != 140 {
			t.Fatalf("blob 应 140B: %v", current.Blob)
		}
		seed, _ := hex.DecodeString(current.SeedHash)
		threshold := parseTarget(current.Target)

		found := false
		for ; !found && time.Now().Before(deadline); nonce++ {
			binary.LittleEndian.PutUint32(blob[108:112], nonce) // 只滚搜索区
			r := sha256.Sum256(append(append([]byte{}, seed...), blob...))
			full := make([]byte, 173)
			copy(full, blob)
			full[140] = 0x20
			copy(full[141:], r[:])
			d1 := sha256.Sum256(full)
			pow := sha256.Sum256(d1[:])
			if binary.LittleEndian.Uint64(pow[24:32]) < threshold { // drg-xmrig 同款过滤
				if err := enc.Encode(map[string]any{
					"id": 5, "jsonrpc": "2.0", "method": "submit",
					"params": map[string]any{
						"id": sessID, "job_id": current.JobID,
						"nonce":  hex.EncodeToString(blob[108:140]), // 完整 32B 回显
						"result": hex.EncodeToString(r[:]),          // 原始内层 rx
						"algo":   "rx/drgtest",
					},
				}); err != nil {
					t.Fatal(err)
				}
				found = true
			}
		}
		if !found {
			break
		}
	waitResp:
		for sc.Scan() {
			var msg struct {
				Method string                    `json:"method"`
				Params wireJob                   `json:"params"`
				Result map[string]any            `json:"result"`
				Error  *struct{ Message string } `json:"error"`
			}
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}
			switch {
			case msg.Method == "job":
				current = msg.Params
				nonce = 0
			case msg.Error != nil:
				break waitResp
			case msg.Result != nil && msg.Result["status"] == "OK":
				accepted++
				break waitResp
			}
		}
	}
	if accepted < 1 {
		t.Fatal("dragonx 矿工未能提交任何被接受的 share")
	}
	t.Logf("✓ dragonx 矿工 accepted=%d", accepted)
}

// dragonx 全链路：锄头→share(双段校验)→爆块(节点真验)→10确认→PPLNS→
// z_sendmany(opid 轮询→txid)→coinbase 成熟→Maintainer shield 回补金库。
func TestDrgFullPipeline(t *testing.T) {
	node := newFakeDrgNode()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, drgTestConfig(node.URL()), coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatalf("启动 dragonx 币实例失败: %v", err)
	}
	defer inst.Stop()

	drgMineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", drgStratumPort), 3)

	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "drgdemo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("池未记录 dragonx 爆块")
	}
	if node.sentCount() != 0 {
		t.Fatal("10 确认前不应打款")
	}
	t.Logf("✓ dragonx 爆块 %d 个（假节点独立重算 solution + 173B sha256d 通过）", blocksFound)

	// 推进 15 个确认（>10）→ confirm + z_sendmany 垫付
	node.advance(15)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("打款周期失败: %v", err)
	}
	snap, _ := inst.Ledger().Snapshot(ctx, "drgdemo")
	if snap.Confirmed < 1 {
		t.Fatalf("10 确认后块未入账: %+v", snap)
	}
	if node.sentCount() < 1 {
		t.Fatal("确认后应有 z_sendmany 打款")
	}
	if node.pollCount() < 2 {
		t.Fatalf("z_sendmany 应经过 opid 异步轮询（polls=%d）", node.pollCount())
	}
	node.mu.Lock()
	sentFrom, sentAddrs := node.sentFrom, append([]string(nil), node.sentAddrs...)
	node.mu.Unlock()
	if sentFrom != drgVault {
		t.Fatalf("打款出账源应为金库 zs, got %s", sentFrom)
	}
	if len(sentAddrs) != 1 || sentAddrs[0] != drgMinerAddress {
		t.Fatalf("收款人应为矿工 zs 地址: %v", sentAddrs)
	}
	t.Logf("✓ 10 确认垫付打款：金库→矿工 zs，opid 轮询 %d 次后拿到 txid", node.pollCount())

	// coinbase 成熟（≥100 确认）→ Maintainer 自动 shield 回补金库
	node.advance(200)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("成熟后打款周期失败: %v", err)
	}
	if node.shieldCount() < 1 {
		t.Fatal("coinbase 成熟后应自动 z_shieldcoinbase 回补金库")
	}
	t.Logf("✓ dragonx 全链路端到端：share→爆块→10确认→PPLNS→z_sendmany(opid)→shield 回补")
}

// dragonx 孤块路径：reorg 甩掉的块判 ORPHANED，不打款。
func TestDrgOrphanPipeline(t *testing.T) {
	node := newFakeDrgNode()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := drgTestConfig(node.URL())
	cfg.Ports[0].Port = drgStratumPort + 1
	inst, err := coininstance.Start(ctx, cfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	drgMineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", drgStratumPort+1), 3)
	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "drgdemo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("未爆块，孤块测试无从谈起")
	}

	node.orphanAll()
	node.advance(15)
	_ = inst.RunPayoutNow(ctx)
	snap, _ := inst.Ledger().Snapshot(ctx, "drgdemo")
	if snap.Orphaned < 1 {
		t.Fatalf("应判至少 1 个孤块: %+v", snap)
	}
	if snap.Confirmed != 0 || node.sentCount() != 0 {
		t.Fatalf("孤块不应确认/打款: %+v sent=%d", snap, node.sentCount())
	}
	t.Logf("✓ dragonx 孤块路径：%d 个块判 ORPHANED，未误打款", snap.Orphaned)
}
