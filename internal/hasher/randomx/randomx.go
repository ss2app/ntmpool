//go:build randomx

// Package randomx RandomX 池端重算哈希器（cgo 链 vendored libRandomX）。
//
// 同源铁律：third_party/randomx 与 NTMminer 的 vendored 库是【同一份源码】
// （BSD，含 NTMminer 扩展 randomx_init_cache_salted）——矿工、矿池用同一份
// 共识引擎，跨实现逐字节一致由源头保证。
//
// 本包只链 stock 配置库（Monero 标准参数），注册两个名字：
//   - "rx/0"    zoka/门罗系
//   - "rx/juno" Juno Cash（Zcash 系 140B 头 + 标准 RandomX；算法名不同但引擎
//     参数与 rx/0 逐项相同——junorig RxAlgo.cpp 里 RX_JUNO 落 default 分支即
//     MoneroConfig，节点 vendored tevador 库 salt 也是标准 "RandomX\x03"）
//
// 两个名字各自独立实例（独立 seed→VM 缓存），互不串状态。
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
// rx/dragonx 变体要第二份库 + 符号前缀隔离（NTMminer zkrx_ 先例）。
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
	"os"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/scashcc/ntmpool/internal/hasher"
)

// keepSeeds 保留最近几个 epoch 的 VM（seed 轮换窗口内新旧 share 并存）。
const keepSeeds = 2

// vmsPerSeed 每个 seed 建多少个 RandomX VM = PoW 验证的真实并发上限。
//
// ★血泪（2026-08-02 BRVA 主网，rx/brva 同款修复）：RandomX VM 非线程安全，一个 VM
// 同时只能算一条 hash。旧版每 seed 只建 1 个 VM + mutex 串行，powVerifyConcurrency
// 完全是摆设——拿到并发槽的 goroutine 全堵在同一把锁上，多核机恒定 1 核 100%。
// cache 是只读的，可被多个 VM 共享；官方多线程用法就是「1 cache + 每线程 1 VM」，
// 每个 light-mode VM 仅额外占 ~2MB scratchpad。
func vmsPerSeed() int {
	if s := os.Getenv("NTM_RX_VMS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 128 {
			return n
		}
	}
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	if n > 32 {
		n = 32
	}
	return n
}

type rxVM struct {
	// mu RLock=计算中（多路并发）；Lock=销毁（独占，等所有在途算完）。
	mu    sync.RWMutex
	dead  bool
	cache *C.randomx_cache
	idle  chan *C.randomx_vm // 空闲 VM 池：取用—归还，保证一个 VM 同时只被一方持有
	all   []*C.randomx_vm    // 全量句柄，仅 destroy 用
}

func (v *rxVM) destroy() {
	v.mu.Lock() // 等所有在途 hash 结束，避免销毁正在跑的 VM
	defer v.mu.Unlock()
	if v.dead {
		return
	}
	v.dead = true
	for _, vm := range v.all {
		C.randomx_destroy_vm(vm)
	}
	v.all = nil
	if v.cache != nil {
		C.randomx_release_cache(v.cache)
		v.cache = nil
	}
}

// Hasher 实现 hasher.KeyedHasher。key = epoch seed（seed_hash 的字节）。
// name 决定注册名（"rx/0" / "rx/juno"），引擎与参数完全相同。
type Hasher struct {
	name    string
	mu      sync.Mutex
	entries map[string]*rxVM
	order   []string
}

var _ hasher.KeyedHasher = (*Hasher)(nil)

func New() *Hasher {
	return NewNamed("rx/0")
}

// NewNamed 同一 stock 引擎挂别的算法名（如 "rx/juno"）。实例间状态完全独立。
func NewNamed(name string) *Hasher {
	return &Hasher{name: name, entries: map[string]*rxVM{}}
}

func (h *Hasher) Name() string { return h.name }

// vmFor 取/建该 seed 的 VM 组；LRU 保留 keepSeeds 个 seed。
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
			return nil, fmt.Errorf("%s: alloc_cache 失败", h.name)
		}
	}
	var kp unsafe.Pointer
	if len(key) > 0 {
		kp = unsafe.Pointer(&key[0])
	}
	C.randomx_init_cache(cache, kp, C.size_t(len(key)))
	n := vmsPerSeed()
	v := &rxVM{cache: cache, idle: make(chan *C.randomx_vm, n)}
	for i := 0; i < n; i++ {
		vm := C.randomx_create_vm(flags, cache, nil)
		if vm == nil {
			if len(v.all) == 0 {
				C.randomx_release_cache(cache)
				return nil, fmt.Errorf("%s: create_vm 失败", h.name)
			}
			break // 已建出至少一个：内存紧张时降级带伤跑，不整池挂掉
		}
		v.all = append(v.all, vm)
		v.idle <- vm
	}
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

// HashKeyed RandomX(key=seed, input=blob)。返回 rx_hash 原始 32B（LE）。
// VM 非线程安全 → 从该 seed 的空闲池借一个 VM 独占使用，用完归还；并发度 = vmsPerSeed()。
func (h *Hasher) HashKeyed(key, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%s: 空输入", h.name)
	}
	v, err := h.vmFor(key)
	if err != nil {
		return nil, err
	}
	v.mu.RLock() // 与 destroy 互斥；彼此之间不互斥 → 真并行
	defer v.mu.RUnlock()
	if v.dead {
		return nil, fmt.Errorf("%s: VM 已销毁（seed 已过轮换窗口）", h.name)
	}
	vm := <-v.idle
	defer func() { v.idle <- vm }()
	out := make([]byte, 32)
	C.randomx_calculate_hash(vm,
		unsafe.Pointer(&input[0]), C.size_t(len(input)),
		unsafe.Pointer(&out[0]))
	return out, nil
}

// SelfTest 官方 RandomX 公开测试向量（与 NTMminer rx_kat.c STOCK 段同一组）。
// 这既锚引擎正确性，也锚「链的是 stock 配置」——链错 dragonx 配置库当场失败。
// rx/0 与 rx/juno 两实例各自跑（同引擎同向量；rx/juno 的真链块锚在主网节点
// 同步后补进部署验证，见 coins/junocash/PLAN-矿池.md WP4）。
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
			return fmt.Errorf("%s 金锚[%d]: %w", h.name, i, err)
		}
		exp, _ := hex.DecodeString(v.exp)
		if !bytes.Equal(got, exp) {
			return fmt.Errorf("%s 金锚[%d] 失配: got %x want %s", h.name, i, got, v.exp)
		}
	}
	return nil
}

func init() {
	hasher.RegisterKeyed(New())
	hasher.RegisterKeyed(NewNamed("rx/juno"))
}
