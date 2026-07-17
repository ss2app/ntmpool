package accounting

// Ledger conformance 套件：同一组断言跑 MemLedger 与 PGLedger，
// 保证两实现金额数学分毫不差（M4 迁移 Postgres 的等价性铁证）。
// PG 侧由 NTMPOOL_PG_DSN 门控（CI postgres service / 服务器冒烟），无 DSN 自动跳过。

import (
	"context"
	"errors"
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
	l := NewMemLedger(8, 2)
	coin := fmt.Sprintf("mem%d", poolSeq.Add(1))
	// 每个 conformance 子场景退出时统一验证现有守恒/J4 与 journal J1/J2/J3。
	t.Cleanup(func() { assertConformanceTail(t, context.Background(), l, coin) })
	return l, coin
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
	l := NewPGLedger(h, coin, 8, 2, "test1")
	// 后注册以保证 LIFO 顺序下先校验账本、再关闭测试数据库句柄。
	t.Cleanup(func() { assertConformanceTail(t, context.Background(), l, coin) })
	return l, coin
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

func assertConformanceTail(t *testing.T, ctx context.Context, l Ledger, coin string) {
	t.Helper()
	assertDelta0(t, ctx, l, coin, "场景收尾 J4")
	mismatches, checked, err := l.JournalShadowAudit(ctx, coin)
	if err != nil || !checked {
		t.Fatalf("场景收尾 J3 未完成: checked=%t err=%v", checked, err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("场景收尾 J3 失配: %+v", mismatches)
	}

	switch ledger := l.(type) {
	case *MemLedger:
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		seen := map[string]struct{}{}
		for _, tx := range ledger.journal {
			var sum int64
			for _, leg := range tx.legs {
				sum += leg.delta
			}
			if sum != 0 {
				t.Errorf("场景收尾 J1 不平: key=%s sum=%d", tx.key, sum)
			}
			if _, duplicate := seen[tx.key]; duplicate {
				t.Errorf("场景收尾 J2 business_key 重复: %s", tx.key)
			}
			seen[tx.key] = struct{}{}
		}
	case *PGLedger:
		var unbalanced, duplicates int
		if err := ledger.h.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM (
			  SELECT jt.id FROM journal_tx jt
			  LEFT JOIN journal_entry je ON je.txref=jt.id
			  WHERE jt.poolid=$1 GROUP BY jt.id HAVING COALESCE(SUM(je.amount),0)<>0
			) bad`, coin).Scan(&unbalanced); err != nil {
			t.Fatalf("场景收尾 J1 查询: %v", err)
		}
		if unbalanced != 0 {
			t.Errorf("场景收尾 J1 不平笔数=%d", unbalanced)
		}
		if err := ledger.h.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM (
			  SELECT business_key FROM journal_tx WHERE poolid=$1
			  GROUP BY business_key HAVING COUNT(*)>1
			) dup`, coin).Scan(&duplicates); err != nil {
			t.Fatalf("场景收尾 J2 查询: %v", err)
		}
		if duplicates != 0 {
			t.Errorf("场景收尾 J2 重复键数=%d", duplicates)
		}
	default:
		t.Fatalf("未覆盖的 Ledger 实现 %T", l)
	}
}

func journalBusinessKeyCount(t *testing.T, ctx context.Context, l Ledger, coin, key string) int {
	t.Helper()
	switch ledger := l.(type) {
	case *MemLedger:
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		count := 0
		for _, tx := range ledger.journal {
			if tx.key == key {
				count++
			}
		}
		return count
	case *PGLedger:
		var count int
		if err := ledger.h.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_tx WHERE poolid=$1 AND business_key=$2`, coin, key).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	default:
		t.Fatalf("未覆盖的 Ledger 实现 %T", l)
		return 0
	}
}

func balanceChangeCount(t *testing.T, ctx context.Context, l Ledger, coin string) int {
	t.Helper()
	switch ledger := l.(type) {
	case *MemLedger:
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		return len(ledger.changes)
	case *PGLedger:
		var count int
		if err := ledger.h.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM balance_changes WHERE poolid=$1`, coin).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	default:
		t.Fatalf("未覆盖的 Ledger 实现 %T", l)
		return 0
	}
}

