//go:build randomx

package brisviarx

import (
	"encoding/hex"
	"testing"
)

// TestSelfTest 启动门禁：官方引擎向量 + Brisvia 真链块 #3000 锚。
func TestSelfTest(t *testing.T) {
	if err := New().SelfTest(); err != nil {
		t.Fatal(err)
	}
}

// TestName 算法名 = 矿工 login / 配置 algo 字段。
func TestName(t *testing.T) {
	if got := New().Name(); got != "rx/brva" {
		t.Fatalf("Name() = %q, want rx/brva", got)
	}
}

// TestRealBlockPoWMeetsTarget 真链块 #3000：rx_hash 视作 256-bit LE 整数 reverse 后
// 必须 ≤ compact(0x1e523a3f) 展开的 target —— 与节点 CheckProofOfWork 同判据。
func TestRealBlockPoWMeetsTarget(t *testing.T) {
	key, _ := hex.DecodeString(anchorSeedKeyHex)
	hdr, _ := hex.DecodeString(anchorHeader80Hex)
	rx, err := New().HashKeyed(key, hdr)
	if err != nil {
		t.Fatal(err)
	}
	// LE → 大端整数
	be := make([]byte, 32)
	for i := 0; i < 32; i++ {
		be[i] = rx[31-i]
	}
	// compact 0x1e523a3f 展开为 32B 大端 target
	tgt := compactToBE32(0x1e523a3f)
	if bytesCompare(be, tgt) > 0 {
		t.Fatalf("块#3000 rx_hash(int)=%x > target=%x（不该：链上是有效块）", be, tgt)
	}
	t.Logf("块#3000 rx_hash(int)=%x <= target=%x ✓", be, tgt)
}

func compactToBE32(bits uint32) []byte {
	out := make([]byte, 32)
	mant := bits & 0x007fffff
	exp := int(bits >> 24)
	for i := 0; i < 3; i++ {
		byteval := byte((mant >> uint(8*(2-i))) & 0xff)
		pos := exp - 1 - i
		if pos >= 0 && pos < 32 {
			out[31-pos] = byteval
		}
	}
	return out
}

func bytesCompare(a, b []byte) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
