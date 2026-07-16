//go:build quark

package quark

import (
	"encoding/hex"
	"testing"
)

// TestQuarkSelfTest 跑金锚：主网块 3600 header80 → HashQuark 字节对拍。
func TestQuarkSelfTest(t *testing.T) {
	if err := (quarkHasher{}).SelfTest(); err != nil {
		t.Fatal(err)
	}
}

// TestQuarkDeterministic 同一 input 两次一致 + 输出 32 字节。
func TestQuarkDeterministic(t *testing.T) {
	h := quarkHasher{}
	in, _ := hex.DecodeString(golden3600Header)
	a, err := h.Hash(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 {
		t.Fatalf("输出长度 %d ≠ 32", len(a))
	}
	b, _ := h.Hash(in)
	if hex.EncodeToString(a) != hex.EncodeToString(b) {
		t.Fatal("非确定性")
	}
}
