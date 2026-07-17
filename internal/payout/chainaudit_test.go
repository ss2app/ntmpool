package payout

import (
	"context"
	"errors"
	"testing"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
)

type chainAuditWallet struct {
	outbound    []adapter.OutboundTx
	outboundErr error
	coinbase    []adapter.CoinbaseReceipt
	coinbaseErr error
}

func (*chainAuditWallet) SendMany(context.Context, map[string]string) (string, error) {
	return "unused", nil
}
func (*chainAuditWallet) TxConfirmations(context.Context, string) (int64, error) { return 0, nil }
func (w *chainAuditWallet) ListRecentOutbound(context.Context, uint64) ([]adapter.OutboundTx, error) {
	return w.outbound, w.outboundErr
}
func (w *chainAuditWallet) ListRecentCoinbase(context.Context, uint64) ([]adapter.CoinbaseReceipt, error) {
	return w.coinbase, w.coinbaseErr
}

type noChainAuditWallet struct{}

func (*noChainAuditWallet) SendMany(context.Context, map[string]string) (string, error) {
	return "unused", nil
}
func (*noChainAuditWallet) TxConfirmations(context.Context, string) (int64, error) { return 0, nil }

type allFailBatchStore struct{ *MemBatchStore }

func (s *allFailBatchStore) FindByTxID(string) (*Batch, bool, error) {
	return nil, false, errors.New("database down")
}

func newChainAuditEngine(wallet adapter.WalletAdapter, store BatchStore) *Engine {
	ledger := accounting.NewMemLedger(8, 2)
	node := &fakeNode{conf: map[string]int64{}, mainHash: map[uint64]string{}}
	enabled := true
	return NewEngine(Config{Coin: "audit", Decimals: 8, MinPayout: 1, Maturity: 100,
		ChainAuditEnabled: &enabled}, ledger, node, wallet, store)
}

