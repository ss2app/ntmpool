//go:build randomx

package scashrx

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/hasher"
)

func TestSaltedRandomXGenesisGolden(t *testing.T) {
	if err := New().SelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestHasherRejectsSolvedHeaderAsInput(t *testing.T) {
	header, _ := hex.DecodeString(testZeroedHeaderHex)
	key, _ := hex.DecodeString(genesisKeyRawHex)
	header[RXOffset] = 1
	if _, err := New().Hash(key, header); err == nil {
		t.Fatal("尾 32B 非零的 solved header 未被拒绝")
	}
}

func TestForgedCommitmentCannotReplaceRecomputedR(t *testing.T) {
	zeroed, _ := hex.DecodeString(testZeroedHeaderHex)
	key, _ := hex.DecodeString(genesisKeyRawHex)
	target := new(big.Int).Lsh(big.NewInt(0x7fffff), 8*(0x1e-3))

	var forgedR [32]byte
	found := false
	for nonce := uint32(1); nonce < 2_000_000; nonce++ {
		binary.LittleEndian.PutUint32(forgedR[:4], nonce)
		cm := Commitment(zeroed, forgedR)
		if btcwork.HashMeetsTarget(cm[:], target) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("未找到测试用 forged R commitment")
	}
	solved := append([]byte(nil), zeroed...)
	copy(solved[RXOffset:], forgedR[:])
	if _, err := New().VerifySolvedHeader(key, solved); err == nil {
		t.Fatal("仅 CM 达 target、stored R 非真值的伪造 header 被接受")
	}
}

func TestStandaloneStartupSelfTest(t *testing.T) {
	if err := hasher.SelfTestAll([]string{Algorithm}); err != nil {
		t.Fatal(err)
	}
}
