// Package btcwork bitcoin 系挖矿工作构造：目标/难度换算、coinbase、merkle、
// 块组装、stratum V1 字节序编码。
//
// 字节序约定（全包统一，血泪重灾区）：
//   - 「BE hex」= RPC/浏览器显示序（如 getblockhash 返回值）；
//   - 「内部序」= 80 字节 header 里的实际字节序（BE hex 的整体反转）；
//   - sha256d 输出天然是内部序。
//
// stratum notify 的 prevhash 用 NOMP 参考实现的字序（8 个 uint32 BE→LE 再整体反转），
// 与 cgminer/cpuminer 十年互操作验证过的口径一致。
package btcwork

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
)

// Diff1Target 池难度 1 对应的目标（bdiff 口径，bitcoin 系 stratum 事实标准）。
var Diff1Target = mustBig("00000000ffff0000000000000000000000000000000000000000000000000000")

func mustBig(hexStr string) *big.Int {
	n, ok := new(big.Int).SetString(hexStr, 16)
	if !ok {
		panic("bad hex const")
	}
	return n
}

func sha256d(b []byte) []byte {
	h1 := sha256.Sum256(b)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// Reverse 返回反转副本（BE hex 字节 ↔ 内部序）。
func Reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

// DiffToTarget 份额难度 → 目标（target = Diff1 / diff）。
func DiffToTarget(diff float64) *big.Int {
	if diff <= 0 {
		diff = 1e-9
	}
	t := new(big.Float).SetPrec(256).SetInt(Diff1Target)
	t.Quo(t, big.NewFloat(diff))
	out, _ := t.Int(nil)
	return out
}

// HashValue hash（内部序，sha256d 输出）→ 数值（比较用大端语义 = 反转后 SetBytes）。
func HashValue(hash []byte) *big.Int {
	return new(big.Int).SetBytes(Reverse(hash))
}

// HashMeetsTarget hash ≤ target。
func HashMeetsTarget(hash []byte, target *big.Int) bool {
	return HashValue(hash).Cmp(target) <= 0
}

// ShareDiff 该 hash 实际达到的份额难度（vardiff 计权用）。
func ShareDiff(hash []byte) float64 {
	v := HashValue(hash)
	if v.Sign() == 0 {
		return 0
	}
	f := new(big.Float).SetPrec(256).SetInt(Diff1Target)
	f.Quo(f, new(big.Float).SetPrec(256).SetInt(v))
	out, _ := f.Float64()
	return out
}

// TargetFromHex GBT 的 target 字段（BE hex）→ big.Int。
func TargetFromHex(beHex string) (*big.Int, error) {
	b, err := hex.DecodeString(beHex)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// ---- stratum V1 编码 ----

// PrevHashStratum BE hex 的 prevhash → notify 用的字序（NOMP reverseByteOrder：
// 8 个 uint32 逐字换端序后整体反转）。
func PrevHashStratum(beHex string) (string, error) {
	b, err := hex.DecodeString(beHex)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("prevhash 非 32 字节: %q", beHex)
	}
	out := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint32(out[i*4:], binary.BigEndian.Uint32(b[i*4:]))
	}
	return hex.EncodeToString(Reverse(out)), nil
}

// PrevHashInternalFromStratum notify 字序 → 内部序（池端重建 header 用，与矿工解码对偶）。
func PrevHashInternalFromStratum(stratumHex string) ([]byte, error) {
	b, err := hex.DecodeString(stratumHex)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("stratum prevhash 非 32 字节")
	}
	r := Reverse(b)
	out := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(out[i*4:], binary.LittleEndian.Uint32(r[i*4:]))
	}
	// 内部序 = BE hex 整体反转
	return Reverse(out), nil
}

// ---- varint / script 工具 ----

func varInt(n uint64) []byte {
	switch {
	case n < 0xfd:
		return []byte{byte(n)}
	case n <= 0xffff:
		b := make([]byte, 3)
		b[0] = 0xfd
		binary.LittleEndian.PutUint16(b[1:], uint16(n))
		return b
	case n <= 0xffffffff:
		b := make([]byte, 5)
		b[0] = 0xfe
		binary.LittleEndian.PutUint32(b[1:], uint32(n))
		return b
	default:
		b := make([]byte, 9)
		b[0] = 0xff
		binary.LittleEndian.PutUint64(b[1:], n)
		return b
	}
}

