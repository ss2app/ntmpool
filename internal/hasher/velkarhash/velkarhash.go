// Package velkarhash 实现 Velkar (VELK) 的共识 PoW —— VelkarHash，池端重算哈希器。
//
// VelkarHash 是 Kaspa kHeavyHash 的魔改版（4 阶段，故意与原版 Kaspa 锄头不兼容）：
//
//	stage1 = PowHash(keccak-f1600, cSHAKE256"ProofOfWorkHash" 初始态)
//	stage2 = 64×64 nibble 矩阵 heavy_hash(+内嵌 KHeavyHash)   ← ①②为原版 kHeavyHash
//	stage3 = KHeavyHash(stage2)                              ← ★额外
//	stage4 = blake2b-keyed("ProofOfWorkHash")(stage3‖nonce_le8‖target_le32)  ← ★额外、吃 target
//	pow    = stage4 ⊕ {rotl(stage1.w0,17),rotr(stage2.w1,11),rotl(stage3.w2,7),rotl(nonce,13)}
//
// 权威源：velkar 节点 consensus/pow/src/lib.rs + crypto/hashes/src/pow_hashers.rs
// （见 _knowledge/algorithms/VelkarHash-velkar-spec.md）。
//
// 同源铁律（hasher.go 铁律 1）：VelkarHash 是标准原语组合（keccak-f1600 ×3 + 64×64
// nibble 矩阵 + blake2b），非 RandomX 那种必须 FFI 的复杂算法；走纯 Go 是工厂既定先例
// （argon2id/quark/midvdf 同）。共识一致性靠：① KAT 金锚逐字节钉死（velkar-cpuminer
// pow.rs::velkar_stratum_pow_regression，pow=000382fb…）；② 真链多块对拍。
//
// ⚠ 浮点共识点：矩阵满秩判定 computeRank 用 f64 高斯消元（与节点 matrix.rs 同）。
// Go 默认不做 FMA 融合（除非 math.FMA），与节点 -ffp-contract=off 口径一致；nibble
// 矩阵值域 0..15 远离奇异，EPS=1e-9 边界 case 极罕见。KAT + 真链对拍兜底。
package velkarhash

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"

	"github.com/scashcc/ntmpool/internal/hasher"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
)

// ── VelkarHash 共识常量（crypto/hashes/src/pow_hashers.rs，cSHAKE256 padding 已折进）──

// PowHash cSHAKE256("ProofOfWorkHash") 初始态。
var initialStatePow = [25]uint64{
	1242148031264380989, 3008272977830772284, 2188519011337848018, 1992179434288343456, 8876506674959887717,
	5399642050693751366, 1745875063082670864, 8605242046444978844, 17936695144567157056, 3343109343542796272,
	1123092876221303306, 4963925045340115282, 17037383077651887893, 16629644495023626889, 12833675776649114147,
	3784524041015224902, 1082795874807940378, 13952716920571277634, 13411128033953605860, 15060696040649351053,
	9928834659948351306, 5237849264682708699, 12825353012139217522, 6706187291358897596, 196324915476054915,
}

// KHeavyHash cSHAKE256("HeavyHash") 初始态。
var initialStateKHeavy = [25]uint64{
	4239941492252378377, 8746723911537738262, 8796936657246353646, 1272090201925444760, 16654558671554924250,
	8270816933120786537, 13907396207649043898, 6782861118970774626, 9239690602118867528, 11582319943599406348,
	17596056728278508070, 15212962468105129023, 7812475424661425213, 3370482334374859748, 5690099369266491460,
	8596393687355028144, 570094237299545110, 9119540418498120711, 16901969272480492857, 13372017233735502424,
	14372891883993151831, 5171152063242093102, 10573107899694386186, 6096431547456407061, 1592359455985097269,
}

// ── Keccak-p[1600, 24]（标准，与节点 asm KeccakF1600 / tiny-keccak 一致）──

