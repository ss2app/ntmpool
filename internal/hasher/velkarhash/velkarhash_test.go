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

// TestHashBlob 走 80 字节 blob 接口路径，应与 CalculatePow 一致（主网 KAT 输入）。
func TestHashBlob(t *testing.T) {
	var prePow, target [32]byte
	for i := range prePow {
		prePow[i] = byte(i) // 00..1f
	}
	for i := range target {
		target[i] = byte(32 + i) // 20..3f
	}

	blob := make([]byte, 80)
	copy(blob[0:32], prePow[:])
	binary.LittleEndian.PutUint64(blob[32:40], 0x1122334455667788)
	binary.LittleEndian.PutUint64(blob[40:48], 0x0123456789abcdef)
	copy(blob[48:80], target[:])

	got, err := velkarHasher{}.Hash(blob)
	if err != nil {
		t.Fatal(err)
	}
	wantLE := "fa4d0a039f977f9aac19037b4e03ad8a0e6675e1695cf61af12ed1f114777e0e"
	if gotLE := hex.EncodeToString(got); gotLE != wantLE {
		t.Fatalf("Hash blob 失配:\n got(le)=%s\nwant(le)=%s", gotLE, wantLE)
	}
}