// scriptPushNum BIP34 高度 push（最小编码 CScriptNum + 前置长度字节）。
func scriptPushNum(n uint64) []byte {
	if n == 0 {
		return []byte{0x00} // OP_0
	}
	var payload []byte
	for v := n; v > 0; v >>= 8 {
		payload = append(payload, byte(v&0xff))
	}
	// 最高位为 1 时补 0x00 防止被解释为负数
	if payload[len(payload)-1]&0x80 != 0 {
		payload = append(payload, 0x00)
	}
	return append([]byte{byte(len(payload))}, payload...)
}

// ---- coinbase 构造 ----

// Coinbase 预构造的 coinbase 交易，scriptSig 中留出 extranonce 空位。
// 最终交易 = Coinb1 || extranonce1 || extranonce2 || Coinb2（非见证序列化，算 txid 用）。
type Coinbase struct {
	Coinb1        []byte
	Coinb2        []byte
	ExtraNonceLen int  // extranonce1+extranonce2 总字节数（空位大小）
	HasWitness    bool // 是否包含见证承诺（决定块内序列化形态）
}

// BuildCoinbase 组 coinbase。
//   - height: BIP34 高度（scriptSig 第一个 push）
//   - valueSat: coinbase 总额（聪）。铁律：空块只 claim subsidy（pitfall C5），调用方负责给对值。
//   - payoutScript: 矿池地址的 scriptPubKey（validateaddress 取，不自己解地址）
//   - witnessCommitment: GBT default_witness_commitment 的 script hex 解码；nil = 无
//   - tag: 池标记（如 "/NTMPool/"），进 scriptSig 尾部
//   - extraNonceLen: extranonce1+extranonce2 总字节数（stratum subscribe 时约定）
func BuildCoinbase(height uint64, valueSat int64, payoutScript, witnessCommitment, tag []byte, extraNonceLen int) (*Coinbase, error) {
	heightPush := scriptPushNum(height)
	scriptLen := len(heightPush) + extraNonceLen + len(tag)
	if scriptLen > 100 {
		return nil, fmt.Errorf("coinbase scriptSig %d 字节超限(100)", scriptLen)
	}

	var c1 []byte
	c1 = append(c1, 0x02, 0x00, 0x00, 0x00) // version=2 LE
	c1 = append(c1, 0x01)                   // 输入数 1
	c1 = append(c1, make([]byte, 32)...)    // prevout hash = 0
	c1 = append(c1, 0xff, 0xff, 0xff, 0xff) // prevout index
	c1 = append(c1, byte(scriptLen))        // scriptSig 长度
	c1 = append(c1, heightPush...)          // BIP34 高度
	// —— extranonce 空位在此 ——

	var c2 []byte
	c2 = append(c2, tag...)
	c2 = append(c2, 0xff, 0xff, 0xff, 0xff) // sequence
	// 输出
	nOut := 1
	if len(witnessCommitment) > 0 {
		nOut = 2
	}
	c2 = append(c2, byte(nOut))
	val := make([]byte, 8)
	binary.LittleEndian.PutUint64(val, uint64(valueSat))
	c2 = append(c2, val...)
	c2 = append(c2, varInt(uint64(len(payoutScript)))...)
	c2 = append(c2, payoutScript...)
	if len(witnessCommitment) > 0 {
		c2 = append(c2, make([]byte, 8)...) // value 0
		c2 = append(c2, varInt(uint64(len(witnessCommitment)))...)
		c2 = append(c2, witnessCommitment...)
	}
	c2 = append(c2, 0x00, 0x00, 0x00, 0x00) // locktime

	return &Coinbase{Coinb1: c1, Coinb2: c2, ExtraNonceLen: extraNonceLen, HasWitness: len(witnessCommitment) > 0}, nil
}