func TestJournalBuilderRejectsUnbalanced(t *testing.T) {
	j := newJournalTx("test", "test:unbalanced", journalPolicyPreRegistry, "")
	j.leg("block:revenue", "", 1)
	if err := j.validate(); !errors.Is(err, ErrJournalUnbalanced) {
		t.Fatalf("不平 journal 应返回哨兵错误，得到 %v", err)
	}
}

func runLedgerConformance(t *testing.T, mk mkLedger) {
	ctx := context.Background()

	t.Run("绕过流水改余额必被J4抓住", func(t *testing.T) {
		l, coin := mk(t)
		creditViaBlock(t, ctx, l, coin, "A", "10.00000000", "j4", 1)
		assertDelta0(t, ctx, l, coin, "篡改前")

		switch ledger := l.(type) {
		case *MemLedger:
			ledger.mu.Lock()
			ledger.balances["A"]++
			ledger.mu.Unlock()
		case *PGLedger:
			if _, err := ledger.h.ExecContext(ctx,
				`UPDATE balances SET amount=amount+1 WHERE poolid=$1 AND address='A'`, coin); err != nil {
				t.Fatalf("绕过流水修改 PG 余额: %v", err)
			}
		default:
			t.Fatalf("未覆盖的 Ledger 实现 %T", l)
		}

		if delta, err := l.Reconcile(ctx, coin); err != nil {
			t.Fatalf("J4 日志路径不应 panic/报错: %v", err)
		} else if delta == "0.00000000" {
			t.Fatal("绕过流水修改余额后 J4 必须返回非零 delta")
		}

		switch ledger := l.(type) {
		case *MemLedger:
			ledger.mu.Lock()
			ledger.balances["A"]--
			ledger.mu.Unlock()
		case *PGLedger:
			if _, err := ledger.h.ExecContext(ctx,
				`UPDATE balances SET amount=amount-1 WHERE poolid=$1 AND address='A'`, coin); err != nil {
				t.Fatalf("修复 PG 余额: %v", err)
			}
		}
		assertDelta0(t, ctx, l, coin, "修复后")
	})

	t.Run("reward回填幂等与状态约束", func(t *testing.T) {
		l, coin := mk(t)
		b := confBlock(coin, "reward-fill", "A", "999.00000000", 99, 1, false)
		b.RewardPending = true
		if err := l.RecordBlock(ctx, b, "raw"); err != nil {
			t.Fatal(err)
		}
		if err := l.ConfirmBlock(ctx, b, 0); err == nil {
			t.Fatal("权威 reward 未回填前不得 confirm")
		}
		for i := 0; i < 2; i++ {
			if err := l.UpdateBlockReward(ctx, coin, b.Hash, "12.50000000"); err != nil {
				t.Fatalf("第 %d 次幂等回填: %v", i+1, err)
			}
		}
		pending, err := l.PendingBlocks(ctx, coin)
		if err != nil || len(pending) != 1 || pending[0].Reward != "12.50000000" {
			t.Fatalf("回填值未写入 pending block: err=%v blocks=%+v", err, pending)
		}
		b.Reward = "12.50000000"
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		if err := l.UpdateBlockReward(ctx, coin, b.Hash, "12.50000000"); err == nil {
			t.Fatal("confirmed 块不得回填 reward")
		}
		if err := l.UpdateBlockReward(ctx, coin, "missing", "1.00000000"); err == nil {
			t.Fatal("不存在的块不得回填 reward")
		}
	})

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

	t.Run("SOLO份额不进PPLNS窗口", func(t *testing.T) {
		// 混跑场景：同一币同时开 pplns 与 solo 端口。solo 矿工 C 的 share
		// 若混进 PPLNS 窗口，会稀释 PPLNS 矿工分账且 C 白拿分成（自己 solo 爆块却独吞）。
		l, coin := mk(t)
		now := time.Now()
		for i := 0; i < 5; i++ {
			s := confShare(coin, "C", now)
			s.Solo = true
			_ = l.RecordShare(ctx, s, 1000) // C 大权重 solo share
		}
		_ = l.RecordShare(ctx, confShare(coin, "A", now), 1) // A 在 pplns 端口 1 份
		// pplns 块：窗口远大于全部权重，若 solo 混入则 C 分走 ~99.98%
		b := confBlock(coin, "h1", "A", "50.00000000", 100, 1e6, false)
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "50.00000000" {
			t.Fatalf("PPLNS 块应全归 A（solo share 不得进窗口）: %+v", snap.Balances)
		}
		if v, ok := snap.Balances["C"]; ok && v != "0.00000000" {
			t.Fatalf("solo 矿工 C 不应分到 PPLNS 块: %s", v)
		}
		assertDelta0(t, ctx, l, coin, "solo/pplns 混跑")

		// 反向：C 的 solo 爆块仍全归 C，A 不受影响
		b2 := confBlock(coin, "h2", "C", "50.00000000", 101, 1e6, true)
		_ = l.RecordBlock(ctx, b2, "raw")
		if err := l.ConfirmBlock(ctx, b2, 0); err != nil {
			t.Fatal(err)
		}
		snap2, _ := l.Snapshot(ctx, coin)
		if snap2.Balances["C"] != "50.00000000" || snap2.Balances["A"] != "50.00000000" {
			t.Fatalf("solo 爆块应全归 C 且不动 A: %+v", snap2.Balances)
		}
		assertDelta0(t, ctx, l, coin, "混跑双向")
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

	t.Run("孤块回滚journal配平", func(t *testing.T) {
		// 同一场景同时覆盖 fee 与余额不足转 debt：R=50、F=5，先付 40，孤块后 take=5/remain=40。
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "journal-orphan", "A", "50.00000000", 110, 1, false)
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 10); err != nil {
			t.Fatal(err)
		}
		if err := l.DeductForPayout(ctx, coin, map[string]string{"A": "40.00000000"}, 110); err != nil {
			t.Fatal(err)
		}
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		mismatches, checked, err := l.JournalShadowAudit(ctx, coin)
		if err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("带费且带 debt 的孤块回滚后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
	})

	t.Run("重复business_key被拒", func(t *testing.T) {
		l, coin := mk(t)
		b := confBlock(coin, "journal-idempotent", "A", "5.00000000", 111, 1, true)
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		if count := journalBusinessKeyCount(t, ctx, l, coin, "confirm:block:"+b.Hash); count != 1 {
			t.Fatalf("重复 confirm 只能有一笔 journal，得到 %d", count)
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

	t.Run("空奖励块可落库不丢块", func(t *testing.T) {
		// 回归：zoka 等 custom-http 链模板无 reward → FoundBlock.Reward=""。
		// 曾因空串塞 numeric 列触发 22P02，RecordBlock 报错 → blockSink 提前 return
		// → 块永不 submit = 真矿工白挖（2026-07-07 zoka 13 个真块被丢）。
		// 空奖励必须能落库（归一为 0），块绝不能丢。
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "hEmpty", "A", "", 200, 1.0, false) // reward=""
		if err := l.RecordBlock(ctx, b, "raw"); err != nil {
			t.Fatalf("空奖励块落库失败（白挖回归）: %v", err)
		}
		pend, err := l.PendingBlocks(ctx, coin)
		if err != nil || len(pend) != 1 || pend[0].Hash != "hEmpty" {
			t.Fatalf("空奖励块应作为待确认块存在: %v %+v", err, pend)
		}
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatalf("空奖励块确认失败: %v", err)
		}
		assertDelta0(t, ctx, l, coin, "空奖励块确认后")
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

	// ---- 直付块（midstate coinbase 直付，docs/07 §6）----

	t.Run("直付块入账与守恒", func(t *testing.T) {
		// E=100，credit A=60 B=35（费=5）；A 实付 60，B 低于阈值实付 0（结转）。
		l, coin := mk(t)
		b := confBlock(coin, "d1", "A", "100.00000000", 300, 1.0, false)
		b.Direct = []core.DirectCredit{
			{Address: "A", Credit: "60.00000000", Paid: "60.00000000"},
			{Address: "B", Credit: "35.00000000", Paid: "0.00000000"},
		}
		if err := l.RecordBlock(ctx, b, "raw"); err != nil {
			t.Fatal(err)
		}
		// feePercent 传 99 证明被忽略（直付块费率在模板分账时已定死）
		if err := l.ConfirmBlock(ctx, b, 99); err != nil {
			t.Fatal(err)
		}
		snap, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Balances["A"] != "0.00000000" && snap.Balances["A"] != "" {
			t.Fatalf("A 已随块直付，余额应为 0: %s", snap.Balances["A"])
		}
		if snap.Balances["B"] != "35.00000000" {
			t.Fatalf("B 尘埃应结转: %s", snap.Balances["B"])
		}
		if snap.TotalPaid != "60.00000000" {
			t.Fatalf("已付应计 A 的直付: %s", snap.TotalPaid)
		}
		if snap.TotalFees != "5.00000000" {
			t.Fatalf("费应=E−Σcredit: %s", snap.TotalFees)
		}
		assertDelta0(t, ctx, l, coin, "直付块确认后")
		// 幂等
		if err := l.ConfirmBlock(ctx, b, 99); err != nil {
			t.Fatal(err)
		}
		snap2, _ := l.Snapshot(ctx, coin)
		if snap2.TotalPaid != "60.00000000" || snap2.Balances["B"] != "35.00000000" {
			t.Fatalf("重复 confirm 双份入账: paid=%s B=%s", snap2.TotalPaid, snap2.Balances["B"])
		}
		ms, ok, err := l.MinerSummary(ctx, coin, "A")
		if err != nil || !ok {
			t.Fatalf("A 应有会计记录: %v", err)
		}
		if ms.TotalPaid != "60.00000000" {
			t.Fatalf("A 矿工自查已付: %s", ms.TotalPaid)
		}
	})

	t.Run("直付块carry兑付与在飞预留", func(t *testing.T) {
		// 第一块给 B 结转 35；第二块 credit B=10 且把 carry 一并实付（paid=45）。
		l, coin := mk(t)
		b1 := confBlock(coin, "d1", "A", "100.00000000", 300, 1.0, false)
		b1.Direct = []core.DirectCredit{
			{Address: "B", Credit: "35.00000000", Paid: "0.00000000"},
		}
		_ = l.RecordBlock(ctx, b1, "raw")
		if err := l.ConfirmBlock(ctx, b1, 0); err != nil {
			t.Fatal(err)
		}
		// 计划输入：B 的 carry 应可见
		_, carry, err := l.DirectPlanInputs(ctx, coin, 1)
		if err != nil {
			t.Fatal(err)
		}
		if carry["B"] != "35.00000000" {
			t.Fatalf("B carry 应为 35: %v", carry)
		}
		// 第二块（pending，paid>credit=carry 兑付在飞）
		b2 := confBlock(coin, "d2", "B", "100.00000000", 301, 1.0, false)
		b2.Direct = []core.DirectCredit{
			{Address: "B", Credit: "10.00000000", Paid: "45.00000000"},
		}
		_ = l.RecordBlock(ctx, b2, "raw")
		// 在飞预留：B 的 carry 不得再次可用（防双付）
		_, carry2, err := l.DirectPlanInputs(ctx, coin, 1)
		if err != nil {
			t.Fatal(err)
		}
		if carry2["B"] != "" {
			t.Fatalf("在飞预留应扣光 B 的 carry: %v", carry2)
		}
		if err := l.ConfirmBlock(ctx, b2, 0); err != nil {
			t.Fatal(err)
		}
		snap, _ := l.Snapshot(ctx, coin)
		if snap.Balances["B"] != "0.00000000" && snap.Balances["B"] != "" {
			t.Fatalf("carry 兑付后 B 余额应为 0: %s", snap.Balances["B"])
		}
		if snap.TotalPaid != "45.00000000" {
			t.Fatalf("已付: %s", snap.TotalPaid)
		}
		assertDelta0(t, ctx, l, coin, "carry 兑付后")
	})

	t.Run("直付块孤块回滚", func(t *testing.T) {
		// 确认后孤块：coinbase 没上链=谁都没拿到，paid 退回、credit 反转、费出账。
		l, coin := mk(t)
		b := confBlock(coin, "d1", "A", "100.00000000", 300, 1.0, false)
		b.Direct = []core.DirectCredit{
			{Address: "A", Credit: "60.00000000", Paid: "60.00000000"},
			{Address: "B", Credit: "35.00000000", Paid: "0.00000000"},
		}
		_ = l.RecordBlock(ctx, b, "raw")
		_ = l.ConfirmBlock(ctx, b, 0)
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		snap, _ := l.Snapshot(ctx, coin)
		for _, a := range []string{"A", "B"} {
			if v := snap.Balances[a]; v != "" && v != "0.00000000" {
				t.Fatalf("孤块回滚后 %s 余额应清零: %s", a, v)
			}
		}
		if snap.TotalPaid != "0.00000000" {
			t.Fatalf("孤块回滚后已付应清零: %s", snap.TotalPaid)
		}
		if snap.TotalFees != "0.00000000" {
			t.Fatalf("孤块回滚后费应出账: %s", snap.TotalFees)
		}
		if snap.DebtsNet != "0.00000000" {
			t.Fatalf("直付孤块不应产生 debts（coinbase 没上链）: %s", snap.DebtsNet)
		}
		assertDelta0(t, ctx, l, coin, "直付孤块回滚后")
		if mismatches, checked, err := l.JournalShadowAudit(ctx, coin); err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("直付确认+孤块回滚后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
		// 未确认直付块孤块 = 无账务动作
		b2 := confBlock(coin, "d2", "A", "100.00000000", 301, 1.0, false)
		b2.Direct = []core.DirectCredit{{Address: "A", Credit: "60.00000000", Paid: "60.00000000"}}
		_ = l.RecordBlock(ctx, b2, "raw")
		if err := l.OrphanBlock(ctx, b2); err != nil {
			t.Fatal(err)
		}
		snap2, _ := l.Snapshot(ctx, coin)
		if snap2.TotalPaid != "0.00000000" {
			t.Fatalf("未确认直付孤块不应有账务: %s", snap2.TotalPaid)
		}
		assertDelta0(t, ctx, l, coin, "未确认直付孤块后")
	})

	t.Run("直付空分账块全额归费", func(t *testing.T) {
		// 窗口为空时 Direct=[]（非 nil）：绝不能走「全给爆块者」兜底——
		// coinbase 全额进了费地址，账上必须同样全记费（账实一致）。
		l, coin := mk(t)
		b := confBlock(coin, "d1", "A", "100.00000000", 300, 1.0, false)
		b.Direct = []core.DirectCredit{}
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		snap, _ := l.Snapshot(ctx, coin)
		if snap.TotalFees != "100.00000000" {
			t.Fatalf("空分账直付块应全额归费: %s", snap.TotalFees)
		}
		if v := snap.Balances["A"]; v != "" && v != "0.00000000" {
			t.Fatalf("爆块者不应被虚记余额: %s", v)
		}
		assertDelta0(t, ctx, l, coin, "空分账直付块后")
	})

	t.Run("坏账核销后J3J4与守恒仍为零", func(t *testing.T) {
		l, coin := mk(t)
		_ = l.RecordShare(ctx, confShare(coin, "A", time.Now()), 1)
		b := confBlock(coin, "writeoff-source", "A", "50.00000000", 112, 1, false)
		_ = l.RecordBlock(ctx, b, "raw")
		if err := l.ConfirmBlock(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		if err := l.DeductForPayout(ctx, coin, map[string]string{"A": "40.00000000"}, 112); err != nil {
			t.Fatal(err)
		}
		if err := l.OrphanBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
		changesBefore := balanceChangeCount(t, ctx, l, coin)
		if err := l.WriteOffDebt(WithConfigAuditID(ctx, "writeoff-audit-1"), coin,
			"A", "15.00000000", "确认无法追回"); err != nil {
			t.Fatal(err)
		}
		snap, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		if snap.DebtsNet != "25.00000000" || snap.Balances["A"] != "0.00000000" {
			t.Fatalf("部分核销投影错误: debts=%s balance=%s", snap.DebtsNet, snap.Balances["A"])
		}
		if count := journalBusinessKeyCount(t, ctx, l, coin, "debtwriteoff:A:writeoff-audit-1"); count != 1 {
			t.Fatalf("核销 journal 数量=%d", count)
		}
		if changesAfter := balanceChangeCount(t, ctx, l, coin); changesAfter != changesBefore {
			t.Fatalf("坏账核销不得写 balance_changes: before=%d after=%d", changesBefore, changesAfter)
		}
		mismatches, checked, err := l.JournalShadowAudit(ctx, coin)
		if err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("核销后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
		assertDelta0(t, ctx, l, coin, "部分坏账核销后")
		if err := l.WriteOffDebt(WithConfigAuditID(ctx, "writeoff-audit-2"), coin,
			"A", "26.00000000", "超额应拒"); err == nil {
			t.Fatal("超过 remaining 的核销必须拒绝")
		}
		snap, _ = l.Snapshot(ctx, coin)
		if snap.DebtsNet != "25.00000000" {
			t.Fatalf("超额核销不得部分执行: %s", snap.DebtsNet)
		}
		if pg, ok := l.(*PGLedger); ok {
			var writtenOff string
			if err := pg.h.QueryRowContext(ctx, `
				SELECT written_off::text FROM reconciliations
				WHERE poolid=$1 ORDER BY id DESC LIMIT 1`, coin).Scan(&writtenOff); err != nil {
				t.Fatal(err)
			}
			if got, err := pg.parse(writtenOff); err != nil || got != 15*100_000_000 {
				t.Fatalf("reconciliations written_off=%s err=%v", writtenOff, err)
			}
		}
	})

	t.Run("人工调账保持J3J4且禁止负余额", func(t *testing.T) {
		l, coin := mk(t)
		if err := l.ManualAdjust(WithConfigAuditID(ctx, "manual-credit-1"), coin,
			"A", "10.00000000", true, "人工补发"); err != nil {
			t.Fatal(err)
		}
		if err := l.ManualAdjust(WithConfigAuditID(ctx, "manual-debit-1"), coin,
			"A", "3.00000000", false, "人工冲回"); err != nil {
			t.Fatal(err)
		}
		snap, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Balances["A"] != "7.00000000" {
			t.Fatalf("人工调账余额=%s", snap.Balances["A"])
		}
		switch ledger := l.(type) {
		case *MemLedger:
			ledger.mu.Lock()
			if len(ledger.changes) != 2 || ledger.changes[0].usage != "manual_credit" ||
				ledger.changes[0].tag != "admin:manual-credit-1" || ledger.changes[0].delta != 10*100_000_000 ||
				ledger.changes[1].usage != "manual_debit" || ledger.changes[1].tag != "admin:manual-debit-1" ||
				ledger.changes[1].delta != -3*100_000_000 {
				t.Errorf("Mem 人工流水语义错误: %+v", ledger.changes)
			}
			ledger.mu.Unlock()
		case *PGLedger:
			var count int
			if err := ledger.h.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM balance_changes WHERE poolid=$1 AND address='A' AND
				 ((usage='manual_credit' AND amount=10 AND tags @> ARRAY['admin:manual-credit-1']) OR
				  (usage='manual_debit' AND amount=-3 AND tags @> ARRAY['admin:manual-debit-1']))`, coin).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("PG 人工流水语义匹配数=%d", count)
			}
		}
		if err := l.ManualAdjust(WithConfigAuditID(ctx, "manual-debit-2"), coin,
			"A", "8.00000000", false, "超额扣减"); err == nil {
			t.Fatal("人工 debit 超余额必须拒绝")
		}
		snap, _ = l.Snapshot(ctx, coin)
		if snap.Balances["A"] != "7.00000000" {
			t.Fatalf("失败 debit 不得改余额: %s", snap.Balances["A"])
		}
		assertDelta0(t, ctx, l, coin, "人工 credit/debit 后")
		mismatches, checked, err := l.JournalShadowAudit(ctx, coin)
		if err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("人工调账后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
		if pg, ok := l.(*PGLedger); ok {
			var manual string
			if err := pg.h.QueryRowContext(ctx, `
				SELECT manual_adjustment::text FROM reconciliations
				WHERE poolid=$1 ORDER BY id DESC LIMIT 1`, coin).Scan(&manual); err != nil {
				t.Fatal(err)
			}
			if got, err := pg.parse(manual); err != nil || got != 7*100_000_000 {
				t.Fatalf("reconciliations manual_adjustment=%s err=%v", manual, err)
			}
		}
	})

	t.Run("事故登记了结不触碰投影", func(t *testing.T) {
		l, coin := mk(t)
		before, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		changesBefore := balanceChangeCount(t, ctx, l, coin)
		if err := l.RecordIncident(WithConfigAuditID(ctx, "incident-record-audit"), coin,
			"incident-1", "wrong_address", "recipient-X", "10.00000000", "打错地址"); err != nil {
			t.Fatal(err)
		}
		if err := l.RecordIncident(ctx, coin, "incident-1", "wrong_address",
			"recipient-X", "10.00000000", "重复"); err == nil {
			t.Fatal("重复 incident id 必须拒绝")
		}
		if err := l.ResolveIncident(WithConfigAuditID(ctx, "incident-recover-audit"), coin,
			"incident-1", "recovered", "4.00000000", "追回一部分"); err != nil {
			t.Fatal(err)
		}
		if err := l.ResolveIncident(WithConfigAuditID(ctx, "incident-over-audit"), coin,
			"incident-1", "writeoff", "7.00000000", "超额了结"); err == nil {
			t.Fatal("超过未了结余额必须拒绝")
		}
		if err := l.ResolveIncident(WithConfigAuditID(ctx, "incident-writeoff-audit"), coin,
			"incident-1", "writeoff", "6.00000000", "剩余认赔"); err != nil {
			t.Fatal(err)
		}
		after, err := l.Snapshot(ctx, coin)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Balances) != len(before.Balances) || after.DebtsNet != before.DebtsNet ||
			after.TotalPaid != before.TotalPaid || after.TotalFees != before.TotalFees {
			t.Fatalf("事故 journal 不得改投影: before=%+v after=%+v", before, after)
		}
		if changesAfter := balanceChangeCount(t, ctx, l, coin); changesAfter != changesBefore {
			t.Fatalf("事故登记/了结不得写 balance_changes: before=%d after=%d", changesBefore, changesAfter)
		}
		assertDelta0(t, ctx, l, coin, "事故登记与了结后")
		mismatches, checked, err := l.JournalShadowAudit(ctx, coin)
		if err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("事故操作后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
	})

	t.Run("opening幂等", func(t *testing.T) {
		l, coin := mk(t)
		pg, ok := l.(*PGLedger)
		if !ok {
			t.Skip("Mem 账本从空账开始，无 opening")
		}
		for i := 0; i < 2; i++ {
			if err := pg.EnsureJournalOpening(ctx); err != nil {
				t.Fatalf("第 %d 次 opening: %v", i+1, err)
			}
		}
		var count int
		if err := pg.h.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_tx WHERE poolid=$1 AND kind='opening'`, coin).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("opening 应只有一笔，得到 %d", count)
		}
	})

	t.Run("带存量数据起账", func(t *testing.T) {
		l, coin := mk(t)
		pg, ok := l.(*PGLedger)
		if !ok {
			t.Skip("仅 PG 有持久存量起账")
		}
		// 模拟升级前的投影与历史流水：余额 12、债务 2、已确认收入 16/费 3、已付 3。
		seed, err := pg.h.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		seedStatements := []string{
			`INSERT INTO balances(poolid,address,amount) VALUES($1,'A',12)`,
			`INSERT INTO balance_changes(poolid,address,amount,usage,tags) VALUES
			 ($1,'A',15,'reward',ARRAY['legacy']),($1,'A',-3,'payment',ARRAY['legacy'])`,
			`INSERT INTO debts(poolid,address,original,remaining,reason) VALUES($1,'A',2,2,'legacy')`,
			`INSERT INTO blocks(poolid,blockheight,networkdifficulty,status,transactionconfirmationdata,reward,feeamount)
			 VALUES($1,900,1,'confirmed','legacy-confirmed',16,3)`,
		}
		for _, statement := range seedStatements {
			if _, err := seed.ExecContext(ctx, statement, coin); err != nil {
				seed.Rollback()
				t.Fatalf("构造存量投影: %v", err)
			}
		}
		if err := seed.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := pg.EnsureJournalOpening(ctx); err != nil {
			t.Fatal(err)
		}
		mismatches, checked, err := pg.JournalShadowAudit(ctx, coin)
		if err != nil || !checked || len(mismatches) != 0 {
			t.Fatalf("存量起账后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatches)
		}
	})
}
