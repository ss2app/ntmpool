//go:build quark

// Package quark Noctari (NCTI) 池端重算哈希器（cgo 链节点同源 sphlib）。
//
// 同源铁律（hasher.go 铁律 1）：blake/bmw/groestl/jh/keccak/skein.c + sph_*.h
// 直接取自 noctari 节点 src/crypto/（同一份 sphlib）；quark.c 逐行复刻节点
// src/hash.h 的 HashQuark 9 轮序列。禁止按 spec 自行重写再对拍。
//
// Noctari PoW（node src/primitives/block.cpp CBlockHeader::GetHash）：
//
//	version<4 的块（公平启动窗口 1..20159，ComputeBlockVersion=3）:
//	    blockHash = HashQuark(header80)   ← 本 hasher 复算的就是它
//	version>=20160 起转纯 PoS（SHA256d），锄头/矿池随窗口关闭退场。
//
// SelfTest 金锚 = 主网真块 3600（version 3, Quark PoW）header80 → 块 hash 字节反转。
//
// 构建：go test/build -tags quark（cgo，仅 5850U/Linux；Windows 中文路径+mingw
// cgo 静默失败，铁律①，本地开发不带该 tag）。
package quark

/*
#cgo CFLAGS: -O3 -I${SRCDIR}
#include "quark.h"
*/
import "C"

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"unsafe"

	"github.com/scashcc/ntmpool/internal/hasher"
)

type quarkHasher struct{}

func init() { hasher.Register(quarkHasher{}) }

func (quarkHasher) Name() string { return "quark" }

// Hash 对 block header 做 Quark 共识哈希，返回 32 字节 LE（与节点
// CBlockHeader::GetHash()=HashQuark 逐字节一致；反转即块 hash 显示值）。
func (quarkHasher) Hash(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("quark: 空 input")
	}
	out := make([]byte, 32)
	C.quark_hash(unsafe.Pointer(&input[0]), C.size_t(len(input)), unsafe.Pointer(&out[0]))
	return out, nil
}

// SelfTest 金锚：Noctari 主网块 3600（version 3, Quark PoW）。
// header80 经 HashQuark 应 == 块 hash（getblockhash 为 BE 显示）的字节反转。
func (h quarkHasher) SelfTest() error {
	header, err := hex.DecodeString(golden3600Header)
	if err != nil {
		return err
	}
	if len(header) != 80 {
		return fmt.Errorf("quark: 金锚 header 长度 %d ≠ 80", len(header))
	}
	blockHashBE, err := hex.DecodeString(golden3600HashBE)
	if err != nil {
		return err
	}
	want := reverseBytes(blockHashBE) // BE 显示 → quark LE 输出
	got, err := h.Hash(header)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("quark 金锚失配: got=%x want=%x", got, want)
	}
	return nil
}

const (
	golden3600Header = "030000009b23348eaa624e80d2e2eece9cca2131fffde38bf3b3734a53cb034803000000b3e10d4655924e0683dfa59bebe2234078b31db16e2dec52243fd99324283aae3a1d546ad3140a1df3300900"
	golden3600HashBE = "00000001cefcaaf8400b544a1195706136a4ccfbcba6a4a4516ab7e57cb4752e"
)

func reverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return r
}
