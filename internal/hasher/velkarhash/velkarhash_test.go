package velkarhash

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// TestVelkarHashKAT 金锚:velkar-cpuminer pow.rs::velkar_stratum_pow_regression。
func TestVelkarHashKAT(t *testing.T) {
	if err := (velkarHasher{}).SelfTest(); err != nil {
		t.Fatal(err)
	}
}

// TestHashBlob 走 80 字节 blob 接口路径，应与 CalculatePow 一致。
func TestHashBlob(t *testing.T) {
	prePow, _ := hex.DecodeString("f92b22c908fe912ef58501e2baa9253373d5d7f641232e8d4b386f61f4156a19")
	targetBE, _ := hex.DecodeString("00003d647c000000000000000000000000000000000000000000000000000000")
	target := reverseBytes(targetBE)

	blob := make([]byte, 80)
	copy(blob[0:32], prePow)
	binary.LittleEndian.PutUint64(blob[32:40], 1781329471115)
	binary.LittleEndian.PutUint64(blob[40:48], 0x3a333445c075fa3d)
	copy(blob[48:80], target)

	got, err := velkarHasher{}.Hash(blob)
	if err != nil {
		t.Fatal(err)
	}
	wantBE := "000382fb5b1c9028d630c4a00c5cb9fdbebca9fb83882e61c87f57b27656c22d"
	if gotBE := hex.EncodeToString(reverseBytes(got)); gotBE != wantBE {
		t.Fatalf("Hash blob 失配:\n got(be)=%s\nwant(be)=%s", gotBE, wantBE)
	}
}
