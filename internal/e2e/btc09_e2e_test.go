package e2e

import (
	"bufio"
	"context"
	"encoding/binary"
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

// jsonEncoder 行 JSON 发送闭包。
func jsonEncoder(conn net.Conn) func(v any) error {
	enc := json.NewEncoder(conn)
	return func(v any) error { return enc.Encode(v) }
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("解析失败 %q: %v", string(b), err)
	}
}

const btc09StratumPort = 14921

func btc09TestConfig(nodeURL string) config.CoinConfig {
	return config.CoinConfig{
		ID: "btc09demo", Symbol: "09C", Adapter: "btc09-http", Algo: "argon2id-btc09",
		Nodes:       []config.NodeEndpoint{{URL: nodeURL}},
		PoolAddress: btc09TestAddress(0xA0), FeeAddress: btc09TestAddress(0xB0),
		Ports: []config.PortConfig{{
			Port: btc09StratumPort, Mode: "pplns", Dialect: "btc09", Enabled: true,
			// sub-1 难度路径：真 Argon2id ~几十 ms/hash，0.0001 难度 ≈ 每 ~7 hash 一个 share
			Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 0.0001, MinDiff: 0.00001, MaxDiff: 1000, TargetSeconds: 5},
		}},
		Payout: config.PayoutConfig{
			Enabled: true, PplnsFactor: 2, FeePercent: 5, MinPayout: "0.01",
			Confirmations: 100, IntervalSec: 600,
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

// btc09MineUntilAccepted 最小 09C 协议矿工（NTMminer -a btc09 行为子集）：
// login → 收 job 推送 → 在私有 nonce 窗口内真 Argon2id 碾 nonce → submit
// （带声明 hash，顺带压 badpow tripwire 路径）。
func btc09MineUntilAccepted(t *testing.T, addr, minerAddr string, need int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("连接 09C stratum 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))

	enc := jsonEncoder(conn)
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if err := enc(map[string]any{
		"id": 1, "method": "login",
		"params": map[string]any{"address": minerAddr, "worker": "rig1", "pass": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if !sc.Scan() {
		t.Fatalf("login 无响应: %v", sc.Err())
	}
	var loginResp struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	mustUnmarshal(t, sc.Bytes(), &loginResp)
	if loginResp.Error != nil || loginResp.Result["status"] != "ok" {
		t.Fatalf("login 失败: %s", sc.Text())
	}

	type wireJob struct {
		JobID         string  `json:"job_id"`
		Height        uint64  `json:"height"`
		HeaderBase    string  `json:"header_base"`
		Target        string  `json:"target"`
		NetworkTarget string  `json:"network_target"`
		Difficulty    float64 `json:"difficulty"`
		NonceStart    uint64  `json:"nonce_start"`
		NonceEnd      uint64  `json:"nonce_end"`
	}
	var current wireJob

	// login 后必推一个 job
	if !sc.Scan() {
		t.Fatalf("login 后未收到 job 推送: %v", sc.Err())
	}
	var push struct {
		Method string  `json:"method"`
		Params wireJob `json:"params"`
	}
	mustUnmarshal(t, sc.Bytes(), &push)
	if push.Method != "job" {
		t.Fatalf("期待 job 推送，收到 %s", sc.Text())
	}
	current = push.Params
	if current.NonceStart == 0 || current.NonceEnd <= current.NonceStart {
		t.Fatalf("nonce 窗口非法: %+v", current)
	}

	accepted := 0
	nonce := current.NonceStart
	deadline := time.Now().Add(80 * time.Second)

	for accepted < need && time.Now().Before(deadline) {
		header, err := hex.DecodeString(current.HeaderBase)
		if err != nil || len(header) != 88 {
			t.Fatalf("header 非 88 字节: %v", current.HeaderBase)
		}
		tb, _ := hex.DecodeString(current.Target)
		target := new(big.Int).SetBytes(tb)
		if nonce < current.NonceStart || nonce > current.NonceEnd {
			nonce = current.NonceStart
		}

		// 真 Argon2id 碾 nonce（与池端/假节点同一份 x/crypto 库）
		found := false
		var foundNonce uint64
		var foundHash []byte
		for ; !found && time.Now().Before(deadline); nonce++ {
			binary.LittleEndian.PutUint64(header[80:88], nonce)
			h := btc09PowHash(header)
			if new(big.Int).SetBytes(h).Cmp(target) <= 0 {
				found, foundNonce, foundHash = true, nonce, h
			}
		}
		if !found {
			break
		}
		if err := enc(map[string]any{
			"id": 5, "method": "submit",
			"params": map[string]any{
				"job_id": current.JobID,
				"nonce":  foundNonce,
				"hash":   hex.EncodeToString(foundHash),
			},
		}); err != nil {
			t.Fatal(err)
		}

	waitResp:
		for sc.Scan() {
			var msg struct {
				Method string         `json:"method"`
				Params wireJob        `json:"params"`
				Result map[string]any `json:"result"`
				Error  any            `json:"error"`
			}
			mustUnmarshal(t, sc.Bytes(), &msg)
			switch {
			case msg.Method == "job":
				current = msg.Params // 换工（nonce 窗口保持，job 内容换）
			case msg.Error != nil:
				// stale/duplicate 等：等下一个 job 再战
				break waitResp
			case msg.Result != nil:
				if s, _ := msg.Result["status"].(string); s == "accepted" || s == "block" {
					accepted++
				}
				break waitResp
			}
		}
	}
	if accepted < need {
		t.Fatalf("09C 矿工只有 %d/%d 个 share 被接受", accepted, need)
	}
	t.Logf("✓ 09C 矿工 accepted=%d（真 Argon2id）", accepted)
}

func TestBtc09FullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("真 Argon2id e2e 较重，-short 跳过")
	}
	node := newFakeBtc09Node()
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, btc09TestConfig(node.URL()), coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatalf("启动 09C 币实例失败: %v", err)
	}
	defer inst.Stop()

	miner := btc09TestAddress(0x10)
	btc09MineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", btc09StratumPort), miner, 3)

	// 全网目标 2^254 远松于 share 目标 → 接受的 share 几乎全是块
	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "btc09demo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("池未记录 09C 爆块")
	}
	if node.sentCount() != 0 {
		t.Fatal("此时不应有打款（块未成熟）")
	}
	t.Logf("✓ 09C 池记录爆块 %d 个（假节点真 Argon2id 验块 accepted）", blocksFound)

	node.advance(200)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("打款周期失败: %v", err)
	}
	snap, _ := inst.Ledger().Snapshot(ctx, "btc09demo")
	if snap.Confirmed < 1 {
		t.Fatalf("09C 块未确认入账: %+v", snap)
	}
	if node.sentCount() < 1 {
		t.Fatal("确认后应有打款（/wallet/sendmany）")
	}
	t.Logf("✓ 09C 全链路端到端通过：line-JSON 矿工→sub-1 share→真 Argon2id 重算→爆块→REST submit→确认→PPLNS→sendmany 打款")
}

func TestBtc09OrphanPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("真 Argon2id e2e 较重，-short 跳过")
	}
	node := newFakeBtc09Node()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := btc09TestConfig(node.URL())
	cfg.Ports[0].Port = btc09StratumPort + 1
	inst, err := coininstance.Start(ctx, cfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	miner := btc09TestAddress(0x20)
	btc09MineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", btc09StratumPort+1), miner, 2)

	var blocksFound int
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "btc09demo")
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
	snap, _ := inst.Ledger().Snapshot(ctx, "btc09demo")
	if snap.Orphaned < 1 {
		t.Fatalf("应判至少 1 个孤块: %+v", snap)
	}
	if snap.Confirmed != 0 {
		t.Fatalf("孤块不应有确认: %+v", snap)
	}
	if node.sentCount() != 0 {
		t.Fatal("孤块不应打款")
	}
	t.Logf("✓ 09C 孤块路径：%d 个块判 ORPHANED，未误打款", snap.Orphaned)
}
