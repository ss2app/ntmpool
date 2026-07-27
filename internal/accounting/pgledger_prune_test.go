package accounting

// PruneShares 三道闸验证（NTMPOOL_PG_DSN 门控）。
//
// 这个函数在 2026-07-27 接线之前从未被任何地方调用过，补测试的理由：它删的是 PPLNS
// 窗口的输入数据。删过头 → 窗口被截断 → 矿工在下一个块少拿钱，且事后无从追溯（share
// 已经没了）。所以每道闸都要有断言守着，尤其是 keepRows——它是算力暴跌、窗口时长超过
// 任何固定天数时唯一还在兜底的那道。

import (
	"context"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// pruneSeed 灌 n 条指定时间戳的 share 并落盘（created 取 share.At，见 insertShares）。
func pruneSeed(t *testing.T, pg *PGLedger, coin string, n int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := pg.RecordShare(ctx, core.Share{
			Coin: coin, Address: "A", At: at, RemoteIP: "127.0.0.1",
		}, 1); err != nil {
			t.Fatalf("RecordShare: %v", err)
		}
	}
	if err := pg.FlushShares(ctx); err != nil {
		t.Fatalf("FlushShares: %v", err)
	}
}

func pruneCount(t *testing.T, pg *PGLedger, coin string) int64 {
	t.Helper()
	var n int64
	if err := pg.h.QueryRowContext(context.Background(),
		`SELECT count(*) FROM shares WHERE poolid = $1`, coin).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// keepRows 必须压过时间闸：share 全都老得该删，但条数不够 keepRows ⇒ 一行都不许删。
// 这正是 btc09 算力低谷（PPLNS 窗口跨 7.3 小时、再跌一个数量级就超过 retain）时的形状。
func TestPruneSharesKeepRowsOverridesAge(t *testing.T) {
	l, coin := pgFactory(t)
	pg := l.(*PGLedger)
	old := time.Now().Add(-10 * 24 * time.Hour) // 远早于 retain
	pruneSeed(t, pg, coin, 50, old)

	n, err := pg.PruneShares(context.Background(), 7*24*time.Hour, 1000, 100)
	if err != nil {
		t.Fatalf("PruneShares: %v", err)
	}
	if n != 0 {
		t.Fatalf("keepRows=1000 > 表内 50 行，应一行不删，实删 %d", n)
	}
	if got := pruneCount(t, pg, coin); got != 50 {
		t.Fatalf("剩余行数 = %d，want 50", got)
	}
}

// 时间闸必须压过 keepRows：share 全是新的，keepRows 再小也不许删。
func TestPruneSharesAgeOverridesKeepRows(t *testing.T) {
	l, coin := pgFactory(t)
	pg := l.(*PGLedger)
	pruneSeed(t, pg, coin, 50, time.Now())

	n, err := pg.PruneShares(context.Background(), 7*24*time.Hour, 1, 100)
	if err != nil {
		t.Fatalf("PruneShares: %v", err)
	}
	if n != 0 {
		t.Fatalf("share 全在 retain 内，应一行不删，实删 %d", n)
	}
	if got := pruneCount(t, pg, coin); got != 50 {
		t.Fatalf("剩余行数 = %d，want 50", got)
	}
}

// 两闸同时满足才删，且删完恰好剩 keepRows 条。
// 50 老 + 50 新（id 1..100），keepRows=60 ⇒ 边界 = 从新往老数第 61 条 = id 40，
// 删 created<cutoff 且 id<=40 ⇒ 40 条，剩 60。
func TestPruneSharesDeletesOnlyBeyondBothGates(t *testing.T) {
	l, coin := pgFactory(t)
	pg := l.(*PGLedger)
	old := time.Now().Add(-10 * 24 * time.Hour)
	pruneSeed(t, pg, coin, 50, old)
	pruneSeed(t, pg, coin, 50, time.Now())

	n, err := pg.PruneShares(context.Background(), 7*24*time.Hour, 60, 100)
	if err != nil {
		t.Fatalf("PruneShares: %v", err)
	}
	if n != 40 {
		t.Fatalf("应删 40 行（老的里超出 keepRows 的那部分），实删 %d", n)
	}
	if got := pruneCount(t, pg, coin); got != 60 {
		t.Fatalf("剩余行数 = %d，want 60（== keepRows）", got)
	}
	// 剩下的必须全是「新的那 50 条」+「老的最后 10 条」，即一条新 share 都没被碰。
	var fresh int64
	if err := pg.h.QueryRowContext(context.Background(),
		`SELECT count(*) FROM shares WHERE poolid = $1 AND created > $2`,
		coin, time.Now().Add(-24*time.Hour)).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh != 50 {
		t.Fatalf("retain 内的 share 被删了：剩 %d，want 50", fresh)
	}
}

// 分批不改变最终结果：batch 远小于待删量时，循环必须删干净。
func TestPruneSharesBatchingReachesSameEnd(t *testing.T) {
	l, coin := pgFactory(t)
	pg := l.(*PGLedger)
	old := time.Now().Add(-10 * 24 * time.Hour)
	pruneSeed(t, pg, coin, 100, old)

	n, err := pg.PruneShares(context.Background(), 7*24*time.Hour, 10, 7) // batch=7 ⇒ 多轮
	if err != nil {
		t.Fatalf("PruneShares: %v", err)
	}
	if n != 90 {
		t.Fatalf("应删 90 行，实删 %d", n)
	}
	if got := pruneCount(t, pg, coin); got != 10 {
		t.Fatalf("剩余行数 = %d，want 10", got)
	}
}

// 非正参数一律拒绝，绝不退化成「无闸门全删」。
func TestPruneSharesRejectsNonPositiveArgs(t *testing.T) {
	l, coin := pgFactory(t)
	pg := l.(*PGLedger)
	pruneSeed(t, pg, coin, 10, time.Now().Add(-10*24*time.Hour))
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		retain time.Duration
		keep   int64
		batch  int
	}{
		{"retain=0", 0, 100, 100},
		{"retain<0", -time.Hour, 100, 100},
		{"keepRows=0", time.Hour, 0, 100},
		{"keepRows<0", time.Hour, -1, 100},
		{"batch=0", time.Hour, 100, 0},
		{"batch<0", time.Hour, 100, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pg.PruneShares(ctx, tc.retain, tc.keep, tc.batch); err == nil {
				t.Fatal("应报错，实际 nil")
			}
		})
	}
	if got := pruneCount(t, pg, coin); got != 10 {
		t.Fatalf("参数校验失败的调用不该删任何行，剩 %d，want 10", got)
	}
}

// 空表不报错、不误删。
func TestPruneSharesEmptyTable(t *testing.T) {
	l, _ := pgFactory(t)
	pg := l.(*PGLedger)
	n, err := pg.PruneShares(context.Background(), time.Hour, 100, 100)
	if err != nil {
		t.Fatalf("PruneShares: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表应删 0 行，实删 %d", n)
	}
}
