package hasher

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// argon2idBtc09：Bitcoin 09 (09C) 的共识 PoW。
//
//	PowHash = Argon2id(password=88字节header, salt="BTC09/pow/v1",
//	                   t=1, m=65536 KiB (64 MiB), p=1, out=32)
//
// 与节点 core.PowHash 同源同库（golang.org/x/crypto/argon2——上游节点用的
// 就是这份库），零共识分歧。⚠ 与 BNT 的映射正好相反（BNT: pwd=nonce/
// salt=header/2GiB），别混用。
type argon2idBtc09 struct{}

func init() { Register(argon2idBtc09{}) }

var btc09Salt = []byte("BTC09/pow/v1")

const (
	btc09ArgonTime   = 1
	btc09ArgonMemKiB = 64 * 1024
)

func (argon2idBtc09) Name() string { return "argon2id-btc09" }

func (argon2idBtc09) Hash(input []byte) ([]byte, error) {
	if len(input) != 88 {
		return nil, fmt.Errorf("argon2id-btc09: header 长度 %d ≠ 88", len(input))
	}
	return argon2.IDKey(input, btc09Salt, btc09ArgonTime, btc09ArgonMemKiB, 1, 32), nil
}

// SelfTest 金锚（由 vendored 上游共识码 cmd/powvec 生成，genesis id 与上游
// 公开发布的 ba685f74… 逐字节吻合 = 向量确为主网共识）：
//  1. zero88：88 个零字节 header。
//  2. genesis：主网创世块真实 88 字节 header（nonce 20214）。
func (a argon2idBtc09) SelfTest() error {
	zero := make([]byte, 88)
	wantZero, _ := hex.DecodeString("dc57838e3edfc20a3d2b46635e77b22ad4f22fd64336f2e4ad8bc1210c358071")
	got, err := a.Hash(zero)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, wantZero) {
		return fmt.Errorf("argon2id-btc09 zero88 金锚失配: got=%x want=%x", got, wantZero)
	}

	genesis, err := hex.DecodeString(
		"0100000000000000000000000000000000000000000000000000000000000000" +
			"000000001bb3769741e1631ea344cdd40192f7625aed86f37b52d45acb9c4300" +
			"d5478e36a09b4a6a00000000ffff001ff64e000000000000")
	if err != nil {
		return err
	}
	if len(genesis) != 88 {
		return fmt.Errorf("argon2id-btc09 genesis 向量长度 %d ≠ 88", len(genesis))
	}
	wantGen, _ := hex.DecodeString("000026103581541cf46fe78f172f8972ff1cf80173464fa39c9204357b1cfc7f")
	got, err = a.Hash(genesis)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, wantGen) {
		return fmt.Errorf("argon2id-btc09 genesis 金锚失配: got=%x want=%x", got, wantGen)
	}
	return nil
}
