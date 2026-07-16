// Package scashrx 定义 SCASH 专用 PoW 接口。
//
// R 是 salted RandomX 输出，只能写回 solved header；CM 是 BLAKE2b-256(zeroed112||R)，
// share 与 block 只能比较 CM。本包刻意不实现 hasher.KeyedHasher / TwoStageKeyedHasher，
// 防止通用调用方把 R 当成可接受 share 的第二条路径。
package scashrx

import (
	"encoding/hex"
	"fmt"
	"sync"

	"golang.org/x/crypto/blake2b"
)

const (
	Algorithm  = "rx/scash"
	HeaderSize = 112
	RXOffset   = 80
	Salt       = "RandomX-Scash\x01"
)

// Result 将可持久化的 RandomX 输出与唯一 PoW digest 明确分开。
type Result struct {
	R  [32]byte
	CM [32]byte
}

// Engine 是 scashjob 唯一允许依赖的 SCASH PoW 面。
type Engine interface {
	Name() string
	Hash(key, zeroedHeader []byte) (Result, error)
	SelfTest() error
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Engine{}
)

func Register(e Engine) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[e.Name()] = e
}

func Get(name string) (Engine, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("SCASH hasher %q 未注册（生产构建需要 -tags randomx）", name)
	}
	return e, nil
}

// Commitment 是 RandomX v1.2.1 commitment 的独立、无状态等价表达。
// Engine.Hash 仍会调用 vendored RandomX runtime 的正式 commitment API；此函数用于
// 默认（非 cgo）构建中的 KAT，以及 adapter/job 的纯字节测试。
func Commitment(input []byte, r [32]byte) [32]byte {
	buf := make([]byte, 0, len(input)+len(r))
	buf = append(buf, input...)
	buf = append(buf, r[:]...)
	return blake2b.Sum256(buf)
}

// IsZeroedHeader 检查共识输入严格为 112B 且尾部 R 区全零。
func IsZeroedHeader(header []byte) bool {
	if len(header) != HeaderSize {
		return false
	}
	for _, b := range header[RXOffset:] {
		if b != 0 {
			return false
		}
	}
	return true
}

// DisplayHex 把 Bitcoin uint256 内部原始字节转成 RPC/display hex。
func DisplayHex(raw []byte) string {
	rev := make([]byte, len(raw))
	for i := range raw {
		rev[len(raw)-1-i] = raw[i]
	}
	return hex.EncodeToString(rev)
}