var keccakRC = [24]uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000,
	0x000000000000808B, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
	0x000000000000008A, 0x0000000000000088, 0x0000000080008009, 0x000000008000000A,
	0x000000008000808B, 0x800000000000008B, 0x8000000000008089, 0x8000000000008003,
	0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
	0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}
var keccakRotc = [24]uint{
	1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14,
	27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44,
}
var keccakPiln = [24]int{
	10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4,
	15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1,
}

func keccakF1600(st *[25]uint64) {
	var bc [5]uint64
	for round := 0; round < 24; round++ {
		// Theta
		for i := 0; i < 5; i++ {
			bc[i] = st[i] ^ st[i+5] ^ st[i+10] ^ st[i+15] ^ st[i+20]
		}
		for i := 0; i < 5; i++ {
			t := bc[(i+4)%5] ^ bits.RotateLeft64(bc[(i+1)%5], 1)
			for j := 0; j < 25; j += 5 {
				st[j+i] ^= t
			}
		}
		// Rho + Pi
		t := st[1]
		for i := 0; i < 24; i++ {
			j := keccakPiln[i]
			tmp := st[j]
			st[j] = bits.RotateLeft64(t, int(keccakRotc[i]))
			t = tmp
		}
		// Chi
		for j := 0; j < 25; j += 5 {
			for i := 0; i < 5; i++ {
				bc[i] = st[j+i]
			}
			for i := 0; i < 5; i++ {
				st[j+i] ^= (^bc[(i+1)%5]) & bc[(i+2)%5]
			}
		}
		// Iota
		st[0] ^= keccakRC[round]
	}
}

// ── XoShiRo256++（consensus/pow/src/xoshiro.rs）──

type xoshiro struct{ s0, s1, s2, s3 uint64 }

func newXoshiro(seed []byte) *xoshiro {
	return &xoshiro{
		s0: binary.LittleEndian.Uint64(seed[0:8]),
		s1: binary.LittleEndian.Uint64(seed[8:16]),
		s2: binary.LittleEndian.Uint64(seed[16:24]),
		s3: binary.LittleEndian.Uint64(seed[24:32]),
	}
}

func (x *xoshiro) next() uint64 {
	res := x.s0 + bits.RotateLeft64(x.s0+x.s3, 23)
	t := x.s1 << 17
	x.s2 ^= x.s0
	x.s3 ^= x.s1
	x.s1 ^= x.s2
	x.s0 ^= x.s3
	x.s2 ^= t
	x.s3 = bits.RotateLeft64(x.s3, 45)
	return res
}

// ── 矩阵（consensus/pow/src/matrix.rs）──

// generateMatrix 由 pre_pow_hash 派生 64×64 nibble 矩阵，循环到满秩 64。
func generateMatrix(prePow []byte) *[64][64]uint16 {
	gen := newXoshiro(prePow)
	for {
		var m [64][64]uint16
		for i := 0; i < 64; i++ {
			var val uint64
			for j := 0; j < 64; j++ {
				shift := j % 16
				if shift == 0 {
					val = gen.next()
				}
				m[i][j] = uint16((val >> (4 * uint(shift))) & 0x0F)
			}
		}
		if computeRank(&m) == 64 {
			return &m
		}
	}
}

// computeRank f64 高斯消元求秩（matrix.rs::compute_rank，共识关键，运算顺序须与节点一致）。
func computeRank(m *[64][64]uint16) int {
	const eps = 1e-9
	var mat [64][64]float64
	for i := 0; i < 64; i++ {
		for j := 0; j < 64; j++ {
			mat[i][j] = float64(m[i][j])
		}
	}
	rank := 0
	var rowSelected [64]bool
	for i := 0; i < 64; i++ {
		j := 0
		for j < 64 {
			if !rowSelected[j] && math.Abs(mat[j][i]) > eps {
				break
			}
			j++
		}
		if j != 64 {
			rank++
			rowSelected[j] = true
			for p := i + 1; p < 64; p++ {
				mat[j][p] /= mat[j][i]
			}
			for k := 0; k < 64; k++ {
				if k != j && math.Abs(mat[k][i]) > eps {
					for p := i + 1; p < 64; p++ {
						mat[k][p] -= mat[j][p] * mat[k][i]
					}
				}
			}
		}
	}
	return rank
}

