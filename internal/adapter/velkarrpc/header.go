package velkarrpc

// header.go —— 从 GetBlockTemplate 返回的 RpcBlockHeader 计算 VelkarHash 挖矿所需的
// 两个共识量：① pre_pow_hash（喂矿工 + CalculatePow 的 prePow 参数）；② block target
// （从 header.bits 解出的网络 target，stage4 吃它——这是 VelkarHash 与普通 blob 链
// "target 只比较不入哈希" 的唯一差别，见 docs/08 §3 / velkarhash 包注释）。
//
// pre_pow_hash 逐字节复刻节点 consensus/core/src/hashing/header.rs::hash_override_nonce_time：
// BlockHash(blake2b-256 keyed "BlockHash") 覆盖头序列化，nonce/timestamp 置 0。
// 同一序列化传入真实 nonce/timestamp 即得块 id（= header hash），故 headerHash 可用
// "真链块 → headerHash(header,realNonce,realTs) == 节点给的块 hash" 自证正确（见 header_test.go）。
//
// block target 复刻 math/src/lib.rs::from_compact_target_bits（satoshi 紧凑浮点编码）。

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc/protowire"
	"golang.org/x/crypto/blake2b"
)

// headerHash 复刻 hash_override_nonce_time(header, nonce, timestamp)。
// nonce=0,timestamp=0 → pre_pow_hash；nonce/timestamp=真值 → 块 id。返回 32B（节点内部序，
// 与 Hash::to_string() 的 hex 字节同序，非 bitcoin 反转）。
func headerHash(h *protowire.RpcBlockHeader, nonce, timestamp uint64) ([]byte, error) {
	bh, err := blake2b.New(32, []byte("BlockHash"))
	if err != nil {
		return nil, err
	}

	// version: u16 little-endian（节点 header.version 是 u16，proto 里放宽成 u32）
	var b2 [2]byte
	binary.LittleEndian.PutUint16(b2[:], uint16(h.Version))
	bh.Write(b2[:])

	// 父层数 expanded_len: u64 LE（RPC parents 即展开后的 by-level 结构）
	writeLen(bh, len(h.Parents))
	// 每层 write_var_array(level) = write_len(层内父数) + 各 32B 父 hash
	for _, level := range h.Parents {
		if level == nil {
			writeLen(bh, 0)
			continue
		}
		writeLen(bh, len(level.ParentHashes))
		for _, ph := range level.ParentHashes {
			pb, err := decodeHash32(ph)
			if err != nil {
				return nil, fmt.Errorf("父 hash %q: %w", ph, err)
			}
			bh.Write(pb)
		}
	}

	// hash_merkle_root / accepted_id_merkle_root / utxo_commitment：各 32B
	for _, hx := range []string{h.HashMerkleRoot, h.AcceptedIdMerkleRoot, h.UtxoCommitment} {
		hb, err := decodeHash32(hx)
		if err != nil {
			return nil, fmt.Errorf("头 hash 字段 %q: %w", hx, err)
		}
		bh.Write(hb)
	}

	// timestamp(u64 LE) · bits(u32 LE) · nonce(u64 LE) · daa_score(u64 LE) · blue_score(u64 LE)
	var b8 [8]byte
	binary.LittleEndian.PutUint64(b8[:], timestamp)
	bh.Write(b8[:])
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], h.Bits)
	bh.Write(b4[:])
	binary.LittleEndian.PutUint64(b8[:], nonce)
	bh.Write(b8[:])
	binary.LittleEndian.PutUint64(b8[:], h.DaaScore)
	bh.Write(b8[:])
	binary.LittleEndian.PutUint64(b8[:], h.BlueScore)
	bh.Write(b8[:])

	// blue_work: write_var_bytes(big-endian 去前导零)。big.Int.Bytes() 即最小大端无前导零，
	// 0 → 空切片（与节点 write_blue_work(0)= write_len(0) 一致）。
	bw, ok := new(big.Int).SetString(strings.TrimPrefix(h.BlueWork, "0x"), 16)
	if !ok {
		if strings.TrimSpace(h.BlueWork) == "" {
			bw = big.NewInt(0)
		} else {
			return nil, fmt.Errorf("blueWork 非法 hex %q", h.BlueWork)
		}
	}
	bwBytes := bw.Bytes()
	writeLen(bh, len(bwBytes))
	bh.Write(bwBytes)

	// pruning_point: 32B
	pp, err := decodeHash32(h.PruningPoint)
	if err != nil {
		return nil, fmt.Errorf("pruningPoint %q: %w", h.PruningPoint, err)
	}
	bh.Write(pp)

	return bh.Sum(nil), nil
}

// prePowHash = headerHash(header, 0, 0)（VelkarHash 的 prePow 输入 + 矩阵种子）。
func prePowHash(h *protowire.RpcBlockHeader) ([]byte, error) {
	return headerHash(h, 0, 0)
}

// writeLen 复刻 HasherExtensions::write_len —— usize 以 u64 little-endian 写入。
func writeLen(w io.Writer, n int) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(n))
	_, _ = w.Write(b[:])
}

// decodeHash32 把节点 hex（Hash::to_string，正序非反转）解成 32B。容忍奇数位/短串
// （左填充零，等价 big-int 语义）——对齐 bridge 对 "Odd number of digits" 的兜底。
func decodeHash32(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return make([]byte, 32), nil
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) > 32 {
		return nil, fmt.Errorf("长度 %d 字节 > 32", len(raw))
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out, nil
}

// targetFromBits 复刻 Uint256::from_compact_target_bits(bits)。
// 返回 (32B little-endian（CalculatePow 的 target 参数 = Uint256.to_le_bytes()）, 大端 big.Int（份额/网络难度比较+展示用）)。
func targetFromBits(bits uint32) (leBytes []byte, be *big.Int) {
	unshiftedExpt := bits >> 24
	var mant, expt uint32
	if unshiftedExpt <= 3 {
		mant = (bits & 0xFFFFFF) >> (8 * (3 - unshiftedExpt))
		expt = 0
	} else {
		mant = bits & 0xFFFFFF
		expt = 8 * ((bits >> 24) - 3)
	}
	le := make([]byte, 32)
	if mant > 0x7FFFFF { // 尾数有符号但不许为负 → Uint256::ZERO
		return le, big.NewInt(0)
	}
	t := new(big.Int).Lsh(new(big.Int).SetUint64(uint64(mant)), uint(expt))
	beBytes := t.Bytes() // 大端最小
	for i := 0; i < len(beBytes) && i < 32; i++ {
		le[i] = beBytes[len(beBytes)-1-i] // 大端 → 小端
	}
	return le, t
}
