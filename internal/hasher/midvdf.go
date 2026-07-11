package hasher

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// midVDF：midstate (MDS) 的共识 PoW——标准 BLAKE3 顺序迭代（官方节点
// core/extension.rs create_extension）：
//
//	x = BLAKE3(mining_midstate[32] ‖ nonce_le8)   // 40 字节
//	重复 iters 次: x = BLAKE3(x)                   // 32 字节
//	final_hash = x
//
// ≤64 字节消息的 BLAKE3 = 单次压缩（CHUNK_START|CHUNK_END|ROOT 三标志同置），
// 这里照公开 spec clean-room 手写 compress（IV/消息置换/G 函数），行为与
// NTMminer mid_core.c 逐字节对齐——1M 次迭代对每次调用的开销敏感，手写零分配
// 快过通用 hasher 接口。金锚 = NTMminer mid_kat.c 同组跨实现向量（期望值由
// Rust 官方 blake3 跑出）+ 真链块 KAT（midvdf_chain_test.go）。
//
// Hash(input) 契约：input = 40 字节（midstate ‖ nonce_le8），返回 32 字节 final_hash。
type midVDF struct {
	iters uint32
}

// midIterations 官方 EXTENSION_ITERATIONS（core/types.rs:719）。
const midIterations = 1_000_000

func init() { Register(midVDF{iters: midIterations}) }

// MidVDFWithIters 返回指定迭代数的 midvdf（e2e/单测用小值提速；生产走注册表 1M）。
func MidVDFWithIters(iters uint32) Hasher { return midVDF{iters: iters} }

func (midVDF) Name() string { return "midvdf" }

func (m midVDF) Hash(input []byte) ([]byte, error) {
	if len(input) != 40 {
		return nil, fmt.Errorf("midvdf: 输入长度 %d ≠ 40（midstate‖nonce_le8）", len(input))
	}
	// seed block：40 字节 = 10 个 LE 字 + 6 个零字。
	var blk [16]uint32
	for i := 0; i < 10; i++ {
		blk[i] = binary.LittleEndian.Uint32(input[i*4:])
	}
	var h [8]uint32
	blake3Compress(&blake3IV, &blk, 40, midHashFlags, &h)

	// 迭代步：block = 上一步 hash（8 字）+ 8 个零字，block_len=32。
	for it := uint32(0); it < m.iters; it++ {
		blk = [16]uint32{h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]}
		blake3Compress(&blake3IV, &blk, 32, midHashFlags, &h)
	}

	out := make([]byte, 32)
	for i, w := range h {
		binary.LittleEndian.PutUint32(out[i*4:], w)
	}
	return out, nil
}

// SelfTest 金锚：mid_kat.c 同组（midstate=0x00..0x1f，全量 1M 迭代，期望值来自
// Rust 官方 blake3 genanchor.rs）。取 3 个覆盖 nonce 零值/high-32-bit/全 1 边界；
// 全 6 组在 midvdf_test.go。
func (m midVDF) SelfTest() error {
	if m.iters != midIterations {
		return fmt.Errorf("midvdf: 非生产迭代数 %d 不做金锚自检", m.iters)
	}
	anchors := []struct {
		nonce uint64
		want  string
	}{
		{0x0000000000000000, "949382421e5f14787f4c0a7074c896dc12a005b6bdb78832145157f7306f4b48"},
		{0x0000000100000005, "8635d9819d4398c34af8515d0df095dca1d69c7fed46cc4a30320f3d1abf2f5c"},
		{0xffffffffffffffff, "746cd516f24dd3496c9af4d489c2e29068f88f93bc4bc9101ed5af01b23922ef"},
	}
	var ms [32]byte
	for i := range ms {
		ms[i] = byte(i)
	}
	for _, a := range anchors {
		got, err := m.Hash(MidSeed(ms[:], a.nonce))
		if err != nil {
			return err
		}
		want, _ := hex.DecodeString(a.want)
		if !bytes.Equal(got, want) {
			return fmt.Errorf("midvdf 金锚失配 nonce=%#x: got=%x want=%s", a.nonce, got, a.want)
		}
	}
	return nil
}

// MidSeed 拼 VDF 输入：midstate(32B) ‖ nonce 小端 8 字节。
func MidSeed(midstate []byte, nonce uint64) []byte {
	seed := make([]byte, 40)
	copy(seed, midstate)
	binary.LittleEndian.PutUint64(seed[32:], nonce)
	return seed
}

// MidSalt coinbase 输出盐 = blake3(height_le8 ‖ index_le8)——fork 池 derive_salt
// 同款（盐只需 per-输出唯一即可；确定性让 Expected 重试重建同一模板）。
func MidSalt(height, index uint64) []byte {
	var msg [16]byte
	binary.LittleEndian.PutUint64(msg[:8], height)
	binary.LittleEndian.PutUint64(msg[8:], index)
	out := blake3Short(msg[:])
	return out[:]
}

