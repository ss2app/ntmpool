//go:build randomx

package randomx

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// rx/juno 主网真链块锚（2026-08-06 自家 154 节点 getblock 实取，块 #3000）：
//   seed = 块 2048 hash（display→反转成内部序；epoch 2048/lag 96 → seedHeight(3000)=2048）
//   header140 = version|prev|merkle|blockcommitments|time|bits|nonce（display 字段全反转、数值 LE）
//   期望 rx_hash = 链上 solution 字段【原样】（= reverse(块 hash)，juno 单段 PoW 的内部序输出）
// 此锚同时钉死三件事：stock 引擎参数、140B 头喂法、seed 不反转直用（GBT randomxseedhash
// 的内部序 == 这里手工 reverse(display) 的结果）。
const (
	junoAnchorSeedDisplay   = "000155d8e801e21f379199700de57ff10b532001af025a3456796627fb5d5bce" // block 2048 hash
	junoAnchorPrevDisplay   = "0000cc468976384ddff7b538b87a497bf5c63962cc5cc9ddec3d5a2289d66f4f"
	junoAnchorMerkleDisplay = "89fefa563475540b4b0caac428119e5c8e5df7ad816bb9364f4a06adcac083ed"
	junoAnchorCommitDisplay = "19370bc0943184ac70a68f4c4b715ea8571f55d874b5dd77609dad943870ec8a"
	junoAnchorNonceDisplay  = "000003a58e1bed46e9ee0af14ca2f956ea554a17a7dd33bd7fcd400b166f01e8"
	junoAnchorTime          = uint32(1763394660)
	junoAnchorBitsDisplay   = "1f016f7e"
	junoAnchorSolutionHex   = "79f3a199e52a84c689f1618c1e56bdd570044b64bc10887b26730c574fd10000" // 内部序，原样
)

func revBytes(t *testing.T, displayHex string) []byte {
	t.Helper()
	b, err := hex.DecodeString(displayHex)
	if err != nil {
		t.Fatalf("bad hex %q: %v", displayHex, err)
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func TestJunoMainnetAnchorBlock3000(t *testing.T) {
	hdr := make([]byte, 140)
	binary.LittleEndian.PutUint32(hdr[0:4], 4)
	copy(hdr[4:36], revBytes(t, junoAnchorPrevDisplay))
	copy(hdr[36:68], revBytes(t, junoAnchorMerkleDisplay))
	copy(hdr[68:100], revBytes(t, junoAnchorCommitDisplay))
	binary.LittleEndian.PutUint32(hdr[100:104], junoAnchorTime)
	copy(hdr[104:108], revBytes(t, junoAnchorBitsDisplay))
	copy(hdr[108:140], revBytes(t, junoAnchorNonceDisplay))

	seed := revBytes(t, junoAnchorSeedDisplay)
	want, _ := hex.DecodeString(junoAnchorSolutionHex)

	h := NewNamed("rx/juno")
	got, err := h.HashKeyed(seed, hdr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("rx/juno 主网锚失配:\ngot  %x\nwant %s\n（怀疑引擎参数/头喂法/seed 字节序）", got, junoAnchorSolutionHex)
	}
}
