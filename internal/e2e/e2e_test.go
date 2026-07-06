// Package e2e 是 NTMPool 的纯 Go 端到端测试：假 bitcoind + 真 CoinInstance +
// 真 stratum 客户端（btcwork 挖矿），跑通 锄头→share→爆块→submitblock→确认→PPLNS→打款 全链路。
// 不依赖 Docker/bitcoind，可进 CI。真实 bitcoind 的字节级验收另在服务器 regtest 做。
package e2e

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/api"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
)

// 编译期断言：真 CoinInstance 满足公共 API 的只读视图接口。
var _ api.Pool = (*coininstance.Instance)(nil)

const stratumPort = 13911

// minerAddress e2e 矿工地址（authorize 用 minerAddress.rig1）。
const minerAddress = "minerAddr"

func testConfig(nodeURL string) config.CoinConfig {
	return config.CoinConfig{
		ID: "demo", Symbol: "DEMO", Adapter: "bitcoin-rpc", Algo: "sha256d",
		Nodes:       []config.NodeEndpoint{{URL: nodeURL, User: "x", Pass: "y"}},
		PoolAddress: "poolAddr", FeeAddress: "feeAddr",
		Ports: []config.PortConfig{{
			Port: stratumPort, Mode: "pplns", Dialect: "stratum1", Enabled: true,
			Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 0.0001, MinDiff: 0.00001, MaxDiff: 1000, TargetSeconds: 5},
		}},
		Payout: config.PayoutConfig{
			Enabled: true, PplnsFactor: 2, FeePercent: 10, MinPayout: "0.01",
			Confirmations: 100, IntervalSec: 600,
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

func TestFullPipeline(t *testing.T) {
	// 假 P2PKH 脚本（矿池地址）
	poolScript := "76a914000102030405060708090a0b0c0d0e0f101112131488ac"
	node := newFakeNode(poolScript)
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, testConfig(node.URL()), true)
	if err != nil {
		t.Fatalf("启动币实例失败: %v", err)
	}
	defer inst.Stop()

	// 连 stratum 端口挖矿，直到池记录到至少 1 个爆块
	mineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", stratumPort))

	// 断言：池找到并记录了块
	snap, _ := inst.Ledger().Snapshot(ctx, "demo")
	if snap.BlocksFound < 1 {
		t.Fatalf("池未记录爆块: %+v", snap)
	}
	if node.broadcastCount() != 0 {
		t.Fatal("此时不应有打款（块未成熟）")
	}
	t.Logf("✓ 池记录爆块 %d 个，节点已接受 submitblock", snap.BlocksFound)

	// 推进成熟 → 跑打款周期 → 块 confirm + PPLNS 分账 + 拆步打款广播
	node.matureAll(120)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("打款周期失败: %v", err)
	}
	snap, _ = inst.Ledger().Snapshot(ctx, "demo")
	if snap.Confirmed < 1 {
		t.Fatalf("块未确认入账: %+v", snap)
	}
	if node.broadcastCount() < 1 {
		t.Fatal("确认后应有打款广播")
	}
	t.Logf("✓ 块确认 %d 个，PPLNS 已分账，打款已广播（拆步 txid 广播前落库）", snap.Confirmed)
	t.Logf("✓ 全链路端到端通过：锄头→share→爆块→submitblock→确认→PPLNS→拆步打款")

	// 公共 API 冒烟：真 Instance 直接喂给 api.Server，验证 miningcore 形状 + 脱敏
	srv := api.New(func() map[string]api.Pool { return map[string]api.Pool{"demo": inst} }, []byte("e2e"))
	h := srv.Handler()
	for _, path := range []string{"/api/pools", "/api/pools/demo/blocks", "/api/pools/demo/payments", "/api/pools/demo/miners"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("API %s code=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if path != "/api/pools" && strings.Contains(rec.Body.String(), minerAddress) {
			t.Fatalf("API %s 泄漏完整矿工地址", path)
		}
	}
	var pools struct {
		Pools []struct {
			TotalConfirmedBlocks int             `json:"totalConfirmedBlocks"`
			TotalPaid            json.RawMessage `json:"totalPaid"`
		} `json:"pools"`
	}
	req := httptest.NewRequest("GET", "/api/pools", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &pools); err != nil || len(pools.Pools) != 1 {
		t.Fatalf("API /api/pools 解析失败: %v %s", err, rec.Body.String())
	}
	if pools.Pools[0].TotalConfirmedBlocks < 1 {
		t.Fatalf("API 应反映已确认块: %+v", pools.Pools[0])
	}
	// 矿工完整地址自查（不脱敏自己的数据）
	req = httptest.NewRequest("GET", "/api/pools/demo/miners/"+minerAddress, nil)
	req.RemoteAddr = "127.0.0.1:9"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("矿工自查 code=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("✓ 公共 API（miningcore 形状+脱敏+自查）对真实例冒烟通过")
}

// 孤块路径：块提交后，主链在该高度换成别的 hash → 应判孤块，不打款。
func TestOrphanPipeline(t *testing.T) {
	poolScript := "76a914000102030405060708090a0b0c0d0e0f101112131488ac"
	node := newFakeNode(poolScript)
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, testConfig(node.URL()), true)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	mineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", stratumPort))
	snap, _ := inst.Ledger().Snapshot(ctx, "demo")
	blocksFound := snap.BlocksFound

	// 把所有已提交块的高度对应主链 hash 改成别人的（模拟我们被 reorg 掉）
	node.mu.Lock()
	for h := range node.submitted {
		node.submitted[h] = "someoneelseshash000000000000000000000000000000000000000000000000"
	}
	for hash := range node.blockConf {
		node.blockConf[hash] = 120 // 成熟，但 hash 已对不上
	}
	node.mu.Unlock()

	_ = inst.RunPayoutNow(ctx)
	snap, _ = inst.Ledger().Snapshot(ctx, "demo")
	if snap.Orphaned < 1 {
		t.Fatalf("应判至少 1 个孤块（共 %d 块）: %+v", blocksFound, snap)
	}
	if snap.Confirmed != 0 {
		t.Fatalf("孤块不应有确认: %+v", snap)
	}
	if node.broadcastCount() != 0 {
		t.Fatal("孤块不应打款")
	}
	t.Logf("✓ 孤块路径：%d 个块被主链 hash 逐字节比对判为 ORPHANED，未误打款", snap.Orphaned)
}

