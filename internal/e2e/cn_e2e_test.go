package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
)

const cnStratumPort = 13921
const cnMinerAddress = "cnMinerAddr"

func cnTestConfig(nodeURL string) config.CoinConfig {
	return config.CoinConfig{
		ID: "cndemo", Symbol: "CND", Adapter: "custom-http", Algo: "rx/test",
		Nodes:       []config.NodeEndpoint{{URL: nodeURL}},
		PoolAddress: "cnPoolAddr", FeeAddress: "cnFeeAddr",
		Ports: []config.PortConfig{{
			Port: cnStratumPort, Mode: "pplns", Dialect: "cryptonote", Enabled: true,
			Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 0.0001, MinDiff: 0.00001, MaxDiff: 1000, TargetSeconds: 5},
		}},
		Payout: config.PayoutConfig{
			Enabled: true, PplnsFactor: 2, FeePercent: 10, MinPayout: "0.01",
			Confirmations: 100, IntervalSec: 600,
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

// cnMiner 最小 CN 协议矿工（XMRig 行为子集）：login → 挖当前 job →
// submit → 处理 job 推送换工。挖到 needBlocks 个被接受的 share 即返回。
func cnMineUntilBlock(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("连接 CN stratum 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	enc := json.NewEncoder(conn)
	sc := bufio.NewScanner(conn)

	// login（带 rigid + algo 协商）
	if err := enc.Encode(map[string]any{
		"id": 1, "jsonrpc": "2.0", "method": "login",
		"params": map[string]any{
			"login": cnMinerAddress, "pass": "x", "agent": "e2e-miner/1.0",
			"rigid": "rig1", "algo": []string{"rx/test"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	type wireJob struct {
		JobID    string `json:"job_id"`
		Blob     string `json:"blob"`
		Target   string `json:"target"`
		Algo     string `json:"algo"`
		Height   uint64 `json:"height"`
		SeedHash string `json:"seed_hash"`
	}
	var (
		sessID  string
		current wireJob
	)

	// 读 login 响应
	if !sc.Scan() {
		t.Fatalf("login 无响应: %v", sc.Err())
	}
	var loginResp struct {
		Result struct {
			ID  string          `json:"id"`
			Job wireJob         `json:"job"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &loginResp); err != nil || loginResp.Error != nil {
		t.Fatalf("login 失败: %s", sc.Text())
	}
	sessID, current = loginResp.Result.ID, loginResp.Result.Job
	if current.Algo != "rx/test" || len(current.SeedHash) != 64 {
		t.Fatalf("job 字段错: %+v", current)
	}

	key, _ := hex.DecodeString(current.SeedHash)
	accepted := 0
	nonce := uint64(0)

	submit := func(blob []byte, result []byte) error {
		full := make([]byte, 8)
		copy(full, blob[len(blob)-8:])
		return enc.Encode(map[string]any{
			"id": 5, "jsonrpc": "2.0", "method": "submit",
			"params": map[string]any{
				"id": sessID, "job_id": current.JobID,
				"nonce":  hex.EncodeToString(full),
				"result": hex.EncodeToString(result),
			},
		})
	}

	deadline := time.Now().Add(50 * time.Second)
	for accepted < 3 && time.Now().Before(deadline) {
		blob, err := hex.DecodeString(current.Blob)
		if err != nil || len(blob) < 8 {
			t.Fatalf("blob 非法: %v", current.Blob)
		}
		tb, _ := hex.DecodeString(current.Target)
		target := new(big.Int).SetBytes(tb) // 64-hex 大端全量 target

		// 滚低 4 字节搜索区（高 4 字节 = 池钉的连接 tag，保持不动）
		found := false
		for ; !found && time.Now().Before(deadline); nonce++ {
			for i := 0; i < 4; i++ {
				blob[len(blob)-8+i] = byte(nonce >> (8 * i))
			}
			h := sha256.New()
			h.Write(key)
			h.Write(blob)
			sum := h.Sum(nil)
			if new(big.Int).SetBytes(sum).Cmp(target) <= 0 {
				if err := submit(blob, sum); err != nil {
					t.Fatal(err)
				}
				found = true
			}
		}
		if !found {
			break
		}
		// 读响应/推送直到拿到 submit 结果
	waitResp:
		for sc.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params wireJob         `json:"params"`
				Result map[string]any  `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}
			switch {
			case msg.Method == "job":
				current = msg.Params // 换工
			case msg.Error != nil:
				// stale/lowdiff 等：换 job 后重试（vardiff straddle 在真锄头由一步 grace 兜）
				break waitResp
			case msg.Result != nil && msg.Result["status"] == "OK":
				accepted++
				break waitResp
			}
		}
	}
	if accepted < 1 {
		t.Fatal("CN 矿工未能提交任何被接受的 share")
	}
	t.Logf("✓ CN 矿工 accepted=%d", accepted)
}

func TestCNFullPipeline(t *testing.T) {
	node := newFakeCNNode()
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, cnTestConfig(node.URL()), coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatalf("启动 CN 币实例失败: %v", err)
	}
	defer inst.Stop()

	cnMineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", cnStratumPort))

	// diffBits=1：接受的 share 约一半是块，挖 3 个 accepted 几乎必有块；
	// 但为稳妥轮询等待爆块入账（池提交/记账是同步的，这里只等 ledger 可见）
	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "cndemo")
		blocksFound = snap.BlocksFound
		if blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("池未记录 CN 爆块")
	}
	if node.sentCount() != 0 {
		t.Fatal("此时不应有打款（块未成熟）")
	}
	t.Logf("✓ CN 池记录爆块 %d 个（节点端真验块 accepted）", blocksFound)

	// 推进成熟（tip - blockHeight + 1 > 100）→ 打款
	node.advance(200)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("打款周期失败: %v", err)
	}
	snap, _ := inst.Ledger().Snapshot(ctx, "cndemo")
	if snap.Confirmed < 1 {
		t.Fatalf("CN 块未确认入账: %+v", snap)
	}
	if node.sentCount() < 1 {
		t.Fatal("确认后应有打款（/wallet/sendmany）")
	}
	t.Logf("✓ CN 全链路端到端通过：CN 锄头→share→爆块→REST submit→确认→PPLNS→sendmany 打款")
}

// CN 孤块路径：被 reorg 甩掉的块 hash 逐字节比对判 ORPHANED，不打款。
func TestCNOrphanPipeline(t *testing.T) {
	node := newFakeCNNode()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := cnTestConfig(node.URL())
	cfg.Ports[0].Port = cnStratumPort + 1
	inst, err := coininstance.Start(ctx, cfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	cnMineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", cnStratumPort+1))
	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "cndemo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("未爆块，孤块测试无从谈起")
	}

	node.orphanAll()
	node.advance(200)
	_ = inst.RunPayoutNow(ctx)
	snap, _ := inst.Ledger().Snapshot(ctx, "cndemo")
	if snap.Orphaned < 1 {
		t.Fatalf("应判至少 1 个孤块: %+v", snap)
	}
	if snap.Confirmed != 0 {
		t.Fatalf("孤块不应有确认: %+v", snap)
	}
	if node.sentCount() != 0 {
		t.Fatal("孤块不应打款")
	}
	t.Logf("✓ CN 孤块路径：%d 个块判 ORPHANED，未误打款", snap.Orphaned)
}

// 多币单实例共存：bitcoin 系 + CN 系两个 Instance 同进程并行挖，互不影响。
func TestMultiCoinCoexistence(t *testing.T) {
	poolScript := "76a914000102030405060708090a0b0c0d0e0f101112131488ac"
	btcNode := newFakeNode(poolScript)
	defer btcNode.Close()
	cnNode := newFakeCNNode()
	defer cnNode.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	btcCfg := testConfig(btcNode.URL())
	btcCfg.Ports[0].Port = stratumPort + 100
	cnCfg := cnTestConfig(cnNode.URL())
	cnCfg.Ports[0].Port = cnStratumPort + 100

	btcInst, err := coininstance.Start(ctx, btcCfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatalf("btc 实例: %v", err)
	}
	defer btcInst.Stop()
	cnInst, err := coininstance.Start(ctx, cnCfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatalf("cn 实例: %v", err)
	}
	defer cnInst.Stop()

	mineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", stratumPort+100))
	cnMineUntilBlock(t, fmt.Sprintf("127.0.0.1:%d", cnStratumPort+100))

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := btcInst.Ledger().Snapshot(ctx, "demo")
		c, _ := cnInst.Ledger().Snapshot(ctx, "cndemo")
		if b.BlocksFound >= 1 && c.BlocksFound >= 1 {
			t.Logf("✓ 多币共存：btc 爆块 %d + CN 爆块 %d（同进程两条独立管线）", b.BlocksFound, c.BlocksFound)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := btcInst.Ledger().Snapshot(ctx, "demo")
	c, _ := cnInst.Ledger().Snapshot(ctx, "cndemo")
	t.Fatalf("多币共存未双爆块: btc=%d cn=%d", b.BlocksFound, c.BlocksFound)
}
