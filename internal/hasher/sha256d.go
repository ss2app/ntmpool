package hasher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// sha256d：double-SHA256，覆盖标准比特币系/绝大多数分叉山寨币。
// 这是 M1 竖切用的第一个算法（纯 stdlib，无 FFI，先验证架构）。
type sha256d struct{}

func init() { Register(sha256d{}) }

func (sha256d) Name() string { return "sha256d" }

func (sha256d) Hash(input []byte) ([]byte, error) {
	h1 := sha256.Sum256(input)
	h2 := sha256.Sum256(h1[:])
	return h2[:], nil
}

// SelfTest 金锚：Bitcoin 创世块 80 字节 header → 已知块哈希（内部字节序）。
func (s sha256d) SelfTest() error {
	header, err := hex.DecodeString(
		"01000000" +
			"0000000000000000000000000000000000000000000000000000000000000000" +
			"3ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a" +
			"29ab5f49" + "ffff001d" + "1dac2b7c")
	if err != nil {
		return err
	}
	want, _ := hex.DecodeString("6fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000")
	got, err := s.Hash(header)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("sha256d 金锚失配: got=%x want=%x", got, want)
	}
	return nil
}
