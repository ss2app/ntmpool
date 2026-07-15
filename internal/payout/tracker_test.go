package payout

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
)

// fakeCoinWallet 模拟 btc09/dragonx 类「只 sendmany、无拆步」的钱包（e.rawtx==nil，
// 走 payoutOneBatch 路径③）。实现 adapter.TxTracker，可控每笔 tx 的 (conf, known)。
type fakeCoinWallet struct {
	sendErr error                // SendMany 返回的错误（测 ErrNotBroadcast）
	txSeq   int
	status  map[string]txStat    // txid → 追踪状态
}

type txStat struct {
	conf  int64
	known bool
}

func newFakeCoinWallet() *fakeCoinWallet {
	return &fakeCoinWallet{status: map[string]txStat{}}
}

func (w *fakeCoinWallet) SpendableBalance(context.Context) (string, error) { return "1000000.0", nil }
func (w *fakeCoinWallet) SendMany(context.Context, map[string]string) (string, error) {
	if w.sendErr != nil {
		return "", w.sendErr
	}
	w.txSeq++
	return fmt.Sprintf("tx%d", w.txSeq), nil
}
func (w *fakeCoinWallet) TxConfirmations(_ context.Context, txid string) (int64, error) {
	return w.status[txid].conf, nil
}
func (w *fakeCoinWallet) TxStatus(_ context.Context, txid string) (int64, bool, error) {
	s := w.status[txid]
	return s.conf, s.known, nil
}

var (
	_ adapter.WalletAdapter = (*fakeCoinWallet)(nil)
	_ adapter.TxTracker     = (*fakeCoinWallet)(nil)
)

func setupCoin(t *testing.T) (*Engine, *accounting.MemLedger, *fakeNode, *fakeCoinWallet) {
	t.Helper()
	l := accounting.NewMemLedger(8, 2)
	node := &fakeNode{conf: map[string]int64{}, mainHash: map[uint64]string{}}
	w := newFakeCoinWallet()
	cfg := Config{Coin: "t", Decimals: 8, FeePercent: 0, MinPayout: 1.0, Maturity: 100}
	e := NewEngine(cfg, l, node, w, NewMemBatchStore())
	return e, l, node, w
}

// confirmAndPay 记一份 share + 一个成熟块 → RunOnce 确认分账并 sendmany 打款，
// 返回那笔 payout 批次（sendmany 路径：TxID 已设、无 rawtx）。
func confirmAndPay(t *testing.T, ctx context.Context, e *Engine, l *accounting.MemLedger, node *fakeNode, addr, hash string) *Batch {
	t.Helper()
	_ = l.RecordShare(ctx, core.Share{Coin: "t", Address: addr}, 1)
	b := core.FoundBlock{Coin: "t", Height: 100, Hash: hash, Finder: addr,
		Reward: "50.00000000", NetDiff: 1, Status: core.BlockPending}
	_ = l.RecordBlock(ctx, b, "raw")
	node.conf[hash] = 100
	node.mainHash[100] = hash
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	all, _ := e.store.All()
	for _, batch := range all {
		if batch.Kind == "payout" && batch.TxID != "" {
			return batch
		}
	}
	t.Fatal("未产生 sendmany payout 批次")
	return nil
}

// 追踪器：sent 打款达确认阈值 → 标 confirmed + 维护 confirmations 显示。
func TestTrackConfirmsSentPayout(t *testing.T) {
	ctx := context.Background()
	e, l, node, w := setupCoin(t)
	pb := confirmAndPay(t, ctx, e, l, node, "A", "h1")
	if pb.Status != core.PaymentSent {
		t.Fatalf("打款后应为 sent，得 %s", pb.Status)
	}
	// 该 tx 现有 5 确认（≥ 默认阈值 3）
	w.status[pb.TxID] = txStat{conf: 5, known: true}
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, _ := e.store.Load(pb.ID)
	if got.Status != core.PaymentConfirmed {
		t.Fatalf("应标 confirmed，得 %s", got.Status)
	}
	if got.Confirmations != 5 {
		t.Fatalf("confirmations 显示应为 5，得 %d", got.Confirmations)
	}
}

