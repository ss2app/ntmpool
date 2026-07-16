//go:build quark

package main

// Quark 家族哈希器（cgo 链节点同源 sphlib：blake/bmw/groestl/jh/keccak/skein）。
// 只在 -tags quark 构建进来：生产 linux 二进制带，本地 Windows 开发不带
// （中文路径 + mingw cgo 静默失败，铁律①；Noctari 池生产构建须带 -tags quark）。
import _ "github.com/scashcc/ntmpool/internal/hasher/quark"
