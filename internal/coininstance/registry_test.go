package coininstance

// InstanceRegistry 集成测试（NTMPOOL_PG_DSN 门控）：验证心跳 upsert 的 text[]
// 写入、判活 make_interval 比较、以及 ListInstances 的 text[] 扫描全部正确
//（这些 Postgres-ism 最易写错，必须对真库跑）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/pgdb"
)

func TestInstanceRegistryHeartbeatAndList(t *testing.T) {
	dsn := os.Getenv("NTMPOOL_PG_DSN")
	if dsn == "" {
		t.Skip("NTMPOOL_PG_DSN 未设置，跳过实例注册表集成测试")
	}
	ctx := context.Background()
	db, err := pgdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("连接 Postgres: %v", err)
	}
	defer db.Close()
	if err := pgdb.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	// 独立 id，避免与其它测试/真实例串味
	id := "regtest-" + time.Now().Format("150405.000000")
	defer db.ExecContext(ctx, `DELETE FROM instances WHERE id=$1`, id)

	reg := NewInstanceRegistry(db, id, "vtest")
	reg.SetCoins([]string{"zoka", "btc09"})
	reg.beat(ctx) // 直接打一次心跳（不起后台协程）

	list, err := ListInstances(ctx, db)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	var got *InstanceInfo
	for i := range list {
		if list[i].ID == id {
			got = &list[i]
		}
	}
	if got == nil {
		t.Fatal("心跳后未在列表中找到本实例")
	}
	if !got.Alive {
		t.Fatal("刚心跳的实例应判活")
	}
	if got.Version != "vtest" {
		t.Fatalf("version 未持久化: %q", got.Version)
	}
	if len(got.Coins) != 2 || got.Coins[0] != "zoka" || got.Coins[1] != "btc09" {
		t.Fatalf("text[] 币列表往返错误: %+v", got.Coins)
	}

	// 更新币列表再心跳 → upsert 覆盖
	reg.SetCoins([]string{"zoka"})
	reg.beat(ctx)
	list, _ = ListInstances(ctx, db)
	for i := range list {
		if list[i].ID == id && (len(list[i].Coins) != 1 || list[i].Coins[0] != "zoka") {
			t.Fatalf("upsert 未更新币列表: %+v", list[i].Coins)
		}
	}

	// 判活边界：手动把 lastseen 拨到 StaleAfter 之前 → 应判死
	if _, err := db.ExecContext(ctx,
		`UPDATE instances SET lastseen = now() - make_interval(secs => $2) WHERE id=$1`,
		id, StaleAfter.Seconds()+30); err != nil {
		t.Fatal(err)
	}
	list, _ = ListInstances(ctx, db)
	for i := range list {
		if list[i].ID == id && list[i].Alive {
			t.Fatal("超过 StaleAfter 的实例应判死")
		}
	}
}
