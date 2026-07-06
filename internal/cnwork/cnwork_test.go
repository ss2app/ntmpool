package cnwork

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// 难度 1 的 compact target = ffffffff（(2^256-1)/1 高 32 位全 f，LE hex 仍全 f）。
func TestCompactTargetDiff1(t *testing.T) {
	if got := EncodeTargetCompactLE(1); got != "ffffffff" {
		t.Fatalf("diff=1 compact target = %s, want ffffffff", got)
	}
}

// XMRig 解码往返：compact 4 字节 LE → uint32，diff ≈ 2^32/uint32 值。
// 校验编码值与难度互逆（±1 精度内）。
func TestCompactTargetRoundTrip(t *testing.T) {
	for _, diff := range []float64{1, 2, 100, 5000, 100000, 4000000} {
		s := EncodeTargetCompactLE(diff)
		b, _ := hex.DecodeString(s)
		raw := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24
		// top32 = floor((2^256-1)/diff) >> 224 ≈ floor((2^32-1)/diff)
		want := uint64(0xFFFFFFFF) / uint64(diff)
		if raw != want && raw != want-1 && raw != want+1 {
			t.Fatalf("diff=%v: compact=%s raw=%d want≈%d", diff, s, raw, want)
		}
	}
}

// zoka 口径：difficulty_bits=b → target = 2^(256-b)-1，64-hex 大端。
func TestHex256BETargetBits(t *testing.T) {
	bits := uint(8)
	target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256-bits), big.NewInt(1))
	got := EncodeTargetHex256BE(target)
	want := "00" + strings.Repeat("f", 62)
	if got != want {
		t.Fatalf("bits=8 target = %s, want %s", got, want)
	}
}

func TestHashValueEndianness(t *testing.T) {
	hash := make([]byte, 32)
	hash[0] = 0x01 // 大端解释 = 0x01<<248；小端解释 = 0x01
	be := HashValue(hash, true)
	le := HashValue(hash, false)
	if be.Cmp(new(big.Int).Lsh(big.NewInt(1), 248)) != 0 {
		t.Fatalf("BE 解释错误: %s", be.Text(16))
	}
	if le.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("LE 解释错误: %s", le.Text(16))
	}
}

func TestShareDiffAndMeetsTarget(t *testing.T) {
	// 大端 hash 前导 8 个零 bit → diff ≈ 2^8
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = 0xff
	}
	hash[0] = 0x00
	d := ShareDiff(hash, true)
	if d < 255.9 || d > 256.1 {
		t.Fatalf("ShareDiff = %v, want ≈256", d)
	}
	target8 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 248), big.NewInt(1))
	if !MeetsTarget(hash, target8, true) {
		t.Fatal("应命中 8-bit 目标")
	}
	target9 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 247), big.NewInt(1))
	if MeetsTarget(hash, target9, true) {
		t.Fatal("不应命中 9-bit 目标")
	}
}

func TestParseNonceLE(t *testing.T) {
	// 8 字节 LE：0102030405060708 → 0x0807060504030201
	n, ln, err := ParseNonceLE("0102030405060708")
	if err != nil || ln != 8 || n != 0x0807060504030201 {
		t.Fatalf("ParseNonceLE = %x/%d/%v", n, ln, err)
	}
	// 4 字节（monero 系 submit）
	n, ln, err = ParseNonceLE("2a000000")
	if err != nil || ln != 4 || n != 0x2a {
		t.Fatalf("ParseNonceLE 4B = %x/%d/%v", n, ln, err)
	}
	for _, bad := range []string{"", "123", "zz", "010203040506070809"} {
		if _, _, err := ParseNonceLE(bad); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}

func TestPutAndReadNonce(t *testing.T) {
	blob := make([]byte, 16)
	PutNonceLE(blob, 8, 8, 0x1122334455667788)
	if got := NonceFieldLE(blob, 8, 8); got != 0x1122334455667788 {
		t.Fatalf("nonce 往返失败: %x", got)
	}
	if blob[8] != 0x88 || blob[15] != 0x11 {
		t.Fatalf("LE 布局错误: % x", blob[8:])
	}
}
