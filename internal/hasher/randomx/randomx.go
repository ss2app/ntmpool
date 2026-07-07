//go:build randomx

// Package randomx RandomX 池端重算哈希器（cgo 链 vendored libRandomX）。
//
// 同源铁律：third_party/randomx 与 NTMminer 的 vendored 库是【同一份源码】
// （BSD，含 NTMminer 扩展 randomx_init_cache_salted）——矿工、矿池用同一份
// 共识引擎，跨实现逐字节一致由源头保证。
//
// 本包只注册 stock 配置 = "rx/0"（Monero 标准参数，zoka/门罗系用）。
// 构建要求（CI ci.yml / 本地 linux）：
//
//	cmake -S third_party/randomx -B third_party/randomx/build-stock \
//	      -DARCH=default -DCMAKE_BUILD_TYPE=Release \
//	      -DCMAKE_C_FLAGS=-DRANDOMX_STOCK -DCMAKE_CXX_FLAGS=-DRANDOMX_STOCK
//	cmake --build third_party/randomx/build-stock --target randomx -j
//	go test -tags randomx ./...
//
// ⚠ vendored configuration.h 默认是 dragonx 常量，-DRANDOMX_STOCK 才是 rx/0；
// 链错构建 = 挖废块，SelfTest 的官方向量金锚会当场拦下（启动门禁）。
// rx/dragonx 变体要第二份库 + 符号前缀隔离（NTMminer zkrx_ 先例），M3-6 迁移时加。
//
// 模式：light 模式（cache-only，~毫秒级/hash）。池端验 share 是低频操作，
// 不需要矿工的 full-dataset 模式；省 2GiB 内存。
package randomx

/*
#cgo CFLAGS: -I${SRCDIR}/../../../third_party/randomx/src
#cgo LDFLAGS: ${SRCDIR}/../../../third_party/randomx/build-stock/librandomx.a -lstdc++ -lm
#include <stdlib.h>
#include "randomx.h"
*/
import "C"

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sync"
	"unsafe"

	"github.com/scashcc/ntmpool/internal/hasher"
)

// keepSeeds 保留最近几个 epoch 的 VM（seed 轮换窗口内新旧 share 并存）。
const keepSeeds = 2

type rxVM struct {
	mu    sync.Mutex
	cache *C.randomx_cache
	vm    *C.randomx_vm
}

func (v *rxVM) destroy() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vm != nil {
		C.randomx_destroy_vm(v.vm)
		v.vm = nil
	}
	if v.cache != nil {
		C.randomx_release_cache(v.cache)
		v.cache = nil
	}
}

// Hasher 实现 hasher.KeyedHasher（"rx/0"）。key = epoch seed（seed_hash 的字节）。
type Hasher struct {
	mu      sync.Mutex
	entries map[string]*rxVM
	order   []string
}

var _ hasher.KeyedHasher = (*Hasher)(nil)

func New() *Hasher {
	return &Hasher{entries: map[string]*rxVM{}}
}

func (h *Hasher) Name() string { return "rx/0" }

// vmFor 取/建该 seed 的 VM；LRU 保留 keepSeeds 个。
func (h *Hasher) vmFor(key []byte) (*rxVM, error) {
	k := string(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.entries[k]; ok {
		return v, nil
	}
	flags := C.randomx_get_flags() // 自动探测 JIT/AES/SSSE3/AVX2；light 模式（无 FULL_MEM）
	cache := C.randomx_alloc_cache(flags)
	if cache == nil {
		// JIT/大页等环境受限：退最保守 flags 再试
		flags = C.RANDOMX_FLAG_DEFAULT
		cache = C.randomx_alloc_cache(flags)
		if cache == nil {
			return nil, fmt.Errorf("randomx: alloc_cache 失败")
		}
	}
	var kp unsafe.Pointer
	if len(key) > 0 {
		kp = unsafe.Pointer(&key[0])
	}
	C.randomx_init_cache(cache, kp, C.size_t(len(key)))
	vm := C.randomx_create_vm(flags, cache, nil)
	if vm == nil {
		C.randomx_release_cache(cache)
		return nil, fmt.Errorf("randomx: create_vm 失败")
	}
	v := &rxVM{cache: cache, vm: vm}
	h.entries[k] = v
	h.order = append(h.order, k)
	for len(h.order) > keepSeeds {
		old := h.order[0]
		h.order = h.order[1:]
		if ov, ok := h.entries[old]; ok {
			delete(h.entries, old)
			go ov.destroy() // 可能有在途 hash 持锁，异步等锁销毁
		}
	}
	return v, nil
}

// HashKeyed RandomX(key=seed, input=blob)。VM 非线程安全，每 seed 一把锁串行。
func (h *Hasher) HashKeyed(key, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("randomx: 空输入")
	}
	v, err := h.vmFor(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 32)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vm == nil {
		return nil, fmt.Errorf("randomx: VM 已销毁（seed 已过轮换窗口）")
	}
	C.randomx_calculate_hash(v.vm,
		unsafe.Pointer(&input[0]), C.size_t(len(input)),
		unsafe.Pointer(&out[0]))
	return out, nil
}

// SelfTest 官方 RandomX 公开测试向量（与 NTMminer rx_kat.c STOCK 段同一组）。
// 这既锚引擎正确性，也锚「链的是 stock 配置」——链错 dragonx 配置库当场失败。
func (h *Hasher) SelfTest() error {
	vectors := []struct{ key, in, exp string }{
		{"test key 000", "This is a test",
			"639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f"},
		{"test key 000", "Lorem ipsum dolor sit amet",
			"300a0adb47603dedb42228ccb2b211104f4da45af709cd7547cd049e9489c969"},
		{"test key 000", "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			"c36d4ed4191e617309867ed66a443be4075014e2b061bcdaf9ce7b721d2b77a8"},
		{"test key 001", "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			"e9ff4503201c0c2cca26d285c93ae883f9b1d30c9eb240b820756f2d5a7905fc"},
	}
	for i, v := range vectors {
		got, err := h.HashKeyed([]byte(v.key), []byte(v.in))
		if err != nil {
			return fmt.Errorf("rx/0 金锚[%d]: %w", i, err)
		}
		exp, _ := hex.DecodeString(v.exp)
		if !bytes.Equal(got, exp) {
			return fmt.Errorf("rx/0 金锚[%d] 失配: got %x want %s", i, got, v.exp)
		}
	}
	return nil
}

func init() {
	hasher.RegisterKeyed(New())
}
