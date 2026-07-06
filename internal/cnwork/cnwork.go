// Package cnwork 是 blob 系链（CryptoNote/RandomX 家族 + zoka 类自定义链）的
// 目标/难度/nonce 数学。docs/04 §2 的实现依据：
//   - target 两种下发编码：8-hex compact 小端（XMRig 惯例）/ 64-hex 大端全量（zoka/CRB 惯例）
//   - hash 两种解释字节序：小端（monero）/ 大端（zoka leading-zero-bits）
//   - share 难度 = Diff1 / hashValue（Diff1 = 2^256−1，行业口径，miningcore 同款）
package cnwork

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Diff1 = 2^256 − 1（share 难度换算基准）。
var Diff1 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// TargetFromDiff 难度 → 256-bit 目标（floor(Diff1/diff)）。diff ≤ 0 视为 1。
func TargetFromDiff(diff float64) *big.Int {
	d := new(big.Int)
	big.NewFloat(diff).Int(d)
	if d.Sign() <= 0 {
		d = big.NewInt(1)
	}
	return new(big.Int).Div(Diff1, d)
}

// EncodeTargetCompactLE 难度 → 8-hex compact 小端 target（XMRig login/job 用）。
// 取 256-bit 目标最高 4 字节，按小端字节序 hex 编码（node-cryptonote-pool/miningcore 同款）。
func EncodeTargetCompactLE(diff float64) string {
	t := TargetFromDiff(diff)
	top := new(big.Int).Rsh(t, 224).Uint64() // 高 32 位
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(top))
	return hex.EncodeToString(b[:])
}

// EncodeTargetHex256BE 目标 → 64-hex 大端（zoka/CRB 惯例，NTMminer 认这个）。
func EncodeTargetHex256BE(t *big.Int) string {
	b := t.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	var padded [32]byte
	copy(padded[32-len(b):], b)
	return hex.EncodeToString(padded[:])
}

// EncodeTargetForDiff 按编码开关生成下发矿工的 target。
func EncodeTargetForDiff(diff float64, compactLE bool) string {
	if compactLE {
		return EncodeTargetCompactLE(diff)
	}
	return EncodeTargetHex256BE(TargetFromDiff(diff))
}

// HashValue 把 32 字节 hash 按指定字节序解释为 256-bit 整数。
// bigEndian=false（monero 惯例）时反转字节序后再解释。
func HashValue(hash []byte, bigEndian bool) *big.Int {
	b := make([]byte, len(hash))
	if bigEndian {
		copy(b, hash)
	} else {
		for i, v := range hash {
			b[len(hash)-1-i] = v
		}
	}
	v := new(big.Int).SetBytes(b)
	if v.Sign() == 0 {
		v = big.NewInt(1) // 防除零（全零 hash 只在测试假哈希器出现）
	}
	return v
}

// ShareDiff share 实际达到的难度（float，供 vardiff Judge 与计权）。
func ShareDiff(hash []byte, bigEndian bool) float64 {
	q := new(big.Float).Quo(
		new(big.Float).SetInt(Diff1),
		new(big.Float).SetInt(HashValue(hash, bigEndian)),
	)
	f, _ := q.Float64()
	return f
}

// MeetsTarget hash 是否命中 256-bit 目标（≤ 即命中）。
func MeetsTarget(hash []byte, target *big.Int, bigEndian bool) bool {
	return HashValue(hash, bigEndian).Cmp(target) <= 0
}

// ParseNonceLE 解析矿工提交的 nonce：hex（小端字节序，1..8 字节）。
// 兼容 0x 前缀。返回 (值, 字节数)。（CRB/zoka 先例：rx 系锄头交全字段 LE hex。）
func ParseNonceLE(s string) (uint64, int, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
	if s == "" || len(s)%2 != 0 || len(s) > 16 {
		return 0, 0, fmt.Errorf("非法 nonce %q", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return 0, 0, fmt.Errorf("非法 nonce %q", s)
	}
	var n uint64
	for i, v := range b {
		n |= uint64(v) << (8 * i)
	}
	return n, len(b), nil
}

// PutNonceLE 把 nonce 字段（LE，nonceLen 字节）写进 blob。
func PutNonceLE(blob []byte, offset, nonceLen int, nonce uint64) {
	for i := 0; i < nonceLen; i++ {
		blob[offset+i] = byte(nonce >> (8 * i))
	}
}

// NonceFieldLE 从 blob 读出 nonce 字段（LE，nonceLen 字节）。
func NonceFieldLE(blob []byte, offset, nonceLen int) uint64 {
	var n uint64
	for i := 0; i < nonceLen; i++ {
		n |= uint64(blob[offset+i]) << (8 * i)
	}
	return n
}
