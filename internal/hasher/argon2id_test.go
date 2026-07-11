package hasher

import (
	"encoding/hex"
	"testing"
)

func TestArgon2idBtc09SelfTest(t *testing.T) {
	h, err := Get("argon2id-btc09")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestArgon2idBtc09RejectsBadLength(t *testing.T) {
	h, err := Get("argon2id-btc09")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Hash(make([]byte, 80)); err == nil {
		t.Fatal("80-byte input unexpectedly accepted")
	}
}

func TestArgon2idBtc09ViaSelfTestAll(t *testing.T) {
	if err := SelfTestAll([]string{"argon2id-btc09"}); err != nil {
		t.Fatal(err)
	}
}

// 防手滑：金锚向量本身的基本形状。
func TestArgon2idBtc09VectorShape(t *testing.T) {
	genesis, err := hex.DecodeString(
		"0100000000000000000000000000000000000000000000000000000000000000" +
			"000000001bb3769741e1631ea344cdd40192f7625aed86f37b52d45acb9c4300" +
			"d5478e36a09b4a6a00000000ffff001ff64e000000000000")
	if err != nil || len(genesis) != 88 {
		t.Fatalf("genesis vector: len=%d err=%v", len(genesis), err)
	}
}
