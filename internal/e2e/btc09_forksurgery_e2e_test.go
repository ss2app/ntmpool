package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/payout"
)

func TestBtc09ForkSurgery(t *testing.T) {
	if testing.Short() {
		t.Skip("真 Argon2id e2e 较重，-short 跳过")
	}
	node := newFakeBtc09Node()
	defer node.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := btc09TestConfig(node.URL())
	cfg.Ports[0].Port = btc09StratumPort + 2
	inst, err := coininstance.Start(ctx, cfg, coininstance.Deps{Payouts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()

	miner := btc09TestAddress(0x30)
	btc09MineUntilAccepted(t, fmt.Sprintf("127.0.0.1:%d", cfg.Ports[0].Port), miner, 2)
	blocksFound := 0
	for i := 0; i < 50; i++ {
		snap, _ := inst.Ledger().Snapshot(ctx, cfg.ID)
		blocksFound = snap.BlocksFound
		if blocksFound >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocksFound == 0 {
		t.Fatal("未爆块，fork 手术测试无从执行")
	}
	node.advance(200)
	if err := inst.RunPayoutNow(ctx); err != nil {
		t.Fatalf("确认并打款: %v", err)
	}

	all, err := inst.Batches().All()
	if err != nil {
		t.Fatal(err)
	}
	var ghost *payout.Batch
	for _, b := range all {
		if b.Kind == "payout" {
			ghost = b
			break
		}
	}
	if ghost == nil || ghost.TxID == "" || ghost.Status != core.PaymentSent {
		t.Fatalf("未形成 sent payout 批次: %+v", all)
	}
	ghostAmount := ghost.Outputs[miner]
	if ghostAmount == "" {
		t.Fatalf("幽灵批次缺矿工输出: %+v", ghost.Outputs)
	}
	// 模拟事故现场：节点从未收录 tx，但数据库批次被错误推进为 confirmed。
	node.mu.Lock()
	delete(node.txConf, ghost.TxID)
	node.mu.Unlock()
	ghost.Status = core.PaymentConfirmed
	if err := inst.Batches().Save(ghost); err != nil {
		t.Fatal(err)
	}

	if err := inst.VoidPayoutBatch(ctx, ghost.ID); err != nil {
		t.Fatalf("作废幽灵批次: %v", err)
	}
	if err := inst.VoidPayoutBatch(ctx, ghost.ID); err != nil {
		t.Fatalf("重复作废应幂等: %v", err)
	}
	voided, ok, err := inst.Batches().Load(ghost.ID)
	if err != nil || !ok || voided.Status != core.PaymentVoided {
		t.Fatalf("批次未 voided: ok=%v err=%v batch=%+v", ok, err, voided)
	}
	unf, err := inst.Batches().Unfinished()
	if err != nil || len(unf) != 0 {
		t.Fatalf("voided 后不应有在飞批次: err=%v batches=%+v", err, unf)
	}
	refunded, err := inst.Ledger().Snapshot(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refunded.Balances[miner] != ghostAmount || refunded.TotalPaid != "0.00000000" {
		t.Fatalf("幽灵退款错误: balance=%s want=%s paid=%s",
			refunded.Balances[miner], ghostAmount, refunded.TotalPaid)
	}

	blocks, _, err := inst.Ledger().Blocks(ctx, cfg.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var toxic []core.FoundBlock
	for _, b := range blocks {
		if b.Status == core.BlockConfirmed {
			toxic = append(toxic, b)
		}
	}
	if len(toxic) == 0 {
		t.Fatal("没有可供 fork 手术的 confirmed 块")
	}
	for _, b := range toxic {
		if err := inst.ForceOrphanBlock(ctx, b.Hash, int64(b.Height)); err != nil {
			t.Fatalf("强制孤块 %s: %v", b.Hash, err)
		}
	}
	if err := inst.ForceOrphanBlock(ctx, toxic[0].Hash, int64(toxic[0].Height)); err != nil {
		t.Fatalf("重复强制孤块应幂等: %v", err)
	}

	final, err := inst.Ledger().Snapshot(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance := final.Balances[miner]; balance != "" && balance != "0.00000000" {
		t.Fatalf("毒块冲销后余额=%s", balance)
	}
	if final.DebtsNet != "0.00000000" || final.TotalPaid != "0.00000000" || final.Confirmed != 0 ||
		final.Orphaned != len(toxic) {
		t.Fatalf("fork 手术后投影错误: %+v toxic=%d", final, len(toxic))
	}
	if delta, err := inst.RunReconcile(ctx); err != nil || delta != "0.00000000" {
		t.Fatalf("fork 手术后守恒: delta=%s err=%v", delta, err)
	}
}
