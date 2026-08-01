//go:build randomx

// Package brisviarx Brisvia (BRVA) 池端 RandomX 重算哈希器（"rx/brva"）。
//
// Brisvia = Bitcoin Core v30.2 fork，PoW = stock tevador RandomX（无魔改，= Monero rx/0
// 同一份引擎参数）。故本包链【同一份 stock 库】build-stock/librandomx.a（-DRANDOMX_STOCK，
// 原名符号），与 internal/hasher/randomx(rx/0) 共用一份 archive——同库同符号不串味，
// 无需 dragonx 那样的前缀隔离（dragonx 因是不同配置常量才要 drgrx_ 前缀）。
//
// 单段 PoW（与 dragonx 的双段不同，无外层 double-SHA）：
//
//	pow = RandomX_stock(seed, header80)      ← 视作 256-bit LE 整数，≤ target 即有效
//	（seed = 按高度 2048/64 epoch 取的祖先块 hash 的内部序；由 job manager 计算下发）
//	（header80 = 标准 80 字节比特币区块头，nonce 在 offset 76 LE）
//	（块 hash = SHA256d(header80)，与 pow 无关，仅作区块标识——故不满足 "pow 反转即块hash"）
//
// HashKeyed 返回 rx_hash 原始 32 字节（LE，rx[0]=最低有效字节）；调用方（job manager）
// reverse 后作大端整数比 target，与 Brisvia 节点 RandomXOutputToTargetInteger 同口径。
//
// 共识来源：_knowledge/algorithms/RandomX-brisvia-spec.md（真链金锚已钉死）。
// 构建（CI / 本地 linux，与 rx/0 同一份 build-stock）：
//
//	cmake -S third_party/randomx -B third_party/randomx/build-stock \
//	      -DARCH=default -DCMAKE_BUILD_TYPE=Release \
//	      -DCMAKE_C_FLAGS=-DRANDOMX_STOCK -DCMAKE_CXX_FLAGS=-DRANDOMX_STOCK
//	cmake --build third_party/randomx/build-stock --target randomx -j
//	go test -tags randomx ./internal/hasher/brisviarx/
package brisviarx

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

// keepSeeds 保留最近几个 epoch 的 VM（seed 每 2048 块轮换，窗口内新旧 share 并存）。
const keepSeeds = 2

// vmsPerSeed 每个 seed 建多少个 RandomX VM = PoW 验证的真实并发上限。
//
// ★血泪（2026-08-02 BRVA 主网）：RandomX VM 非线程安全，一个 VM 同时只能算一条
// hash。初版每 seed 只建 1 个 VM + mutex 串行，于是 powVerifyConcurrency 完全是
// 摆设——拿到并发槽的 goroutine 全堵在同一把锁上。32 核生产机实测池进程恒定
// 100% CPU（= 恰好 1 核），share 稍密就排队超时，矿工侧刷屏
// "Server busy (verification queue full)"。当时把 concurrency 4→24、queue 64→1024
// 只是把队列加长，症状缓解而根因未动。
//
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

// Hasher 实现 hasher.KeyedHasher（"rx/brva"）。key = epoch seed（祖先块 hash 内部序）。
type Hasher struct {
	mu      sync.Mutex
	entries map[string]*rxVM
	order   []string
}

var _ hasher.KeyedHasher = (*Hasher)(nil)

func New() *Hasher {
	return &Hasher{entries: map[string]*rxVM{}}
}

func (h *Hasher) Name() string { return "rx/brva" }