// blake3Short 标准 BLAKE3（≤1024 字节 = 单 chunk，多块链式压缩）。
// 非 VDF 热路径（盐派生/测试重放用）。
func blake3Short(msg []byte) [32]byte {
	n := len(msg)
	numBlocks := (n + 63) / 64
	if numBlocks == 0 {
		numBlocks = 1
	}
	cv := blake3IV
	for i := 0; i < numBlocks; i++ {
		start := i * 64
		end := start + 64
		if end > n {
			end = n
		}
		var buf [64]byte
		copy(buf[:], msg[start:end])
		var blk [16]uint32
		for j := 0; j < 16; j++ {
			blk[j] = binary.LittleEndian.Uint32(buf[j*4:])
		}
		flags := uint32(0)
		if i == 0 {
			flags |= blake3ChunkStart
		}
		if i == numBlocks-1 {
			flags |= blake3ChunkEnd | blake3Root
		}
		var out [8]uint32
		blake3Compress(&cv, &blk, uint32(end-start), flags, &out)
		cv = out
	}
	var res [32]byte
	for i, w := range cv {
		binary.LittleEndian.PutUint32(res[i*4:], w)
	}
	return res
}

// ---- BLAKE3 单块压缩（clean-room，照公开 spec）----

// blake3IV = SHA-256 IV 高 8 字（BLAKE3 spec §2.1）。
var blake3IV = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

// BLAKE3 域标志（spec §2.3）。midHashFlags = 单块一次性 hash 三标志同置。
const (
	blake3ChunkStart = 1
	blake3ChunkEnd   = 2
	blake3Root       = 8
	midHashFlags     = blake3ChunkStart | blake3ChunkEnd | blake3Root
)

// blake3Schedule 消息置换表，每轮一行（BLAKE3 spec §2.2，共 7 轮）。
var blake3Schedule = [7][16]uint8{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8},
	{3, 4, 10, 12, 13, 2, 7, 14, 6, 5, 9, 0, 11, 15, 8, 1},
	{10, 7, 12, 9, 14, 3, 13, 15, 4, 0, 11, 2, 5, 8, 1, 6},
	{12, 13, 9, 11, 15, 10, 14, 8, 7, 2, 5, 3, 0, 1, 6, 4},
	{9, 14, 11, 5, 8, 12, 15, 1, 13, 3, 0, 10, 2, 6, 4, 7},
	{11, 15, 5, 0, 1, 9, 8, 6, 14, 10, 2, 12, 3, 4, 7, 13},
}

// blake3Compress 一次 BLAKE3 压缩（counter=0，单 chunk 内够用）：
// v[14]/v[15] = blockLen/flags；7 轮后 out[i] = v[i] ^ v[i+8]。
func blake3Compress(cv *[8]uint32, block *[16]uint32, blockLen, flags uint32, out *[8]uint32) {
	var v [16]uint32
	copy(v[:8], cv[:])
	v[8], v[9], v[10], v[11] = blake3IV[0], blake3IV[1], blake3IV[2], blake3IV[3]
	v[12], v[13], v[14], v[15] = 0, 0, blockLen, flags
	for r := 0; r < 7; r++ {
		s := &blake3Schedule[r]
		g(&v, 0, 4, 8, 12, block[s[0]], block[s[1]])
		g(&v, 1, 5, 9, 13, block[s[2]], block[s[3]])
		g(&v, 2, 6, 10, 14, block[s[4]], block[s[5]])
		g(&v, 3, 7, 11, 15, block[s[6]], block[s[7]])
		g(&v, 0, 5, 10, 15, block[s[8]], block[s[9]])
		g(&v, 1, 6, 11, 12, block[s[10]], block[s[11]])
		g(&v, 2, 7, 8, 13, block[s[12]], block[s[13]])
		g(&v, 3, 4, 9, 14, block[s[14]], block[s[15]])
	}
	for i := 0; i < 8; i++ {
		out[i] = v[i] ^ v[i+8]
	}
}

// g BLAKE3 G 混合（rotate-right 16/12/8/7）。
func g(v *[16]uint32, a, b, c, d int, mx, my uint32) {
	v[a] += v[b] + mx
	v[d] = rotr32(v[d]^v[a], 16)
	v[c] += v[d]
	v[b] = rotr32(v[b]^v[c], 12)
	v[a] += v[b] + my
	v[d] = rotr32(v[d]^v[a], 8)
	v[c] += v[d]
	v[b] = rotr32(v[b]^v[c], 7)
}

func rotr32(x uint32, n uint) uint32 { return x>>n | x<<(32-n) }