func TestChainAuditEnablement(t *testing.T) {
	trueValue, falseValue := true, false
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{name: "mem store auto", enabled: nil, want: false},
		{name: "mem store explicit true", enabled: &trueValue, want: true},
		{name: "explicit false", enabled: &falseValue, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := accounting.NewMemLedger(8, 2)
			node := &fakeNode{conf: map[string]int64{}, mainHash: map[uint64]string{}}
			engine := NewEngine(Config{Coin: "audit", ChainAuditEnabled: tt.enabled}, ledger, node,
				&chainAuditWallet{}, NewMemBatchStore())
			if got := engine.chainAudit != nil; got != tt.want {
				t.Fatalf("chainAudit 构造状态=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestChainAuditUnknownOutboundFreezesAndEmits(t *testing.T) {
	wallet := &chainAuditWallet{outbound: []adapter.OutboundTx{{
		TxID: "unknown-tx", Confirmations: 2, Height: 123,
		Outputs: []adapter.OutboundOutput{{Address: "external", Amount: 42}},
	}}}
	engine := newChainAuditEngine(wallet, NewMemBatchStore())
	var events []string
	engine.SetEvents(func(kind, _ string, _ map[string]string) { events = append(events, kind) })

	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !engine.Frozen() {
		t.Fatal("确证未知出账后必须冻结该币打款")
	}
	if len(events) != 1 || events[0] != "chain_audit_unknown_outbound" {
		t.Fatalf("未知出账应 emit P0 事件，got=%v", events)
	}
	engine.Unfreeze()
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !engine.Frozen() {
		t.Fatal("未知出账仍存在时，人工解冻后的下一轮必须重新冻结")
	}
	if len(events) != 1 {
		t.Fatalf("同一未知 tx 重复审计不应刷屏，got=%v", events)
	}
}

func TestChainAuditKnownOutboundDoesNotFreeze(t *testing.T) {
	store := NewMemBatchStore()
	if err := store.Save(&Batch{ID: 1, Kind: "payout", PlannedTxID: "known-tx",
		Outputs: map[string]string{"external": "0.00000042"}, Status: core.PaymentConfirmed}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&Batch{ID: 2, Kind: "fee_collect", TxID: "known-fee-tx",
		Outputs: map[string]string{"fee": "0.00000007"}, Status: core.PaymentConfirmed}); err != nil {
		t.Fatal(err)
	}
	wallet := &chainAuditWallet{outbound: []adapter.OutboundTx{
		{TxID: "known-tx", Confirmations: 4,
			Outputs: []adapter.OutboundOutput{{Address: "external", Amount: 42}}},
		{TxID: "known-fee-tx", Confirmations: 2,
			Outputs: []adapter.OutboundOutput{{Address: "fee", Amount: 7}}},
	}}
	engine := newChainAuditEngine(wallet, store)
	var events []string
	engine.SetEvents(func(kind, _ string, _ map[string]string) { events = append(events, kind) })

	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.Frozen() {
		t.Fatal("TxID/PlannedTxID 能匹配 batch 时不得冻结")
	}
	if len(events) != 0 {
		t.Fatalf("已授权出账不应告警，got=%v", events)
	}
}

func TestChainAuditUnavailableSkipsWithoutError(t *testing.T) {
	engine := newChainAuditEngine(&noChainAuditWallet{}, NewMemBatchStore())
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.Frozen() {
		t.Fatal("适配器不实现 ChainAuditor 时必须跳过且不冻结")
	}
}

func TestChainAuditReadFailureDoesNotFreeze(t *testing.T) {
	engine := newChainAuditEngine(&chainAuditWallet{outboundErr: errors.New("node down")}, NewMemBatchStore())
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.Frozen() {
		t.Fatal("查链失败不能被当作未知出账证据")
	}
}

func TestChainAuditBatchLookupFailureDoesNotFreeze(t *testing.T) {
	wallet := &chainAuditWallet{outbound: []adapter.OutboundTx{{
		TxID: "cannot-prove", Outputs: []adapter.OutboundOutput{{Address: "external", Amount: 9}},
	}}}
	store := &allFailBatchStore{MemBatchStore: NewMemBatchStore()}
	engine := newChainAuditEngine(wallet, store)
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.Frozen() {
		t.Fatal("批次库查询失败时无法确证未知出账，不得冻结")
	}
}

func TestChainAuditAndConservationBothEmit(t *testing.T) {
	wallet := &chainAuditWallet{outbound: []adapter.OutboundTx{{
		TxID: "unknown-with-delta", Outputs: []adapter.OutboundOutput{{Address: "external", Amount: 1}},
	}}}
	ledger := &brokenLedger{MemLedger: accounting.NewMemLedger(8, 2)}
	node := &fakeNode{conf: map[string]int64{}, mainHash: map[uint64]string{}}
	enabled := true
	engine := NewEngine(Config{Coin: "audit", Decimals: 8, MinPayout: 1, Maturity: 100, ChainAuditEnabled: &enabled},
		ledger, node, wallet, NewMemBatchStore())
	seen := map[string]bool{}
	engine.SetEvents(func(kind, _ string, _ map[string]string) { seen[kind] = true })
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !seen["reconcile_frozen"] || !seen["chain_audit_unknown_outbound"] {
		t.Fatalf("守恒与链审计应互补且分别告警: %v", seen)
	}
}

func TestChainAuditOwnTransferAndMissingRound(t *testing.T) {
	wallet := &chainAuditWallet{
		outbound: []adapter.OutboundTx{{TxID: "self", Outputs: []adapter.OutboundOutput{{Address: "mine", Amount: 7, IsMine: true}}}},
		coinbase: []adapter.CoinbaseReceipt{{TxID: "coinbase", BlockHash: "missing-block", Height: 321, Amount: 5_000_000_000}},
	}
	engine := newChainAuditEngine(wallet, NewMemBatchStore())
	var events []string
	engine.SetEvents(func(kind, _ string, _ map[string]string) { events = append(events, kind) })
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.Frozen() {
		t.Fatal("全自有地址转移和 missing round 都不应冻结")
	}
	if len(events) != 1 || events[0] != "chain_audit_missing_round" {
		t.Fatalf("missing round 应只告警，got=%v", events)
	}
}

func TestBatchPolicyVersionChangesWithHotPolicy(t *testing.T) {
	store := NewMemBatchStore()
	engine := newChainAuditEngine(&noChainAuditWallet{}, store)
	if _, err := engine.FeeCollect(context.Background(), "fee-address", "0.00000001"); err != nil {
		t.Fatal(err)
	}
	engine.SetParams(2.5, 1, 120)
	if _, err := engine.FeeCollect(context.Background(), "fee-address", "0.00000002"); err != nil {
		t.Fatal(err)
	}
	all, err := store.All()
	if err != nil || len(all) != 2 {
		t.Fatalf("读取测试批次失败: n=%d err=%v", len(all), err)
	}
	second, first := all[0], all[1]
	if first.FeePolicyVersion == "" || first.ConfirmationPolicyVersion == "" {
		t.Fatal("新建打款批次必须固化 policy version")
	}
	if first.FeePolicyVersion == second.FeePolicyVersion {
		t.Fatal("费率变化必须产生新的 fee policy version")
	}
	if first.ConfirmationPolicyVersion == second.ConfirmationPolicyVersion {
		t.Fatal("确认策略变化必须产生新的 confirmation policy version")
	}
}