// heavyHash stage2：32B 输入 → 64 nibble 向量 → 矩阵乘 → 拼回 32B → XOR 输入 → 内嵌 KHeavyHash。
func heavyHash(m *[64][64]uint16, in []byte) []byte {
	var vec [64]uint16
	for i := 0; i < 32; i++ {
		vec[2*i] = uint16(in[i] >> 4)
		vec[2*i+1] = uint16(in[i] & 0x0F)
	}
	var product [32]byte
	for i := 0; i < 32; i++ {
		var sum1, sum2 uint16 // 最大 64×15×15=14400 < 2^16，不溢出（与节点 u16 累加同）
		for j := 0; j < 64; j++ {
			sum1 += m[2*i][j] * vec[j]
			sum2 += m[2*i+1][j] * vec[j]
		}
		product[i] = byte(((sum1 >> 10) << 4) | (sum2 >> 10))
	}
	for i := 0; i < 32; i++ {
		product[i] ^= in[i]
	}
	return kHeavyHash(product[:])
}

// kHeavyHash KHeavyHash：32B → keccak-f1600(初始态 XOR 输入) → 32B。stage3 与 heavyHash 内部共用。
func kHeavyHash(in []byte) []byte {
	st := initialStateKHeavy
	for i := 0; i < 4; i++ {
		st[i] ^= binary.LittleEndian.Uint64(in[i*8:])
	}
	keccakF1600(&st)
	out := make([]byte, 32)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i*8:], st[i])
	}
	return out
}

// powHashStage1 PowHash：pre_pow(32B) + timestamp + nonce → keccak-f1600 → 32B。
func powHashStage1(prePow []byte, timestamp, nonce uint64) []byte {
	st := initialStatePow
	for i := 0; i < 4; i++ {
		st[i] ^= binary.LittleEndian.Uint64(prePow[i*8:])
	}
	st[4] ^= timestamp
	st[9] ^= nonce
	keccakF1600(&st)
	out := make([]byte, 32)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i*8:], st[i])
	}
	return out
}

// CalculatePow 计算 VelkarHash 的 256 位 pow（返回 32B little-endian；与节点 State::calculate_pow
// 逐字节一致，pow ≤ target 即中奖）。prePow/target 均为 32B little-endian。
func CalculatePow(prePow []byte, timestamp, nonce uint64, target []byte) ([]byte, error) {
	if len(prePow) != 32 {
		return nil, fmt.Errorf("velkarhash: prePow 长度 %d ≠ 32", len(prePow))
	}
	if len(target) != 32 {
		return nil, fmt.Errorf("velkarhash: target 长度 %d ≠ 32", len(target))
	}

	stage1 := powHashStage1(prePow, timestamp, nonce)
	m := generateMatrix(prePow)
	stage2 := heavyHash(m, stage1)
	stage3 := kHeavyHash(stage2)

	// stage4 = Argon2id memory-hard（★主网新增，consensus/pow/src/lib.rs::memory_hard_hash）
	stage4 := memoryHardHash(stage3, stage1, nonce, target)

	// stage5 = blake2b-keyed("ProofOfWorkHash")(stage4 ‖ nonce_le8 ‖ target_le32)
	bh, err := blake2b.New(32, []byte("ProofOfWorkHash"))
	if err != nil {
		return nil, err
	}
	bh.Write(stage4)
	var nb [8]byte
	binary.LittleEndian.PutUint64(nb[:], nonce)
	bh.Write(nb[:])
	bh.Write(target)
	stage5 := bh.Sum(nil)

	// 4 字 rotate-XOR 混合（主网：words[3] 额外 ⊕ rotl(stage4.w3,13)）
	var words [4]uint64
	for i := 0; i < 4; i++ {
		words[i] = binary.LittleEndian.Uint64(stage5[i*8:])
	}
	words[0] ^= bits.RotateLeft64(binary.LittleEndian.Uint64(stage1[0:8]), 17)
	words[1] ^= bits.RotateLeft64(binary.LittleEndian.Uint64(stage2[8:16]), -11) // rotr 11
	words[2] ^= bits.RotateLeft64(binary.LittleEndian.Uint64(stage3[16:24]), 7)
	words[3] ^= bits.RotateLeft64(binary.LittleEndian.Uint64(stage4[24:32]), 13) ^ bits.RotateLeft64(nonce, 13)

	pow := make([]byte, 32)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(pow[i*8:], words[i])
	}
	return pow, nil
}

