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
	"strings"
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

// migrateLockKey 迁移专用 advisory lock 键（"ntmpool" 的 ASCII 常量，全库唯一即可）。
const migrateLockKey = int64(0x6e746d706f6f6c)

// Migrate 应用内嵌 schema（幂等，可重复执行）。防 deadlock 双保险：
//
//  1. advisory lock 串行化 Migrate-对-Migrate（多实例同时启动/并行测试包同库迁移）；
//  2. schema **拆语句逐条执行**——整份 schema 一把 Exec 是一个隐式大事务，会逐表累积
//     ACCESS EXCLUSIVE 锁，与并行会话的普通 DML 事务锁序冲突 → deadlock detected
//     (40P01) 随机崩掉一方；逐条执行则每条语句只短锁单表、语句尾即释放，单语句的锁
//     获取不持有其它锁，不可能参与死锁环。schema 幂等，中途失败重跑即补全。
//
// 锁必须在同一会话上加/解，故先从池里钉住一条连接。
func Migrate(ctx context.Context, h *sql.DB) error {
	conn, err := h.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取迁移连接: %w", err)
	}
	defer conn.Close() // 会话关闭本身也会释放 advisory lock（显式 unlock 的兜底）
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("获取迁移锁: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey)
	// 拆分依据：schema.sql 是我们自控文件，ASCII 分号只出现在语句尾（无 DO $$ 块、
	// 字符串字面量与注释内无分号）——改 schema 时保持这个约定。
	for _, stmt := range strings.Split(db.Schema, ";") {
		if strings.TrimSpace(stripSQLComments(stmt)) == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("应用 schema 语句 %q…: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// stripSQLComments 去掉 `--` 行注释，用于判断片段是否只剩注释/空白（不用于执行）。
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(stripSQLComments(s))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