// 追踪器：sent 打款确定丢失（!known 且过 grace）→ 退回余额 + 标 failed；因 trackSent
// 在 payout 之前跑，同一轮 payout 立即用新 txid 重付（自愈），矿工不需人工介入。
func TestTrackRefundsDroppedPayout(t *testing.T) {
	ctx := context.Background()
	e, l, node, w := setupCoin(t)
	e.cfg.DropGrace = time.Nanosecond // 任何 age 即算过 grace
	pb := confirmAndPay(t, ctx, e, l, node, "A", "h1")
	// 打款后 A 余额已清空（先扣）
	if snap, _ := l.Snapshot(ctx, "t"); snap.Balances["A"] != "0.00000000" {
		t.Fatalf("打款后 A 应为 0，得 %s", snap.Balances["A"])
	}
	// 该 tx 既不在 mempool 也不在链上（广播后节点重启丢失）
	w.status[pb.TxID] = txStat{conf: 0, known: false}
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 原批次标 failed（已退款）
	got, _, _ := e.store.Load(pb.ID)
	if got.Status != core.PaymentFailed {
		t.Fatalf("确定丢失应标 failed，得 %s", got.Status)
	}
	// 自愈：同轮已用新 txid 重付 A（新 payout 批次），A 余额回 0
	all, _ := e.store.All()
	var repaid bool
	for _, batch := range all {
		if batch.Kind == "payout" && batch.ID != pb.ID && batch.TxID != "" && batch.Outputs["A"] != "" {
			repaid = true
		}
	}
	if !repaid {
		t.Fatal("确定丢失后应在同轮用新 txid 自动重付 A")
	}
	if snap, _ := l.Snapshot(ctx, "t"); snap.Balances["A"] != "0.00000000" {
		t.Fatalf("自愈重付后 A 应为 0，得 %s", snap.Balances["A"])
	}
	// 守恒始终不破（退款 + 重付都记账）
	if delta, _ := l.Reconcile(ctx, "t"); delta != "0.00000000" {
		t.Fatalf("退款+重付后守恒破坏 delta=%s", delta)
	}
}

// 追踪器安全性：sent 打款 0 确认但仍在 mempool（known=true）→ 绝不退款，保持 confirming。
func TestTrackNeverRefundsMempoolPending(t *testing.T) {
	ctx := context.Background()
	e, l, node, w := setupCoin(t)
	e.cfg.DropGrace = time.Nanosecond
	pb := confirmAndPay(t, ctx, e, l, node, "A", "h1")
	// 0 确认但仍在 mempool 排队
	w.status[pb.TxID] = txStat{conf: 0, known: true}
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, _ := e.store.Load(pb.ID)
	if got.Status != core.PaymentConfirming {
		t.Fatalf("仍在 mempool 应为 confirming，得 %s", got.Status)
	}
	// 绝不退款：A 仍是 0（钱在途，未退回）
	if snap, _ := l.Snapshot(ctx, "t"); snap.Balances["A"] != "0.00000000" {
		t.Fatalf("在途 tx 绝不退款，A 应保持 0，得 %s", snap.Balances["A"])
	}
}

// 追踪器：冻结中即使确定丢失也不自动退款（改余额动作），只告警待人工。
func TestTrackFrozenNoRefund(t *testing.T) {
	ctx := context.Background()
	e, l, node, w := setupCoin(t)
	e.cfg.DropGrace = time.Nanosecond
	pb := confirmAndPay(t, ctx, e, l, node, "A", "h1")
	w.status[pb.TxID] = txStat{conf: 0, known: false}
	e.mu.Lock()
	e.frozen = true
	e.mu.Unlock()
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, _ := e.store.Load(pb.ID)
	if got.Status == core.PaymentFailed {
		t.Fatal("冻结中不应自动退款标 failed")
	}
	if snap, _ := l.Snapshot(ctx, "t"); snap.Balances["A"] != "0.00000000" {
		t.Fatalf("冻结中不应退款，A 应保持 0，得 %s", snap.Balances["A"])
	}
}

// ErrNotBroadcast：sendmany 确证未广播（如 dragonx z_sendmany 操作失败）→
// payoutOneBatch 立即退回余额 + 标 failed（不是 unknown 人工态）。
func TestSendManyErrNotBroadcastRefunds(t *testing.T) {
	ctx := context.Background()
	e, l, node, w := setupCoin(t)
	// 让 sendmany 确证未广播
	w.sendErr = fmt.Errorf("操作失败（未广播）: shielded requirements not met: %w", adapter.ErrNotBroadcast)

	_ = l.RecordShare(ctx, core.Share{Coin: "t", Address: "A"}, 1)
	b := core.FoundBlock{Coin: "t", Height: 100, Hash: "h", Finder: "A",
		Reward: "50.00000000", NetDiff: 1, Status: core.BlockPending}
	_ = l.RecordBlock(ctx, b, "raw")
	node.conf["h"] = 100
	node.mainHash[100] = "h"

	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 确证未广播 → 退回余额（A 保持 50，下轮重付），批次 failed
	if snap, _ := l.Snapshot(ctx, "t"); snap.Balances["A"] != "50.00000000" {
		t.Fatalf("ErrNotBroadcast 应退回余额，A 应为 50，得 %s", snap.Balances["A"])
	}
	all, _ := e.store.All()
	var failed int
	for _, batch := range all {
		if batch.Kind == "payout" && batch.Status == core.PaymentFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("应有 1 笔 failed payout 批次，得 %d", failed)
	}
	if delta, _ := l.Reconcile(ctx, "t"); delta != "0.00000000" {
		t.Fatalf("退款后守恒破坏 delta=%s", delta)
	}
}
