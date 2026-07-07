package payout

// PGBatchStore 集成测试（NTMPOOL_PG_DSN 门控；CI postgres service / 服务器冒烟）。
// 验证批次跨「重启」（新开 store 实例）持久化 + Unfinished 恢复扫描能读到。

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/pgdb"
)

func pgStore(t *testing.T) (*PGBatchStore, string) {
	dsn := os.Getenv("NTMPOOL_PG_DSN")
	if dsn == "" {
		t.Skip("NTMPOOL_PG_DSN 未设置，跳过 PGBatchStore 集成测试")
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
	coin := fmt.Sprintf("pgbs%d", time.Now().UnixNano())
	return NewPGBatchStore(h, coin), coin
}

func TestPGBatchStorePersistAndRecover(t *testing.T) {
	s, _ := pgStore(t)

	id, err := s.NextBatchID()
	if err != nil {
		t.Fatal(err)
	}
	b := &Batch{
		ID: id, Kind: "payout",
		Outputs: map[string]string{"addrA": "1.50000000", "addrB": "2.25000000"},
		Status:  core.PaymentCreated, CreatedAt: time.Now(),
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	// 推进到 sent（拿到 txid）
	b.PlannedTxID, b.TxID, b.Status = "txid-1", "txid-1", core.PaymentSent
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	// 「重启」：新 store 实例读同一库（模拟崩溃恢复）
	s2 := NewPGBatchStore(s.h, s.coin)
	got, ok, err := s2.Load(id)
	if err != nil || !ok {
		t.Fatalf("重启后 Load: ok=%v err=%v", ok, err)
	}
	if got.Status != core.PaymentSent || got.TxID != "txid-1" {
		t.Fatalf("批次状态未持久化: %+v", got)
	}
	if got.Outputs["addrA"] != "1.50000000" || got.Outputs["addrB"] != "2.25000000" {
		t.Fatalf("输出未持久化: %+v", got.Outputs)
	}

	// 未完成扫描能读到 sent 批次
	unf, err := s2.Unfinished()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range unf {
		if u.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("Unfinished 未包含 sent 批次（恢复扫描会漏）")
	}

	// 完结后不再出现在 Unfinished
	b.Status = core.PaymentConfirmed
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	unf, _ = s2.Unfinished()
	for _, u := range unf {
		if u.ID == id {
			t.Fatal("confirmed 批次不应在 Unfinished")
		}
	}

	// All 新→旧且含本批次
	all, err := s2.All()
	if err != nil || len(all) == 0 || all[0].ID != id {
		t.Fatalf("All 错误: err=%v n=%d", err, len(all))
	}
}
