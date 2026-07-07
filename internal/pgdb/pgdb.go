// Package pgdb Postgres 连接与迁移（M4 多实例的共享持久层入口）。
//
// 驱动 = pgx/v5 的 database/sql 适配（stdlib）：代码只依赖 database/sql 语义，
// 池内所有 SQL 走参数绑定，绝无字符串拼接。
//
// 迁移策略：schema.sql 全语句幂等（IF NOT EXISTS + ADD COLUMN IF NOT EXISTS），
// 每次启动执行一遍即为「迁移」；破坏性变更（改列型）另立 ALTER 段并保持幂等。
package pgdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/scashcc/ntmpool/db"
)

// Open 建连接池并 ping（fail-fast：DSN 错在启动时暴露，不留到打款时）。
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	h, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 Postgres: %w", err)
	}
	h.SetMaxOpenConns(16)
	h.SetMaxIdleConns(4)
	h.SetConnMaxLifetime(time.Hour)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.PingContext(pingCtx); err != nil {
		h.Close()
		return nil, fmt.Errorf("连接 Postgres: %w", err)
	}
	return h, nil
}

// Migrate 应用内嵌 schema（幂等，可重复执行）。
func Migrate(ctx context.Context, h *sql.DB) error {
	if _, err := h.ExecContext(ctx, db.Schema); err != nil {
		return fmt.Errorf("应用 schema: %w", err)
	}
	return nil
}