// mineUntilBlock 连 stratum 挖矿直到池爆块（regtest 极易目标下秒级完成）。
func mineUntilBlock(t *testing.T, addr string) {
	t.Helper()
	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("连 stratum 失败: %v", err)
	}
	defer conn.Close()

	w := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	send := func(id int, method string, params []any) {
		pb, _ := json.Marshal(params)
		msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(pb)})
		_, _ = w.Write(append(msg, '\n'))
		_ = w.Flush()
	}
	send(1, "mining.subscribe", []any{"e2eminer/1.0"})
	send(2, "mining.authorize", []any{minerAddress + ".rig1", "x"})

	var en1 []byte
	en2Size := 4
	var target *btcwork.BigTarget
	var jobID string
	var prevInt, coinb1, coinb2 []byte
	var branch [][]byte
	var version, bits, ntime uint32
	haveJob := false
	var en2Ctr uint32

	deadline := time.Now().Add(20 * time.Second)
	grind := func() bool {
		if !haveJob || target == nil || len(en1) == 0 {
			return false
		}
		en2 := make([]byte, en2Size)
		en2[0] = byte(en2Ctr >> 24)
		en2[1] = byte(en2Ctr >> 16)
		en2[2] = byte(en2Ctr >> 8)
		en2[3] = byte(en2Ctr)
		en2Ctr++
		cb := append(append(append(append([]byte{}, coinb1...), en1...), en2...), coinb2...)
		root := btcwork.MerkleRootFromBranch(btcwork.DoubleSHA(cb), branch)
		for nonce := uint32(0); nonce < 500000; nonce++ {
			header := btcwork.SerializeHeader(version, prevInt, root, ntime, bits, nonce)
			if target.Meets(btcwork.HeaderHash(header)) {
				send(100+int(en2Ctr), "mining.submit", []any{
					"minerAddr.rig1", jobID, hex.EncodeToString(en2),
					fmt.Sprintf("%08x", ntime), fmt.Sprintf("%08x", nonce),
				})
				return true
			}
		}
		return false
	}

	submits := 0
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := br.ReadString('\n')
		if err != nil {
			if grind() {
				submits++
				if submits >= 3 {
					return // 已提交足够多的解，池必已记录爆块
				}
			}
			continue
		}
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "mining.set_difficulty":
			var p []float64
			if json.Unmarshal(msg.Params, &p) == nil && len(p) > 0 {
				target = btcwork.NewBigTarget(btcwork.DiffToTarget(p[0]))
			}
		case "mining.notify":
			var p []json.RawMessage
			if json.Unmarshal(msg.Params, &p) == nil && len(p) >= 9 {
				str := func(i int) string { var s string; _ = json.Unmarshal(p[i], &s); return s }
				jobID = str(0)
				prevInt, _ = btcwork.PrevHashInternalFromStratum(str(1))
				coinb1, _ = hex.DecodeString(str(2))
				coinb2, _ = hex.DecodeString(str(3))
				var bh []string
				_ = json.Unmarshal(p[4], &bh)
				branch = nil
				for _, b := range bh {
					bb, _ := hex.DecodeString(b)
					branch = append(branch, bb)
				}
				version = pu32(str(5))
				bits = pu32(str(6))
				ntime = pu32(str(7))
				haveJob = true
			}
		case "":
			// subscribe 响应
			var arr []json.RawMessage
			if json.Unmarshal(msg.Result, &arr) == nil && len(arr) >= 3 {
				var e string
				_ = json.Unmarshal(arr[1], &e)
				if d, err := hex.DecodeString(e); err == nil && len(d) > 0 {
					en1 = d
					_ = json.Unmarshal(arr[2], &en2Size)
				}
			}
		}
	}
	t.Fatalf("超时未能爆块（提交 %d 次）", submits)
}

func pu32(s string) uint32 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 32)
	return uint32(v)
}
