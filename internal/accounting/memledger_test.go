package accounting

import (
	"context"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

func mkShare(addr string, at time.Time) core.Share {
	return core.Share{Coin: "t", Address: addr, At: at}
}

func mkBlock(hash, finder, reward string, netDiff float64, solo bool) core.FoundBlock {
	return core.FoundBlock{
		Coin: "t", Height: 100, Hash: hash, Finder: finder,
		Reward: reward, NetDiff: netDiff, Status: core.BlockPending, Solo: solo,
	}
}

func TestParseToStrRoundTrip(t *testing.T) {
	l := NewMemLedger(8, 2)
	for _, s := range []string{"0.00000000", "50.00000000", "1.23456789", "0.00000001", "1000.50000000"} {
		v, err := l.parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := l.toStr(v); got != s {
			t.Errorf("round trip %s → %d → %s", s, v, got)
		}
	}
}

// PPLNS 分账守恒：两矿工按份额分，且分毫不差（余数归最大占比者）。
func TestPPLNSConservationAndSplit(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	now := time.Unix(1000, 0)
	// A 交 3 份权重 1，B 交 1 份权重 1 → A:B = 3:1
	for i := 0; i < 3; i++ {
		_ = l.RecordShare(ctx, mkShare("A", now), 1)
	}
	_ = l.RecordShare(ctx, mkShare("B", now), 1)

	b := mkBlock("h1", "A", "50.00000000", 10.0, false) // netDiff 10 → 窗口权重 20，框住全部 4 份
	_ = l.RecordBlock(ctx, b, "raw")
	if err := l.ConfirmBlock(ctx, b, 10.0); err != nil { // 10% 费
		t.Fatal(err)
	}
	snap, _ := l.Snapshot(ctx, "t")
	// distributable = 45；A=3/4→33.75, B=1/4→11.25
	if snap.Balances["A"] != "33.75000000" || snap.Balances["B"] != "11.25000000" {
		t.Fatalf("分账错误: A=%s B=%s", snap.Balances["A"], snap.Balances["B"])
	}
	if snap.TotalFees != "5.00000000" {
		t.Fatalf("费错误: %s", snap.TotalFees)
	}
	// 守恒：确认奖励 50 = 已付0 + 余额45 + 费5 + 债务0
	delta, _ := l.Reconcile(ctx, "t")
	if delta != "0.00000000" {
		t.Fatalf("守恒破坏 delta=%s", delta)
	}
}

// Snipa22 #349 冷启动 bug 修法：窗口远不满（share 少）也必须付满 100%，不沉淀。
func TestPPLNSWindowNotFullPaysFull(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	// 只有 1 个 share，但网络难度巨大（窗口权重远大于已有份额）
	_ = l.RecordShare(ctx, mkShare("A", time.Unix(1, 0)), 1)
	b := mkBlock("h1", "A", "50.00000000", 1e6, false) // 窗口权重 2e6 >> 1
	_ = l.RecordBlock(ctx, b, "raw")
	_ = l.ConfirmBlock(ctx, b, 0)
	snap, _ := l.Snapshot(ctx, "t")
	if snap.Balances["A"] != "50.00000000" {
		t.Fatalf("窗口不满也应付满 100%%: %s", snap.Balances["A"])
	}
	delta, _ := l.Reconcile(ctx, "t")
	if delta != "0.00000000" {
		t.Fatalf("守恒破坏: %s", delta)
	}
}

// SOLO：全归爆块者（扣费后）。
func TestSoloPayout(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	_ = l.RecordShare(ctx, mkShare("A", time.Unix(1, 0)), 1)
	b := mkBlock("h1", "B", "50.00000000", 1.0, true) // solo，B 爆块
	_ = l.RecordBlock(ctx, b, "raw")
	_ = l.ConfirmBlock(ctx, b, 0)
	snap, _ := l.Snapshot(ctx, "t")
	if snap.Balances["B"] != "50.00000000" || snap.Balances["A"] != "" {
		t.Fatalf("solo 应全归爆块者 B: %+v", snap.Balances)
	}
}

// 孤块追缴：已入账并部分打款后，块孤了 → 扣余额 + 剩余记 debt，后续收益抵扣。
func TestOrphanDebtRecovery(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	_ = l.RecordShare(ctx, mkShare("A", time.Unix(1, 0)), 1)
	b := mkBlock("h1", "A", "50.00000000", 1.0, false)
	_ = l.RecordBlock(ctx, b, "raw")
	_ = l.ConfirmBlock(ctx, b, 0) // A 得 50

	// A 打款 40（预打款），余 10
	_ = l.DeductForPayout(ctx, "t", map[string]string{"A": "40.00000000"}, 1)
	snap, _ := l.Snapshot(ctx, "t")
	if snap.Balances["A"] != "10.00000000" {
		t.Fatalf("打款后余额错误: %s", snap.Balances["A"])
	}

	// 块孤了：应扣回余额 10，剩 40 记 debt
	_ = l.OrphanBlock(ctx, b)
	snap, _ = l.Snapshot(ctx, "t")
	if snap.Balances["A"] != "0.00000000" {
		t.Fatalf("孤块后余额应清零: %s", snap.Balances["A"])
	}
	if snap.DebtsNet != "40.00000000" {
		t.Fatalf("应记 40 债务: %s", snap.DebtsNet)
	}

	// A 后续再挖到一个确认块 30 → 先抵债务 30，余额 0，债务剩 10
	_ = l.RecordShare(ctx, mkShare("A", time.Unix(2, 0)), 1)
	b2 := mkBlock("h2", "A", "30.00000000", 1.0, false)
	_ = l.RecordBlock(ctx, b2, "raw")
	_ = l.ConfirmBlock(ctx, b2, 0)
	snap, _ = l.Snapshot(ctx, "t")
	if snap.Balances["A"] != "0.00000000" || snap.DebtsNet != "10.00000000" {
		t.Fatalf("债务抵扣错误: 余额=%s 债务=%s", snap.Balances["A"], snap.DebtsNet)
	}
}

// 起付额门槛 + 矿工自设覆盖。
func TestPayableThreshold(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	l.balances = map[string]int64{
		"A": 500000000,  // 5.0
		"B": 50000000,   // 0.5
		"C": 2500000000, // 25.0
	}
	// 默认门槛 1.0：A、C 可打，B 不够
	pay, _ := l.PayableBalances(ctx, "t", 1.0, nil)
	if _, ok := pay["B"]; ok {
		t.Fatal("B 不应达门槛")
	}
	if pay["A"] != "5.00000000" || pay["C"] != "25.00000000" {
		t.Fatalf("可打款集合错误: %+v", pay)
	}
	// C 自设起付额 30 → C 不再达标
	pay, _ = l.PayableBalances(ctx, "t", 1.0, map[string]float64{"C": 30})
	if _, ok := pay["C"]; ok {
		t.Fatal("C 自设 30 门槛后不应可打")
	}
}

// 余额不足打款必须整体失败（事务语义，不能扣一半）。
func TestDeductInsufficient(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	l.balances = map[string]int64{"A": 1000000000, "B": 10000000}
	err := l.DeductForPayout(ctx, "t", map[string]string{"A": "5.00000000", "B": "1.00000000"}, 1)
	if err == nil {
		t.Fatal("B 余额不足应整体失败")
	}
	// A 不应被扣（事务性）
	if l.balances["A"] != 1000000000 {
		t.Fatalf("失败后 A 余额被误扣: %d", l.balances["A"])
	}
}

func TestMemBalanceChangeSemanticsMatchPG(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(8, 2)
	_ = l.RecordShare(ctx, mkShare("A", time.Unix(1, 0)), 1)
	b := mkBlock("h1", "A", "10.00000000", 1, false)
	_ = l.RecordBlock(ctx, b, "raw")
	if err := l.ConfirmBlock(ctx, b, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.DeductForPayout(ctx, "t", map[string]string{"A": "4.00000000"}, 7); err != nil {
		t.Fatal(err)
	}
	if err := l.RefundPayout(ctx, "t", map[string]string{"A": "4.00000000"}, 7); err != nil {
		t.Fatal(err)
	}
	if err := l.OrphanBlock(ctx, b); err != nil {
		t.Fatal(err)
	}

	direct := mkBlock("d1", "A", "10.00000000", 1, false)
	direct.Direct = []core.DirectCredit{{Address: "A", Credit: "6.00000000", Paid: "4.00000000"}}
	if err := l.RecordBlock(ctx, direct, "raw"); err != nil {
		t.Fatal(err)
	}
	if err := l.ConfirmBlock(ctx, direct, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.OrphanBlock(ctx, direct); err != nil {
		t.Fatal(err)
	}

	want := []memBalanceChange{
		{addr: "A", delta: 1_000_000_000, usage: "reward", tag: "block:h1"},
		{addr: "A", delta: -400_000_000, usage: "payment", tag: "batch:7"},
		{addr: "A", delta: 400_000_000, usage: "payment_refund", tag: "batch:7"},
		{addr: "A", delta: -1_000_000_000, usage: "orphan_reversal", tag: "block:h1"},
		{addr: "A", delta: 600_000_000, usage: "reward", tag: "block:d1"},
		{addr: "A", delta: -400_000_000, usage: "payment", tag: "block:d1"},
		{addr: "A", delta: 400_000_000, usage: "payment_refund", tag: "block:d1"},
		{addr: "A", delta: -600_000_000, usage: "orphan_reversal", tag: "block:d1"},
	}
	if len(l.changes) != len(want) {
		t.Fatalf("流水条数=%d want=%d: %+v", len(l.changes), len(want), l.changes)
	}
	for i := range want {
		if l.changes[i] != want[i] {
			t.Fatalf("流水[%d]=%+v want=%+v", i, l.changes[i], want[i])
		}
	}
	if delta, err := l.Reconcile(ctx, "t"); err != nil || delta != "0.00000000" {
		t.Fatalf("完整流水重放后 J4 应成立: delta=%s err=%v", delta, err)
	}
}

func TestMemReconcileJ4ReturnsAbsDiffWhenConservationIsZero(t *testing.T) {
	l := NewMemLedger(8, 2)
	// 人工制造“守恒式为 0、但余额没有流水”的白盒状态，钉死 J4 的独立冻结路径。
	l.balances["A"] = 1
	l.totalFees = -1
	delta, err := l.Reconcile(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if delta != "0.00000001" {
		t.Fatalf("J4 应返回绝对差 1 聪，got=%s", delta)
	}
}
