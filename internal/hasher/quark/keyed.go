//go:build quark

package quark

import "github.com/scashcc/ntmpool/internal/hasher"

// quarkKeyedHasher 是 quark 的 keyed 适配（hasher.KeyedHasher）。
//
// 为什么需要它：Noctari 走 CN 方言（cryptonote）+ blob 作业管线（internal/cnjob），
// 而 cnjob.Manager 硬依赖 hasher.KeyedHasher（RandomX 家族要 epoch seed 作 key）。
// quark 无 key（PoW = quark_hash(header80)，不掺任何 seed），故本适配【忽略 key】，
// HashKeyed(key, input) 恒等于普通 quark_hash(input)——seed 绝不进入哈希计算。
//
// 复用已验证的 quarkHasher（同一份 cgo sphlib、同一金锚 SelfTest、同一 Hash）：
// 嵌入后自动获得 Name()="quark" 与 SelfTest()（主网块 3600 金锚），本文件只补 HashKeyed。
// 注册进 keyedRegistry（与普通 registry 分离的两张表，同名 "quark" 不冲突）：
// buildNoctariFamily 通过 hasher.GetKeyed("quark") 取到它。
type quarkKeyedHasher struct{ quarkHasher }

var _ hasher.KeyedHasher = quarkKeyedHasher{}

// HashKeyed 忽略 key，返回 quark_hash(input)。Noctari 矿工经 CN 方言收到 blob=
// 80B header hex（seed_hash 缺省/被忽略），滚 nonce@76 后回报的 result 必须逐字节
// 等于池端本函数重算值（cnjob 的 badpow tripwire）。
func (h quarkKeyedHasher) HashKeyed(_ []byte, input []byte) ([]byte, error) {
	return h.quarkHasher.Hash(input)
}

func init() { hasher.RegisterKeyed(quarkKeyedHasher{}) }
