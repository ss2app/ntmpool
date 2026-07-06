// Package hasher 定义可插拔的算法哈希器。
//
// 铁律（工厂 CLAUDE.md + pool-core README）：
//  1. share 校验 = 池端重算，Hasher 必须与节点/锄头同源（FFI 引入同一份共识库），
//     禁止自己按 spec 重实现一遍再"对拍"。
//  2. 每个 Hasher 必须自带金锚 test vector；SelfTest 不过，池进程拒绝启动。
//  3. 定制常量的 RandomX 类算法（rx/dragonx、rx/tar）绝不复用别币的库（pitfall C13）。
package hasher

import (
	"fmt"
	"sync"
)

// Hasher 是一个 PoW 算法的池端重算实现。
type Hasher interface {
	// Name 算法名，如 "sha256d"、"rx/0"、"argon2id-64m"。
	Name() string
	// Hash 对完整 header/blob 做共识哈希。实现必须与节点 AcceptBlock 用的函数逐字节一致。
	Hash(input []byte) ([]byte, error)
	// SelfTest 跑内置金锚 test vector（已知 input → 已知 digest 逐字节比对）。
	SelfTest() error
}

var (
	mu       sync.RWMutex
	registry = map[string]Hasher{}
)

// Register 注册一个算法实现（在各实现包的 init 中调用）。
func Register(h Hasher) {
	mu.Lock()
	defer mu.Unlock()
	registry[h.Name()] = h
}

// Get 按算法名取哈希器。
func Get(name string) (Hasher, error) {
	mu.RLock()
	defer mu.RUnlock()
	h, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("hasher %q 未注册", name)
	}
	return h, nil
}

// SelfTestAll 对指定算法跑金锚自检；启动门禁，任何一个失败都返回错误。
func SelfTestAll(names []string) error {
	for _, n := range names {
		h, err := Get(n)
		if err != nil {
			return err
		}
		if err := h.SelfTest(); err != nil {
			return fmt.Errorf("hasher %s 金锚自检失败（拒绝启动）: %w", n, err)
		}
	}
	return nil
}
