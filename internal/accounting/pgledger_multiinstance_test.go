package accounting

// 多实例共享 Postgres 的无状态化验证（NTMPOOL_PG_DSN 门控）：
// 实例 A 记 share（stratum 无状态：share 落共享库），实例 B 确认块并按全库 share
// 窗口分账——证明会计层不依赖单进程内存，多台服务器可组成一个逻辑池（M4 R14.2）。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

func TestPGMultiInstanceShareThenConfirm(t *testing.T) {
	l, coin := pgFactory(t) // 复用 conformance 的 PG 工厂（无 DSN 自动跳过）
	pgA := l.(*PGLedger)
	ctx := context.Background()

	// 实例 B 连同一库、同一 coin，但 source 标签不同
	pgB := NewPGLedger(pgA.h, coin, pgA.decimals, pgA.pplnsN, "poolB")

	now := time.Now()
	// 实例 A 收 3 份 A 地址；实例 B 收 1 份 B 地址（矿工可能连不同服务器）
	for i := 0; i < 3; i++ {
		if err := pgA.RecordShare(ctx, core.Share{Coin: coin, Address: "A", At: now}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := pgB.RecordShare(ctx, core.Share{Coin: coin, Address: "B", At: now}, 1); err != nil {
		t.Fatal(err)
	}
	// 各实例的 share 缓冲各自落盘（生产中由每实例 1s 定时 flusher 完成，远早于
	// 块成熟的多块延迟；这里显式 flush 模拟「时间已过、周期落盘已跑」）。
	if err := pgA.FlushShares(ctx); err != nil {
		t.Fatal(err)
	}

	// 实例 B 爆块并确认（读的是全库 share 窗口，A/B 两实例的 share 都算）
	blk := core.FoundBlock{
		Coin: coin, Height: 200, Hash: fmt.Sprintf("blk-%d", time.Now().UnixNano()),
		Finder: "B", Reward: "40.00000000", NetDiff: 10.0, Status: core.BlockPending, FoundAt: now,
	}
	if err := pgB.RecordBlock(ctx, blk, "raw"); err != nil {
		t.Fatal(err)
	}
	if err := pgB.ConfirmBlock(ctx, blk, 0); err != nil {
		t.Fatal(err)
	}

	// 实例 A 读快照也应看到同样的分账（共享库）：A=3/4×40=30, B=1/4×40=10
	snap, err := pgA.Snapshot(ctx, coin)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Balances["A"] != "30.00000000" || snap.Balances["B"] != "10.00000000" {
		t.Fatalf("跨实例分账错误: A=%s B=%s", snap.Balances["A"], snap.Balances["B"])
	}
	delta, err := pgA.Reconcile(ctx, coin)
	if err != nil {
		t.Fatal(err)
	}
	if delta != "0.00000000" {
		t.Fatalf("跨实例守恒破坏 delta=%s", delta)
	}

	// share 的 source 标签区分实例（溯源）
	var srcA, srcB int
	if err := pgA.h.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM shares WHERE poolid=$1 AND source='test1'),
		        (SELECT COUNT(*) FROM shares WHERE poolid=$1 AND source='poolB')`,
		coin).Scan(&srcA, &srcB); err != nil {
		t.Fatal(err)
	}
	if srcA != 3 || srcB != 1 {
		t.Fatalf("share 实例溯源错误: test1=%d poolB=%d", srcA, srcB)
	}
}
