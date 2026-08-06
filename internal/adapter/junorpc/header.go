package junorpc

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// Juno Cash（Zcash 6.x 系）块头字节规格 —— 与官方 junorig PROTOCOL_STRATUM.md /
// 节点 pow.cpp CheckRandomXSolution（CEquihashInput + nNonce）逐字段对齐：
//
//	[0:4]     version           LE u32（当前 4）
//	[4:36]    prevHash          display hex 反转（内部序）
//	[36:68]   merkleRoot        display hex 反转（GBT defaultroots.merkleroot）
//	[68:100]  blockCommitments  display hex 反转（GBT defaultroots.blockcommitmentshash；
//	                            dragonx 此槽位是 finalsaplingroot，juno 是 NU5 块承诺）
//	[100:104] time              LE u32
//	[104:108] bits              display hex 4B 反转
//	[108:140] nonce             矿工滚 [108:112]、池 tag [112:116]、盐 [116:120]、余零
//
// RandomX 输入 = 完整 140B；pow = RandomX hash = 链上块 hash（单段，
// 节点 block.cpp GetHash() 直接返回 nSolution）。
// 完整块 = 140B ‖ 0x20 ‖ rx_hash(32) ‖ compactSize(txCount) ‖ coinbase ‖ txs。

const (
	blobLen       = 140
	nonceOffset   = 108
	nonceFieldLen = 32
)

func reverseHex32(displayHex string) ([]byte, error) {
	b, err := hex.DecodeString(displayHex)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("非法 32B hash hex %q", displayHex)
	}
	out := make([]byte, 32)
	for i, v := range b {
		out[31-i] = v
	}
	return out, nil
}

// buildHeaderBlob 从 GBT 字段组 140B hashing blob（nonce 区全零，物化/盐由调用方写）。
// merkle/blockcommitments 都来自 GBT defaultroots（display 序）——我们不改交易集，
// defaultroots 即权威，池不自算 merkle（与 dragonx 的差异之一）。
func buildHeaderBlob(version uint32, prevHashDisplay, merkleDisplay, commitmentsDisplay string, curTime uint32, bitsHex string) ([]byte, error) {
	blob := make([]byte, blobLen)
	binary.LittleEndian.PutUint32(blob[0:4], version)

	prev, err := reverseHex32(prevHashDisplay)
	if err != nil {
		return nil, fmt.Errorf("prevhash: %w", err)
	}
	copy(blob[4:36], prev)

	merkle, err := reverseHex32(merkleDisplay)
	if err != nil {
		return nil, fmt.Errorf("merkleroot: %w", err)
	}
	copy(blob[36:68], merkle)

	bc, err := reverseHex32(commitmentsDisplay)
	if err != nil {
		return nil, fmt.Errorf("blockcommitmentshash: %w", err)
	}
	copy(blob[68:100], bc)

	binary.LittleEndian.PutUint32(blob[100:104], curTime)

	bits, err := hex.DecodeString(bitsHex)
	if err != nil || len(bits) != 4 {
		return nil, fmt.Errorf("非法 bits %q", bitsHex)
	}
	for i := 0; i < 4; i++ {
		blob[104+i] = bits[3-i]
	}
	// [108:140] nonce 区保持零
	return blob, nil
}

// appendCompactSize bitcoin varint（compactSize）。
func appendCompactSize(dst []byte, n uint64) []byte {
	switch {
	case n < 0xfd:
		return append(dst, byte(n))
	case n <= 0xffff:
		dst = append(dst, 0xfd)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(n))
		return append(dst, b[:]...)
	case n <= 0xffffffff:
		dst = append(dst, 0xfe)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(n))
		return append(dst, b[:]...)
	default:
		dst = append(dst, 0xff)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], n)
		return append(dst, b[:]...)
	}
}

// serializeBlock 组完整块字节（PROTOCOL_SOLO.md「Serialized Block Format」）：
// blob(140，含最终 nonce) || 0x20 || rx_hash(32) || compactSize(txCount) || coinbase || txs…
func serializeBlock(blob, rxHash []byte, coinbaseData string, txData []string) ([]byte, error) {
	if len(blob) != blobLen {
		return nil, fmt.Errorf("blob 须 %dB, got %d", blobLen, len(blob))
	}
	if len(rxHash) != 32 {
		return nil, fmt.Errorf("rx_hash 须 32B, got %d", len(rxHash))
	}
	cb, err := hex.DecodeString(coinbaseData)
	if err != nil || len(cb) == 0 {
		return nil, fmt.Errorf("coinbase data 非法")
	}
	out := make([]byte, 0, blobLen+33+9+len(cb))
	out = append(out, blob...)
	out = append(out, 0x20)
	out = append(out, rxHash...)
	out = appendCompactSize(out, uint64(1+len(txData)))
	out = append(out, cb...)
	for i, td := range txData {
		b, err := hex.DecodeString(td)
		if err != nil {
			return nil, fmt.Errorf("tx[%d] data 非法 hex", i)
		}
		out = append(out, b...)
	}
	return out, nil
}

// validSeedHex GBT randomxseedhash 形状校验（64 hex = 32B）。
// ⚠ 它已是【内部序】（节点 rpc/mining.cpp 用 HexStr(begin,end) 输出，非 GetHex），
// junorig 也是原样 fromHex 直用 → 池同样原样透传，绝不反转（brisvia 的「反转」
// 经验在这里恰恰不适用，两条链的输出口径不同）。
func validSeedHex(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("randomxseedhash 长度 %d ≠ 64", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("randomxseedhash 非法 hex: %w", err)
	}
	return nil
}

// unifiedAddrPrefixes juno 三网统一地址 HRP（crates/zcash_protocol constants 实核）。
var unifiedAddrPrefixes = []string{"j1", "jtest1", "jregtest1"}