// memoryHardHash stage4 = Argon2id memory-hard（consensus/pow/src/lib.rs::memory_hard_hash）：
//
//	password = stage3(32) ‖ target_le(32) ‖ nonce_le(8) = 72B
//	salt[i]  = stage1[i] ^ stage3[i+16], i∈[0,16)      = 16B
//	Argon2id(memory 8192 KiB, time 1, lanes 1, version 0x13, out 32B)
//
// golang.org/x/crypto/argon2.IDKey 固定 version 0x13，与节点 argon2 crate V0x13 一致。
func memoryHardHash(stage3, stage1 []byte, nonce uint64, target []byte) []byte {
	var pwd [72]byte
	copy(pwd[0:32], stage3)
	copy(pwd[32:64], target)
	binary.LittleEndian.PutUint64(pwd[64:72], nonce)
	var salt [16]byte
	for i := 0; i < 16; i++ {
		salt[i] = stage1[i] ^ stage3[i+16]
	}
	return argon2.IDKey(pwd[:], salt[:], 1, 8192, 1, 32)
}

// ── hasher.Hasher 接口（注册 + 金锚自检门禁）──

type velkarHasher struct{}

func init() { hasher.Register(velkarHasher{}) }

func (velkarHasher) Name() string { return "velkarhash" }

// Hash 接受 80 字节 blob = prePow(32) ‖ timestamp_le(8) ‖ nonce_le(8) ‖ target_le(32)，
// 返回 pow(32B LE)。velkar 作业管理器通常直接调 CalculatePow；此接口用于统一注册/自检门禁。
func (velkarHasher) Hash(input []byte) ([]byte, error) {
	if len(input) != 80 {
		return nil, fmt.Errorf("velkarhash: blob 长度 %d ≠ 80 (prePow32‖ts8‖nonce8‖target32)", len(input))
	}
	prePow := input[0:32]
	timestamp := binary.LittleEndian.Uint64(input[32:40])
	nonce := binary.LittleEndian.Uint64(input[40:48])
	target := input[48:80]
	return CalculatePow(prePow, timestamp, nonce, target)
}

// SelfTest 金锚 = 主网 velkar-core consensus/pow/src/lib.rs::State::calculate_pow
// （NTM ntm_velkar_kat::ntm_kat_dump 固定输入；含 stage4 Argon2id）。
func (h velkarHasher) SelfTest() error {
	var prePow [32]byte
	for i := range prePow {
		prePow[i] = byte(i) // prePow_le = 00..1f
	}
	var target [32]byte
	for i := range target {
		target[i] = byte(32 + i) // target_le = 20..3f
	}
	const timestamp = uint64(0x1122334455667788)
	const nonce = uint64(0x0123456789abcdef)

	got, err := CalculatePow(prePow[:], timestamp, nonce, target[:])
	if err != nil {
		return err
	}
	// 主网 rust calculate_pow 输出（pow 32B LE 内部序）。
	wantLE, _ := hex.DecodeString("fa4d0a039f977f9aac19037b4e03ad8a0e6675e1695cf61af12ed1f114777e0e")
	if !bytes.Equal(got, wantLE) {
		return fmt.Errorf("velkarhash 主网 KAT 失配: got(le)=%x want(le)=%x", got, wantLE)
	}
	return nil
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}
