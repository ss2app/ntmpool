//go:build randomx

// Package dragonxrx rx/dragonx 池端重算哈希器（cgo 链 drgrx_ 前缀的第二份 libRandomX）。
//
// 同源铁律：third_party/randomx 与 NTMminer vendored 库是同一份源码；本包链的是
// 【默认配置 = dragonx 常数】的构建（不加 -DRANDOMX_STOCK，与 rx/0 恰好相反），
// 5 个共识差异：ARGON_SALT "RandomXHUSH\x03"、ARGON_ITERATIONS 5、PROGRAM_SIZE 512、
// PROGRAM_ITERATIONS 4096、PROGRAM_COUNT 16。
//
// 符号隔离：build-drg 的 librandomx.a 经 scripts/drgrx-prefix.sh 变成 librandomx_drg.o
// （全部全局符号加 drgrx_ 前缀 + COMDAT 打散），与 rx/0 直链的 build-stock 原名符号
// 同二进制共存、互不串味（NTMminer zkrx_ 先例；串味 = 挖废块，SelfTest 金锚拦截）。
//
// 双段 PoW（drg-xmrig PROTOCOL.md / NTMminer rx_dragonx.c / 魔改 miningcore 三源一致）：
//
//	result = RandomX_dragonx(seed, blob[0:140])            ← 矿工上报、badpow 比对
//	pow    = SHA256d(blob[0:140] || 0x20 || result[0:32])  ← 难度/块判定；反转即块 hash
//
// 构建（CI ci.yml）：
//
//	cmake -S third_party/randomx -B third_party/randomx/build-drg \
//	      -DARCH=default -DCMAKE_BUILD_TYPE=Release -DCMAKE_POSITION_INDEPENDENT_CODE=ON
//	cmake --build third_party/randomx/build-drg --target randomx -j
//	sh scripts/drgrx-prefix.sh third_party/randomx/build-drg/librandomx.a \
//	                           third_party/randomx/build-drg/librandomx_drg.o
//	go test -tags randomx ./...
//
// 模式：light（cache-only）。池端验 share 低频，省 2GiB dataset。
package dragonxrx

/*
#cgo CFLAGS: -I${SRCDIR}/../../../third_party/randomx/src
#cgo LDFLAGS: ${SRCDIR}/../../../third_party/randomx/build-drg/librandomx_drg.o -lstdc++ -lm
#include <stdlib.h>
#include "randomx.h"

// drgrx_ 前缀 API 原型。类型全部取自 randomx.h（原名 API 只声明不调用，无链接需求）。
randomx_cache *drgrx_randomx_alloc_cache(randomx_flags flags);
void drgrx_randomx_init_cache(randomx_cache *cache, const void *key, size_t keySize);
void drgrx_randomx_release_cache(randomx_cache *cache);
randomx_vm *drgrx_randomx_create_vm(randomx_flags flags, randomx_cache *cache, randomx_dataset *dataset);
void drgrx_randomx_destroy_vm(randomx_vm *machine);
void drgrx_randomx_calculate_hash(randomx_vm *machine, const void *input, size_t inputSize, void *output);
randomx_flags drgrx_randomx_get_flags(void);
*/
import "C"

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/scashcc/ntmpool/internal/hasher"
)

const (
	// BlobLen dragonx hashing blob 固定长度（108B 头 + 32B nonce 字段）。
	BlobLen = 140
	// FullHeaderLen 完整头 = blob || 0x20 || rx_hash。sha256d 输入；反转即块 hash。
	FullHeaderLen = BlobLen + 1 + 32
	// solutionPreamble compactSize(32)：nSolution 长度前缀。
	solutionPreamble = 0x20
	// keepSeeds 保留最近几个 epoch 的 VM（seed 轮换窗口内新旧 share 并存）。
	keepSeeds = 2
)

// vmsPerSeed 每个 seed 建多少个 RandomX VM = PoW 验证的真实并发上限。
//
// ★血泪（2026-08-02 BRVA 主网，rx/brva 同源同款 bug）：RandomX VM 非线程安全，
// 一个 VM 同时只能算一条 hash。初版每 seed 只建 1 个 VM + mutex 串行，于是
// powVerifyConcurrency 完全是摆设——拿到并发槽的 goroutine 全堵在同一把锁上。
// 32 核生产机实测池进程恒定 100% CPU（= 恰好 1 核），share 稍密就排队超时，
// 矿工侧刷屏 "Server busy (verification queue full)"。当时把 concurrency 4→24、
// queue 64→1024 只是把队列加长，症状缓解而根因未动。
// 5850U 同二进制对拍：vms=1 → 51.8 verify/s，vms=16 → 527.3 verify/s（10.2x）。
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
		C.drgrx_randomx_destroy_vm(vm)
	}
	v.all = nil
	if v.cache != nil {
		C.drgrx_randomx_release_cache(v.cache)
		v.cache = nil
	}
}

