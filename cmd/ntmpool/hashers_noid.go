//go:build noid

package main

// ParanO(1)d (NOID) Poseidon2b 哈希器（cgo 链官方 Rust staticlib libnoidpow.a）。
// 只在 -tags noid 构建进来：init() 注册 poseidon2b/noid + 跑内嵌金锚自检（不过则 panic，
// main.go 的 SelfTestAll 门禁另有一道）。构建见 internal/hasher/noidp2b 包注释。
import (
	_ "github.com/scashcc/ntmpool/internal/hasher/noidp2b"
)
