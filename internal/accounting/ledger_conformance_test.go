package accounting

// Ledger conformance 套件：同一组断言跑 MemLedger 与 PGLedger，
// 保证两实现金额数学分毫不差（M4 迁移 Postgres 的等价性铁证）。
// PG 侧由 NTMPOOL_PG_DSN 门控（CI postgres service / 服务器冒烟），无 DSN 自动跳过。

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/pgdb"
)

var poolSeq atomic.Int64

// mkLedger 工厂：每次调用给一个全新（空账本）的 Ledger，coin 名唯一。
type mkLedger func(t *testing.T) (Ledger, string)

func memFactory(t *testing.T) (Ledger, string) {
	return NewMemLedger(8, 2), fmt.Sprintf("mem%d", poolSeq.Add(1))
}

func pgFactory(t *testing.T) (Ledger, string) {
	dsn := os.Getenv("NTMPOOL_PG_DSN")
	if dsn == "" {
		t.Skip("NTMPOOL_PG_DSN 未设置，跳过 PG conformance")
	}
	ctx := context.Background()
	h, err := pgdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("连接 Postgres: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	if err := pgdb.Migrate(ctx, h); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	coin := fmt.Sprintf("pg%d-%d", time.Now().UnixNano(), poolSeq.Add(1))
	return NewPGLedger(h, coin, 8, 2, "test1"), coin
}

func TestLedgerConformanceMem(t *testing.T) { runLedgerConformance(t, memFactory) }
func TestLedgerConformancePG(t *testing.T)  { runLedgerConformance(t, pgFactory) }

func confShare(coin, addr string, at time.Time) core.Share {
	return core.Share{Coin: coin, Address: addr, At: at, RemoteIP: "127.0.0.1"}
}

func confBlock(coin, hash, finder, reward string, height uint64, netDiff float64, solo bool) core.FoundBlock {
	return core.FoundBlock{
		Coin: coin, Height: height, Hash: hash, Finder: finder,
		Reward: reward, NetDiff: netDiff, Status: core.BlockPending, Solo: solo,
		FoundAt: time.Now(),
	}
}

// creditViaBlock 用一个确认块给地址堆出精确余额（conformance 不碰实现内部字段）。
func creditViaBlock(t *testing.T, ctx context.Context, l Ledger, coin, addr, reward, hash string, height uint64) {
	t.Helper()
	if err := l.RecordShare(ctx, confShare(coin, addr, time.Now()), 1); err != nil {
		t.Fatal(err)
	}
	b := confBlock(coin, hash, addr, reward, height, 0.5, false) // 窗口权重 1，只框住最后 1 份
	if err := l.RecordBlock(ctx, b, "raw"); err != nil {
		t.Fatal(err)
	}
	if err := l.ConfirmBlock(ctx, b, 0); err != nil {
		t.Fatal(err)
	}
}

func assertDelta0(t *testing.T, ctx context.Context, l Ledger, coin, when string) {
	t.Helper()
	delta, err := l.Reconcile(ctx, coin)
	if err != nil {
		t.Fatalf("%s: reconcile: %v", when, err)
	}
	if delta != "0.00000000" {
		t.Fatalf("%s: 守恒破坏 delta=%s", when, delta)
	}
}

func runLedgerConformance(t *testing.T, mk mkLedger) {
	ctx := context.Background()

	t.Run("PPLNS分账守恒与按份额分", func(t *testing.T) {
		l, coin := mk(t)
		now := time.Now()
		for i := 0; i < 3; i++ {
			_ = l.RecordShare(ctx, confShare(coin, "A", now), 1)
		}
		_ = l.RecordShare(ctx, confShare(coin, "B", now), 1)
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 10.0, false) // 窗口 20 框住全部
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 10.0); err != nil {
			t.Fatal(err)
		}
		snap, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Balances["A"] != "33.75000000" || snap.Balances["B"] != "11.25000000" {
			t.Fatalf("分账错误: A=%s B=%s", snap.Balances["A"], snap.Balances["B"])
		}
		if snap.TotalFees != "5.00000000" {
			t.Fatalf("费错误: %s", snap.TotalFees)
		}
		assertDelta0(t, ctx, l, coin, "confirm 后")
		// 幂等：重复 confirm 不得双份入账
		if err := l.ConfirmBlock(ctx, b, 10.0); err != nil {
			t.Fatal(err)
		}
		snap2, _ := l.Snapshot(ctx, coin)
		if snap2.Balances["A"] != "33.75000000" {
			t.Fatalf("重复 confirm 双份入账: %s", snap2.Balances["A"])
		}
	})

	t.Run("窗口不满付满100%", func(t *testing.T) {
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 1e6, false)
		_ = l.RecordBlock(ctx, b, "raw")
		_ = l.ConfirmBlock(ctx, b, 0)
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "50.00000000" {
			t.Fatalf("窗口不满也应付满: %s", snap.Balances["A"])
		}
		assertDelta0(t, ctx, l, coin, "冷启动窗口")
	})

	t.Run("SOLO全归爆块者", func(t *testing.T) {
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "h1", "B", "50.00000000", 100, 1.0, true)
		_ = l.RecordBlock(ctx, b, "raw")
		_ = l.ConfirmBlock(ctx, b, 0)
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["B"] != "50.00000000" {
			t.Fatalf("solo 应全归 B: %+v", snap.Balances)
		}
		if v, ok := snap.Balances["A"]; ok && v != "0.00000000" {
			t.Fatalf("A 不应有余额: %s", v)
		}
	})

	t.Run("孤块追缴闭环", func(t *testing.T) {
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 1.0, false)
		_ = l.RecordBlock(ctx, b, "raw")
		_ = l.ConfirmBlock(ctx, b, 0)

		if err := l.DeductForPayout(ctx, coin, map[string]string{"A": "40.00000000"}, 1); err != nil {
			t.Fatal(err)
		}
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "10.00000000" {
			t.Fatalf("打款后余额: %s", snap.Balances["A"])
		}
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		snap, _ = l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "0.00000000" {
			t.Fatalf("孤块后余额应清零: %s", snap.Balances["A"])
		}
		if snap.DebtsNet != "40.00000000" {
			t.Fatalf("应记 40 债务: %s", snap.DebtsNet)
		}
		// 幂等：重复 orphan 不得重复记债
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		snap, _ = l.Snapshot(ctx, coin)
		if snap.DebtsNet != "40.00000000" {
			t.Fatalf("重复 orphan 改变债务: %s", snap.DebtsNet)
		}
		// 后续确认块先抵债
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b2 := confBlock(coin, "h2", "A", "30.00000000", 101, 1.0, false)
		_ = l.RecordBlock(ctx, b2, "raw")
		_ = l.ConfirmBlock(ctx, b2, 0)
		snap, _ = l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "0.00000000" || snap.DebtsNet != "10.00000000" {
			t.Fatalf("债务抵扣错误: 余额=%s 债务=%s", snap.Balances["A"], snap.DebtsNet)
		}
	})

	t.Run("带费孤块守恒", func(t *testing.T) {
		// M4 修的真 bug：confirm(fee>0) 后 orphan，计提费必须一并作废，否则 delta=-fee 误冻结
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 1.0, false)
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 10.0); err != nil {
			t.Fatal(err)
		}
		assertDelta0(t, ctx, l, coin, "带费 confirm 后")
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		assertDelta0(t, ctx, l, coin, "带费 orphan 后")
		snap, _ := l.Snapshot(ctx, coin)
		if snap.TotalFees != "0.00000000" {
			t.Fatalf("孤块后计提费应作废: %s", snap.TotalFees)
		}
	})

	t.Run("起付额与矿工自设覆盖", func(t *testing.T) {
		l, coin := mk(t)
		creditViaBlock(t, ctx, l, coin, "A", "5.00000000", "hA", 100)
		creditViaBlock(t, ctx, l, coin, "B", "0.50000000", "hB", 101)
		creditViaBlock(t, ctx, l, coin, "C", "25.00000000", "hC", 102)
		pay, err := l.PayableBalances(ctx, coin, 1.0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := pay["B"]; ok {
			t.Fatal("B 不应达门槛")
		}
		if pay["A"] != "5.00000000" || pay["C"] != "25.00000000" {
			t.Fatalf("可打款集合错误: %+v", pay)
		}
		pay, _ = l.PayableBalances(ctx, coin, 1.0, map[string]float64{"C": 30})
		if _, ok := pay["C"]; ok {
			t.Fatal("C 自设 30 门槛后不应可打")
		}
	})

	t.Run("扣款事务性与退款", func(t *testing.T) {
		l, coin := mk(t)
		creditViaBlock(t, ctx, l, coin, "A", "10.00000000", "hA", 100)
		creditViaBlock(t, ctx, l, coin, "B", "0.10000000", "hB", 101)
		err := l.DeductForPayout(ctx, coin, map[string]string{"A": "5.00000000", "B": "1.00000000"}, 1)
		if err == nil {
			t.Fatal("B 余额不足应整体失败")
		}
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "10.00000000" {
			t.Fatalf("失败后 A 被误扣: %s", snap.Balances["A"])
		}
		// 成功扣款 → 退款闭环
		if err := l.DeductForPayout(ctx, coin, map[string]string{"A": "5.00000000"}, 2); err != nil {
			t.Fatal(err)
		}
		assertDelta0(t, ctx, l, coin, "扣款后")
		if err := l.RefundPayout(ctx, coin, map[string]string{"A": "5.00000000"}, 2); err != nil {
			t.Fatal(err)
		}
		snap, _ = l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "10.00000000" || snap.TotalPaid != "0.00000000" {
			t.Fatalf("退款后状态错误: 余额=%s 已付=%s", snap.Balances["A"], snap.TotalPaid)
		}
		assertDelta0(t, ctx, l, coin, "退款后")
	})

	t.Run("矿工摘要与块分页", func(t *testing.T) {
		l, coin := mk(t)
		creditViaBlock(t, ctx, l, coin, "A", "10.00000000", "hA", 100)
		if err := l.DeductForPayout(ctx, coin, map[string]string{"A": "4.00000000"}, 1); err != nil {
			t.Fatal(err)
		}
		ms, ok, err := l.MinerSummary(ctx, coin, "A")
		if err != nil || !ok {
			t.Fatalf("摘要应存在: ok=%v err=%v", ok, err)
		}
		if ms.Balance != "6.00000000" || ms.TotalPaid != "4.00000000" || ms.Debt != "0.00000000" {
			t.Fatalf("摘要错误: %+v", ms)
		}
		if _, ok, _ := l.MinerSummary(ctx, coin, "无此人"); ok {
			t.Fatal("未知地址应 ok=false")
		}
		// 分页：3 块新→旧
		creditViaBlock(t, ctx, l, coin, "A", "1.00000000", "hA2", 101)
		creditViaBlock(t, ctx, l, coin, "A", "1.00000000", "hA3", 102)
		bs, total, err := l.Blocks(ctx, coin, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 || len(bs) != 2 || bs[0].Hash != "hA3" || bs[1].Hash != "hA2" {
			t.Fatalf("分页错误: total=%d %+v", total, bs)
		}
		bs, _, _ = l.Blocks(ctx, coin, 2, 2)
		if len(bs) != 1 || bs[0].Hash != "hA" {
			t.Fatalf("第二页错误: %+v", bs)
		}
	})

	t.Run("PendingBlocks与状态机", func(t *testing.T) {
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 1.0, false)
		_ = l.RecordBlock(ctx, b, "raw")
		pend, err := l.PendingBlocks(ctx, coin)
		if err != nil || len(pend) != 1 || pend[0].Hash != "h1" {
			t.Fatalf("record 后应有 1 个待确认块: %v %+v", err, pend)
		}
		if err := l.MarkBlockPending(ctx, coin, "h1"); err != nil {
			t.Fatal(err)
		}
		_ = l.ConfirmBlock(ctx, b, 0)
		pend, _ = l.PendingBlocks(ctx, coin)
		if len(pend) != 0 {
			t.Fatalf("confirm 后不应有待确认块: %+v", pend)
		}
	})
}