// Serialize 非见证序列化（txid = sha256d(此结果)）。en1+en2 长度必须等于空位。
func (c *Coinbase) Serialize(en1, en2 []byte) ([]byte, error) {
	if len(en1)+len(en2) != c.ExtraNonceLen {
		return nil, fmt.Errorf("extranonce 长度 %d+%d != 空位 %d", len(en1), len(en2), c.ExtraNonceLen)
	}
	out := make([]byte, 0, len(c.Coinb1)+c.ExtraNonceLen+len(c.Coinb2))
	out = append(out, c.Coinb1...)
	out = append(out, en1...)
	out = append(out, en2...)
	out = append(out, c.Coinb2...)
	return out, nil
}

// TxID coinbase txid（内部序）。
func (c *Coinbase) TxID(en1, en2 []byte) ([]byte, error) {
	raw, err := c.Serialize(en1, en2)
	if err != nil {
		return nil, err
	}
	return sha256d(raw), nil
}

// SerializeWitness 见证序列化（进块用）：含 marker/flag 与 32 字节保留见证。
func (c *Coinbase) SerializeWitness(en1, en2 []byte) ([]byte, error) {
	raw, err := c.Serialize(en1, en2)
	if err != nil {
		return nil, err
	}
	if !c.HasWitness {
		return raw, nil
	}
	// 在 version(4) 后插 marker+flag；locktime(尾 4 字节) 前插 witness
	out := make([]byte, 0, len(raw)+2+34)
	out = append(out, raw[:4]...)
	out = append(out, 0x00, 0x01) // marker, flag
	out = append(out, raw[4:len(raw)-4]...)
	out = append(out, 0x01, 0x20)          // 1 个见证项, 32 字节
	out = append(out, make([]byte, 32)...) // 见证保留值（与承诺计算约定一致，全零）
	out = append(out, raw[len(raw)-4:]...) // locktime
	return out, nil
}

// ---- merkle ----

// MerkleBranch 给定块内其余交易的 txid（内部序），算 coinbase-first 的 merkle 分支
// （stratum notify 的 merkle_branch；矿工/池用 MerkleRootFromBranch 折叠）。
func MerkleBranch(txids [][]byte) [][]byte {
	var branch [][]byte
	level := txids
	for {
		if len(level) == 0 {
			break
		}
		branch = append(branch, level[0])
		if len(level) == 1 {
			break
		}
		rest := level[1:]
		var next [][]byte
		for i := 0; i < len(rest); i += 2 {
			a := rest[i]
			b := a
			if i+1 < len(rest) {
				b = rest[i+1]
			}
			next = append(next, sha256d(append(append([]byte{}, a...), b...)))
		}
		level = next
	}
	return branch
}

// MerkleRootFromBranch coinbase txid 与分支折叠出 merkle root（内部序）。
func MerkleRootFromBranch(cbTxid []byte, branch [][]byte) []byte {
	root := cbTxid
	for _, node := range branch {
		root = sha256d(append(append([]byte{}, root...), node...))
	}
	return root
}

// ---- header / block ----

// SerializeHeader 组 80 字节 header（全部内部序/LE）。
// prevHashInternal、merkleRoot 均为内部序 32 字节。
func SerializeHeader(version uint32, prevHashInternal, merkleRoot []byte, ntime, bits, nonce uint32) []byte {
	h := make([]byte, 0, 80)
	var w [4]byte
	binary.LittleEndian.PutUint32(w[:], version)
	h = append(h, w[:]...)
	h = append(h, prevHashInternal...)
	h = append(h, merkleRoot...)
	binary.LittleEndian.PutUint32(w[:], ntime)
	h = append(h, w[:]...)
	binary.LittleEndian.PutUint32(w[:], bits)
	h = append(h, w[:]...)
	binary.LittleEndian.PutUint32(w[:], nonce)
	h = append(h, w[:]...)
	return h
}

// HeaderHash header 的 sha256d（内部序）。BE 显示 = hex(Reverse(此值))。
func HeaderHash(header []byte) []byte { return sha256d(header) }

// AssembleBlock 完整块 hex：header + varint(交易数) + coinbase(见证序列化) + 其余原始交易。
func AssembleBlock(header, coinbaseWitness []byte, rawTxs [][]byte) string {
	out := make([]byte, 0, 81+len(coinbaseWitness))
	out = append(out, header...)
	out = append(out, varInt(uint64(1+len(rawTxs)))...)
	out = append(out, coinbaseWitness...)
	for _, tx := range rawTxs {
		out = append(out, tx...)
	}
	return hex.EncodeToString(out)
}
