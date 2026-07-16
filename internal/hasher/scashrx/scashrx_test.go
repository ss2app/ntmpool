package scashrx

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

const testZeroedHeaderHex = "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4adae5494dffff7f1edb1700000000000000000000000000000000000000000000000000000000000000000000"

func TestCommitmentAndBlockIDGolden(t *testing.T) {
	header, _ := hex.DecodeString(testZeroedHeaderHex)
	rawR, _ := hex.DecodeString("86af952d5202ecbf18bef2311391d6cc7951dbb6388a3c78b1a404b6dfdd48e8")
	var r [32]byte
	copy(r[:], rawR)
	cm := Commitment(header, r)
	if got, want := DisplayHex(cm[:]), "0000388a6a0aa5eaa14ce3aa066106e1d3f82a05b4a8fc6c6c7b128924a24868"; got != want {
		t.Fatalf("CM display = %s, want %s", got, want)
	}
	copy(header[RXOffset:], r[:])
	h1 := sha256.Sum256(header)
	h2 := sha256.Sum256(h1[:])
	if got, want := DisplayHex(h2[:]), "0e3ba94819749c208e2526d9b829e0dba109f1bce4e62600c0fc556294f24c82"; got != want {
		t.Fatalf("block ID = %s, want %s", got, want)
	}
}

func TestCommitmentChangesWhenTailIsNotZero(t *testing.T) {
	header, _ := hex.DecodeString(testZeroedHeaderHex)
	var r [32]byte
	copy(r[:], mustDecode(t, "86af952d5202ecbf18bef2311391d6cc7951dbb6388a3c78b1a404b6dfdd48e8"))
	zeroCM := Commitment(header, r)
	header[RXOffset] = 1
	nonZeroCM := Commitment(header, r)
	if zeroCM == nonZeroCM {
		t.Fatal("尾部非零后 commitment 未变化")
	}
	if IsZeroedHeader(header) {
		t.Fatal("非零 R 区被误判为 zeroed header")
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