// vmFor 取/建该 seed 的 VM；LRU 保留 keepSeeds 个。
func (h *Hasher) vmFor(key []byte) (*rxVM, error) {
	k := string(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.entries[k]; ok {
		return v, nil
	}
	flags := C.randomx_get_flags() // 自动探测 JIT/AES；light 模式（无 FULL_MEM）
	cache := C.randomx_alloc_cache(flags)
	if cache == nil {
		flags = C.RANDOMX_FLAG_DEFAULT
		cache = C.randomx_alloc_cache(flags)
		if cache == nil {
			return nil, fmt.Errorf("rx/brva: alloc_cache 失败")
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
				return nil, fmt.Errorf("rx/brva: create_vm 失败")
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

// HashKeyed RandomX_stock(key=seed, input=header80)。返回 rx_hash 原始 32B（LE）。
// VM 非线程安全 → 从该 seed 的空闲池借一个 VM 独占使用，用完归还；并发度 = vmsPerSeed()。
func (h *Hasher) HashKeyed(key, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("rx/brva: 空输入")
	}
	v, err := h.vmFor(key)
	if err != nil {
		return nil, err
	}
	v.mu.RLock() // 与 destroy 互斥；彼此之间不互斥 → 真并行
	defer v.mu.RUnlock()
	if v.dead {
		return nil, fmt.Errorf("rx/brva: VM 已销毁（seed 已过轮换窗口）")
	}
	vm := <-v.idle
	defer func() { v.idle <- vm }()
	out := make([]byte, 32)
	C.randomx_calculate_hash(vm,
		unsafe.Pointer(&input[0]), C.size_t(len(input)),
		unsafe.Pointer(&out[0]))
	return out, nil
}

// —— 金锚（两层，任一失败拒绝启动）——
//
// 锚1 引擎锚：tevador 官方 RandomX 公开测试向量（与 rx/0、NTMminer rx_kat.c STOCK 段同组）。
//
//	既证引擎正确，也证「链的是 stock 配置」——链错 dragonx 配置库当场失败。
//
// 锚2 真链块锚（终极）：Brisvia testnet 块 #3000（height>2112 用轮换 seed）。
//
//	seed = 块2048 hash 的内部序；header80 = 链上真头 → rx_hash 必须逐字节等于
//	5850U 真机(brisvia_rxpow) + Brisvia 节点验证一致的值。同时钉死 80B 头喂法。
//	(seed epoch 2048/64 规则本身在 job manager 端测；此锚用已知正确 seed 证引擎+喂法。)
const (
	// anchorSeedKeyHex 块2048 hash 的内部序（= reverse(display hash)），作 RandomX key。
	anchorSeedKeyHex = "fa950df7e1ac7600373b662b53d1011916cc7d138169b19e256faa7cab588dc7"
	// anchorHeader80Hex 块3000 的标准 80 字节区块头（version|prev|merkle|time|bits|nonce，全 LE）。
	anchorHeader80Hex = "0000002035487b5f9a101126494b0be6ce35b0ac3dfef9c6d183af38c04bac074b0daa89eabd50bacb3bc6c9e8aee333628840f577518b0325da3139765661a13d1ed2ac8e89516a3f3a521eb8290000"
	// anchorRxHashHex 期望 rx_hash（LE 原始 32B）。作整数(reverse)后 ≤ target(0x1e523a3f) 即链上有效。
	anchorRxHashHex = "b2c436d7c14ed3d7abed4af5716a3b36365d0910d37fbaddb41f40f2e9180000"
)

func (h *Hasher) SelfTest() error {
	// 锚1 引擎（stock 配置）
	vectors := []struct{ key, in, exp string }{
		{"test key 000", "This is a test",
			"639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f"},
		{"test key 000", "Lorem ipsum dolor sit amet",
			"300a0adb47603dedb42228ccb2b211104f4da45af709cd7547cd049e9489c969"},
		{"test key 001", "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			"e9ff4503201c0c2cca26d285c93ae883f9b1d30c9eb240b820756f2d5a7905fc"},
	}
	for i, v := range vectors {
		got, err := h.HashKeyed([]byte(v.key), []byte(v.in))
		if err != nil {
			return fmt.Errorf("rx/brva 锚1[%d]: %w", i, err)
		}
		exp, _ := hex.DecodeString(v.exp)
		if !bytes.Equal(got, exp) {
			return fmt.Errorf("rx/brva 锚1[%d](引擎/stock配置) 失配: got %x want %s（若链成 dragonx 配置库会在此失败）", i, got, v.exp)
		}
	}

	// 锚2 真链块 #3000
	key, _ := hex.DecodeString(anchorSeedKeyHex)
	hdr, _ := hex.DecodeString(anchorHeader80Hex)
	want, _ := hex.DecodeString(anchorRxHashHex)
	if len(hdr) != 80 {
		return fmt.Errorf("rx/brva 锚2: header80 长度 %d ≠ 80", len(hdr))
	}
	got, err := h.HashKeyed(key, hdr)
	if err != nil {
		return fmt.Errorf("rx/brva 锚2(真链块#3000): %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("rx/brva 锚2(真链块#3000) 失配: got %x want %s（怀疑引擎或 80B 头喂法）", got, anchorRxHashHex)
	}
	return nil
}

func init() {
	hasher.RegisterKeyed(New())
}