// Hasher 实现 hasher.TwoStageKeyedHasher（"rx/dragonx"）。key = seed 块 hash 的内部序 32B。
type Hasher struct {
	mu      sync.Mutex
	entries map[string]*rxVM
	order   []string
}

var (
	_ hasher.KeyedHasher         = (*Hasher)(nil)
	_ hasher.TwoStageKeyedHasher = (*Hasher)(nil)
)

func New() *Hasher {
	return &Hasher{entries: map[string]*rxVM{}}
}

func (h *Hasher) Name() string { return "rx/dragonx" }

// vmFor 取/建该 seed 的 VM；LRU 保留 keepSeeds 个。
func (h *Hasher) vmFor(key []byte) (*rxVM, error) {
	k := string(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.entries[k]; ok {
		return v, nil
	}
	flags := C.drgrx_randomx_get_flags() // 自动探测 JIT/AES；light 模式（无 FULL_MEM）
	cache := C.drgrx_randomx_alloc_cache(flags)
	if cache == nil {
		flags = C.RANDOMX_FLAG_DEFAULT
		cache = C.drgrx_randomx_alloc_cache(flags)
		if cache == nil {
			return nil, fmt.Errorf("rx/dragonx: alloc_cache 失败")
		}
	}
	var kp unsafe.Pointer
	if len(key) > 0 {
		kp = unsafe.Pointer(&key[0])
	}
	C.drgrx_randomx_init_cache(cache, kp, C.size_t(len(key)))
	n := vmsPerSeed()
	v := &rxVM{cache: cache, idle: make(chan *C.randomx_vm, n)}
	for i := 0; i < n; i++ {
		vm := C.drgrx_randomx_create_vm(flags, cache, nil)
		if vm == nil {
			if len(v.all) == 0 {
				C.drgrx_randomx_release_cache(cache)
				return nil, fmt.Errorf("rx/dragonx: create_vm 失败")
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

// rxHash 内层 RandomX(seed=key, input)。
// VM 非线程安全 → 从该 seed 的空闲池借一个 VM 独占使用，用完归还；并发度 = vmsPerSeed()。
func (h *Hasher) rxHash(key, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("rx/dragonx: 空输入")
	}
	v, err := h.vmFor(key)
	if err != nil {
		return nil, err
	}
	v.mu.RLock() // 与 destroy 互斥；彼此之间不互斥 → 真并行
	defer v.mu.RUnlock()
	if v.dead {
		return nil, fmt.Errorf("rx/dragonx: VM 已销毁（seed 已过轮换窗口）")
	}
	vm := <-v.idle
	defer func() { v.idle <- vm }()
	out := make([]byte, 32)
	C.drgrx_randomx_calculate_hash(vm,
		unsafe.Pointer(&input[0]), C.size_t(len(input)),
		unsafe.Pointer(&out[0]))
	return out, nil
}

// powFromRx 外层拼装 + sha256d（纯函数，独立锚可测）：
// pow = SHA256d(blob[0:140] || 0x20 || rxh[0:32])。
func powFromRx(blob, rxh []byte) []byte {
	full := make([]byte, FullHeaderLen)
	copy(full, blob)
	full[BlobLen] = solutionPreamble
	copy(full[BlobLen+1:], rxh)
	d1 := sha256.Sum256(full)
	d2 := sha256.Sum256(d1[:])
	return d2[:]
}

// HashKeyedTwoStage 双段：result=内层 rx_hash，pow=外层 sha256d(173B)。
func (h *Hasher) HashKeyedTwoStage(key, input []byte) (result, pow []byte, err error) {
	if len(input) != BlobLen {
		return nil, nil, fmt.Errorf("rx/dragonx: blob 长度 %d ≠ %d", len(input), BlobLen)
	}
	result, err = h.rxHash(key, input)
	if err != nil {
		return nil, nil, err
	}
	return result, powFromRx(input, result), nil
}

// HashKeyed 返回最终 PoW hash（外层 sha256d）——难度/块判定口径，反转即块 hash。
func (h *Hasher) HashKeyed(key, input []byte) ([]byte, error) {
	_, pow, err := h.HashKeyedTwoStage(key, input)
	return pow, err
}

// —— 金锚（三层，任何一层失败拒绝启动）——
//
// 锚1 引擎合成锚：与 NTMminer rx_kat.c dragonx 段同组（经真池 share accepted
//
//	跨实现背书后钉死的回归锚）。
//
// 锚2 外层拼装锚：合成 rx_hash 的 173B sha256d，独立 hashlib 预言机生成——
//
//	单测拼装与字节序，不依赖 RandomX。
//
// 锚3 真链块锚（终极）：主网块 #3131000 的 140B 头 + ruleA(interval1024/lag64)
//
//	seed → result 必须等于链上 solution、pow 反转必须等于链上块 hash。
//	这一锚同时钉死 seed epoch 规则（若失败优先怀疑 epoch 规则而非引擎）。
const (
	// anchorFull346 块 #3131000 完整 173B 头 hex（2026-07-10 节点 getblock raw 实取，
	// 本地 sha256d 已验 == 块 hash）。唯一权威串：blob=前 140B、[140]=0x20、solution=后 32B。
	anchorFull346 = "04000000281a06c48ce20f3727e32c78e60df70010bb7c64628849f6a40b3741db000000b9243da497d12f810dee48eea15ea4bb45124b5dc4b89657241c19b402a578ba68e087a00909ce98e77050f16d9be11c048ef0451d037d9ea06811f4f4e62b57e00f516ada0e011e9e020e0001408ffb00000000000000000000000000000000000000000000000020e87d60da9cd2389b691403bb76466d98a520996643871a28be0585778ceb8915"
	// anchorSeedDisplay seedHeight=3130368（=(3131000-64)/1024*1024）的块 hash（display 序）。
	// 反转成内部序后作 RandomX key —— miningcore DragonXJobManager 同款取法。
	anchorSeedDisplay = "000000983fb35d229347a358e044007456d73679801aa698c850a7c0159ebfe1"
	// anchorBlockHash 块 #3131000 的链上 hash（display 序）== reverse(pow)。
	anchorBlockHash = "00000044826c11188c2bab69afe0f6b02772899bf714152fb10e03ee9b0661d0"
	// engineGolden 锚1 期望：RandomX_dragonx(key=32×0x42, input=140B blob[i]=i)。
	engineGolden = "4b6c85964d42800239dcc951ef71f9fd9de1fc5e313578cf56ecaf91ad1f1bef"
	// outerGolden 锚2 期望：sha256d(blob[i]=i (140B) || 0x20 || rxh[i]=(i*7)&0xff (32B))。
	outerGolden = "ef00f6d749b1726a4c2c8f3b816736d93a6097a1f4da52aa661a00676f638c85"
)

func reverse32(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func (h *Hasher) SelfTest() error {
	// 锚2（先跑：纯 Go，不碰 cgo，拼装错了最快暴露）
	blob := make([]byte, BlobLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	rxh := make([]byte, 32)
	for i := range rxh {
		rxh[i] = byte(i*7) & 0xff
	}
	if got := hex.EncodeToString(powFromRx(blob, rxh)); got != outerGolden {
		return fmt.Errorf("rx/dragonx 锚2(外层拼装) 失配: got %s want %s", got, outerGolden)
	}

	// 锚1 引擎
	key := bytes.Repeat([]byte{0x42}, 32)
	got, err := h.rxHash(key, blob)
	if err != nil {
		return fmt.Errorf("rx/dragonx 锚1: %w", err)
	}
	if hex.EncodeToString(got) != engineGolden {
		return fmt.Errorf("rx/dragonx 锚1(引擎) 失配: got %x want %s（若 rx/0 官方向量同时通过，怀疑链错 -DRANDOMX_STOCK 配置）", got, engineGolden)
	}

	// 锚3 真链块（终极：引擎 + 拼装 + seed epoch 规则一次钉死）
	full, err := hex.DecodeString(anchorFull346)
	if err != nil || len(full) != FullHeaderLen || full[BlobLen] != solutionPreamble {
		return fmt.Errorf("rx/dragonx 锚3: 锚串损坏 len=%d", len(full))
	}
	seedDisp, _ := hex.DecodeString(anchorSeedDisplay)
	result, pow, err := h.HashKeyedTwoStage(reverse32(seedDisp), full[:BlobLen])
	if err != nil {
		return fmt.Errorf("rx/dragonx 锚3: %w", err)
	}
	if !bytes.Equal(result, full[BlobLen+1:]) {
		return fmt.Errorf("rx/dragonx 锚3(真链 solution) 失配: got %x want %x（优先怀疑 seed epoch 规则 1024/64）", result, full[BlobLen+1:])
	}
	if got := hex.EncodeToString(reverse32(pow)); got != anchorBlockHash {
		return fmt.Errorf("rx/dragonx 锚3(真链块 hash) 失配: got %s want %s", got, anchorBlockHash)
	}
	return nil
}

func init() {
	hasher.RegisterKeyed(New())
}
