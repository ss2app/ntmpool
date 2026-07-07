// Package db 携带 NTMPool 的 Postgres schema（go:embed，二进制自带建表能力）。
// schema.sql 全部语句幂等（IF NOT EXISTS），Migrate 可对已有库重复执行。
package db

import _ "embed"

//go:embed schema.sql
var Schema string
