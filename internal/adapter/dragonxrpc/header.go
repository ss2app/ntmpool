package dragonxrpc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// dragonx（Hush/Komodo 系）块头字节规格 —— 真链块 #3131000 逐字节金锚验证
// （docs/06 §0；本包 TestHeaderAnchorBlock3131000）：
//
//	[0:4]     version     LE u32
//	[4:36]    prevHash    display hex 反转（内部序）
//	[36:68]   merkleRoot  内部序（txid 反转后跑标准 bitcoin merkle）
//	[68:100]  finalSaplingRoot display hex 反转
//	[100:104] time        LE u32
//	[104:108] bits        display hex 4B 反转
//	[108:140] nonce       矿工滚 [108:112]、池 tag [112:116]、保留 [116:140]
//
// 完整头 173B = 140B || 0x20 || rx_hash(32)；sha256d(173B) 反转 = 链上块 hash。

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
// finalSaplingDisplay 为空 = 32 字节零（Sapling 未激活的链；dragonx 主网恒有值）。
func buildHeaderBlob(version uint32, prevHashDisplay, finalSaplingDisplay string, merkleInternal []byte, curTime uint32, bitsHex string) ([]byte, error) {
	if len(merkleInternal) != 32 {
		return nil, fmt.Errorf("merkle root 须 32B, got %d", len(merkleInternal))
	}
	blob := make([]byte, blobLen)
	binary.LittleEndian.PutUint32(blob[0:4], version)

	prev, err := reverseHex32(prevHashDisplay)
	if err != nil {
		return nil, fmt.Errorf("prevhash: %w", err)
	}
	copy(blob[4:36], prev)
	copy(blob[36:68], merkleInternal)

	if finalSaplingDisplay != "" {
		fs, err := reverseHex32(finalSaplingDisplay)
		if err != nil {
			return nil, fmt.Errorf("finalsaplingroot: %w", err)
		}
		copy(blob[68:100], fs)
	}
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

// merkleRootInternal txid（display hex）列表 → 内部序 merkle root（标准 bitcoin 规则：
// 叶 = txid 反转；两两 sha256d(l||r)；奇数复制末叶）。首元素须为 coinbase txid。
func merkleRootInternal(txidsDisplay []string) ([]byte, error) {
	if len(txidsDisplay) == 0 {
		return nil, fmt.Errorf("merkle: 空 txid 列表")
	}
	level := make([][]byte, len(txidsDisplay))
	for i, txid := range txidsDisplay {
		leaf, err := reverseHex32(txid)
		if err != nil {
			return nil, fmt.Errorf("merkle txid[%d]: %w", i, err)
		}
		level[i] = leaf
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([][]byte, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			d1 := sha256.Sum256(append(append([]byte{}, level[i]...), level[i+1]...))
			d2 := sha256.Sum256(d1[:])
			h := make([]byte, 32)
			copy(h, d2[:])
			next[i/2] = h
		}
		level = next
	}
	return level[0], nil
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

// serializeBlock 组完整块字节：
// blob(140，含最终 nonce) || 0x20 || rx_hash(32) || compactSize(txCount) || coinbase || txs…
func serializeBlock(blob, rxHash []byte, coinbaseData string, txData []string) ([]byte, error) {
	if len(blob) != blobLen {
		return nil, fmt.Errorf("blob 须 %dB, got %d", blobLen, len(blob))
	}
	if len(rxHash) != 32 {
		return nil, fmt.Errorf("rx_hash 须 32B, got %d（BlobSolution.AuxHash 缺失?）", len(rxHash))
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

// seedHeight rx/dragonx epoch 规则（Hush -ac_randomx_interval=1024 / lag=64，
// miningcore DragonXJobManager 同款；真链两爆块 + 哈希器锚3 双背书）。
func seedHeight(h, interval, lag uint64) uint64 {
	if h < interval+lag {
		return 0
	}
	return (h - lag) / interval * interval
}
