//go:build randomx

package main

// RandomX 家族哈希器（cgo 链 vendored libRandomX）。
// 只在 -tags randomx 构建进来：生产 linux 二进制带、本地 Windows 开发不带
// （中文路径 + mingw cgo 静默失败，铁律①；rx 币的本地开发用 CI 兜）。
import (
	_ "github.com/scashcc/ntmpool/internal/hasher/dragonxrx"
	_ "github.com/scashcc/ntmpool/internal/hasher/randomx"
)
