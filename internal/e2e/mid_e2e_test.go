package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/hasher"
)

const midStratumPort = 14931

func midTestAddress(tag byte) string {
	b := sha256.Sum256([]byte{tag})
	return hex.EncodeToString(b[:])
}

func midTestConfig(nodeURL string, port int) config.CoinConfig {
	return config.CoinConfig{
		ID: "midsdemo", Symbol: "MDS", Adapter: "midstate-rpc", Algo: "midvdf",
		Decimals:    9, // 1 gMDS = 1e9 units：账本整数 = 链上 units
		Nodes:       []config.NodeEndpoint{{URL: nodeURL}},
		PoolAddress: midTestAddress(0xF0), // 费/残差收款（hex64）
		Ports: []config.PortConfig{{
			Port: port, Mode: "pplns", Dialect: "midstate", Enabled: true,
			// 真 midvdf ≈150ms/VDF：diff 2 ≈ 每 ~2 个 VDF 一个 share，秒级出块
			Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 2, MinDiff: 1, MaxDiff: 1 << 40, TargetSeconds: 5},
		}},
		Payout: config.PayoutConfig{
			// enabled=false 永不开（无池端转账腿）；classify/直付入账照跑。
			// minPayout 兼作尘埃阈值：0.000001 gMDS = 1000 units
			Enabled: false, PplnsFactor: 2, FeePercent: 5, MinPayout: "0.000001",
			Confirmations: 3, IntervalSec: 600,
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

// midMineUntilAccepted 最小 midstate 协议矿工（NTMminer -a midstate 行为子集，
// nm_stratum.c:1027 镜像）：subscribe → 收 job（subscribe result 即首个 job）→
// 自播种 nonce 真 midvdf（全量 1M 迭代）碾到 final_hash < target → submit
// {job_id, nonce, final_hash}（final_hash 必带 = 顺带压 badpow tripwire 路径）。
// done 非 nil 时挖到 done() 为真才收工（如「矿工已被 coinbase 直付」）。
func midMineUntilAccepted(t *testing.T, addr, minerAddr string, need int, done func() bool) {
	t.Helper()
	h, err := hasher.Get("midvdf")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("连接 midstate stratum 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second))

	enc := jsonEncoder(conn)
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if err := enc(map[string]any{
		"id": 1, "method": "subscribe",
		"params": map[string]any{"address": minerAddr, "device": "e2e-rig"},
	}); err != nil {
		t.Fatal(err)
	}

	type wireJob struct {
		JobID    uint64 `json:"job_id"`
		Midstate string `json:"midstate"`
		Target   string `json:"target"`
	}
	var current wireJob

	// subscribe result 就是首个 job（fork 池协议）
	if !sc.Scan() {
		t.Fatalf("subscribe 无响应: %v", sc.Err())
	}
	var subResp struct {
		Result *wireJob `json:"result"`
		Error  any      `json:"error"`
	}
	mustUnmarshal(t, sc.Bytes(), &subResp)
	if subResp.Error != nil || subResp.Result == nil {
		t.Fatalf("subscribe 失败: %s", sc.Text())
	}
	current = *subResp.Result
	if len(current.Midstate) != 64 || len(current.Target) != 64 {
		t.Fatalf("job 字段非法: %+v", current)
	}

	accepted := 0
	nonce := uint64(0x00e2e00000000000) // 自播种起点（协议无窗口分区）
	deadline := time.Now().Add(110 * time.Second)

	for (accepted < need || (done != nil && !done())) && time.Now().Before(deadline) {
		ms, _ := hex.DecodeString(current.Midstate)
		tb, _ := hex.DecodeString(current.Target)

		// 真 midvdf 碾 nonce（全量 1M 迭代，与池端/假节点同一把尺）
		var foundNonce uint64
		var foundHash []byte
		for foundHash == nil && time.Now().Before(deadline) {
			fh, err := h.Hash(hasher.MidSeed(ms, nonce))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Compare(string(fh), string(tb)) < 0 {
				foundNonce, foundHash = nonce, fh
			}
			nonce++
		}
		if foundHash == nil {
			break
		}
		if err := enc(map[string]any{
			"id": 5, "method": "submit",
			"params": map[string]any{
				"job_id":     current.JobID,
				"nonce":      foundNonce,
				"final_hash": hex.EncodeToString(foundHash),
			},
		}); err != nil {
			t.Fatal(err)
		}

	waitResp:
		for sc.Scan() {
			var msg struct {
				Method string         `json:"method"`
				Params *wireJob       `json:"params"`
				Result map[string]any `json:"result"`
				Error  any            `json:"error"`
			}
			mustUnmarshal(t, sc.Bytes(), &msg)
			switch {
			case msg.Method == "job" && msg.Params != nil:
				current = *msg.Params // 换工/新 vardiff target（同 midstate 在飞工作不丢）
			case msg.Error != nil:
				if s, _ := msg.Error.(string); strings.Contains(s, "mismatch") {
					t.Fatalf("badpow tripwire 触发（共识失配！）: %s", s)
				}
				// stale 等：池在应答后必推最新 job——再读一行拿到新工再战
				if sc.Scan() {
					var next struct {
						Method string   `json:"method"`
						Params *wireJob `json:"params"`
					}
					mustUnmarshal(t, sc.Bytes(), &next)
					if next.Method == "job" && next.Params != nil {
						current = *next.Params
					}
				}
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
		t.Fatalf("midstate 矿工只有 %d/%d 个 share 被接受", accepted, need)
	}
	t.Logf("✓ midstate 矿工 accepted=%d（真 midvdf 1M 迭代）", accepted)
}

func TestMidstateFullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("真 midvdf e2e 较重，-short 跳过")
	}
	node := newFakeMidNode()
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, midTestConfig(node.URL(), midStratumPort), coininstance.Deps{Payouts: false})
	if err != nil {
		t.Fatalf("启动 midstate 币实例失败: %v", err)
	}
	defer inst.Stop()

	miner := midTestAddress(0x10)
	// 挖到「矿工真的被 coinbase 直付」为止：首个模板窗口为空（冷启动，全额归费），
	// 首个 accepted share 触发同高度补切，之后的块才带矿工直付输出。
	midMineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", midStratumPort), miner, 2,
		func() bool { return len(node.minerPaidDirect(miner)) > 0 })

	var blocksFound int
	for i := 0; i < 100; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "midsdemo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("池未记录 midstate 爆块")
	}

	// ★直付契约：假节点收下的 coinbase 里必须有直接付给矿工地址的 2 的幂输出
	paid := node.minerPaidDirect(miner)
	if len(paid) == 0 {
		t.Fatal("coinbase 未直付矿工（model-a 直付契约破坏）")
	}
	var paidSum uint64
	for _, v := range paid {
		if v == 0 || v&(v-1) != 0 {
			t.Fatalf("直付输出非 2 的幂: %d", v)
		}
		paidSum += v
	}
	t.Logf("✓ coinbase 直付矿工 %d 个 2 的幂输出，共 %d units（爆块即到账）", len(paid), paidSum)

	// 确认后：直付块按快照入账（TotalPaid=Σpaid，txid=块 hash），守恒 delta=0
	node.advance(5)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("classify 周期失败: %v", err)
	}
	snap, _ := inst.Ledger().Snapshot(ctx, "midsdemo")
	if snap.Confirmed < 1 {
		t.Fatalf("midstate 块未确认入账: %+v", snap)
	}
	if snap.TotalPaid == "0.000000000" {
		t.Fatalf("确认后 TotalPaid 应记直付金额: %+v", snap)
	}
	if delta, _ := inst.RunReconcile(ctx); delta != "0.000000000" {
		t.Fatalf("守恒破坏 delta=%s", delta)
	}
	ms, ok, err := inst.Ledger().MinerSummary(ctx, "midsdemo", miner)
	if err != nil || !ok {
		t.Fatalf("矿工自查应有记录: %v", err)
	}
	t.Logf("✓ midstate 全链路端到端通过：NTMminer 方言矿工→1M VDF 重算→Expected 重试→"+
		"直付 coinbase→节点真 VDF 验块→确认→快照入账（矿工已付=%s）→守恒 delta=0", ms.TotalPaid)
}

func TestMidstateOrphanPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("真 midvdf e2e 较重，-short 跳过")
	}
	node := newFakeMidNode()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, midTestConfig(node.URL(), midStratumPort+1), coininstance.Deps{Payouts: false})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	miner := midTestAddress(0x20)
	midMineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", midStratumPort+1), miner, 2, nil)

	var blocksFound int
	for i := 0; i < 100; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, "midsdemo")
		if blocksFound = snap.BlocksFound; blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound < 1 {
		t.Fatal("未爆块，孤块测试无从谈起")
	}

	node.orphanAll()
	node.advance(5)
	_ = inst.RunPayoutNow(ctx)
	snap, _ := inst.Ledger().Snapshot(ctx, "midsdemo")
	if snap.Orphaned < 1 {
		t.Fatalf("应判至少 1 个孤块: %+v", snap)
	}
	if snap.Confirmed != 0 {
		t.Fatalf("孤块不应有确认: %+v", snap)
	}
	if snap.TotalPaid != "0.000000000" {
		t.Fatalf("孤块不应记直付（coinbase 没上链）: %+v", snap)
	}
	if delta, _ := inst.RunReconcile(ctx); delta != "0.000000000" {
		t.Fatalf("孤块后守恒破坏 delta=%s", delta)
	}
	t.Logf("✓ midstate 孤块路径：%d 个块判 ORPHANED，直付账务干净回滚", snap.Orphaned)
}
