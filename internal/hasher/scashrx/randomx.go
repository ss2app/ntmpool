//go:build randomx

package scashrx

/*
#cgo CFLAGS: -I${SRCDIR}/../../../third_party/randomx/src
#cgo LDFLAGS: ${SRCDIR}/../../../third_party/randomx/build-stock/librandomx.a -lstdc++ -lm
#include "randomx.h"

static const unsigned char ntmpool_scash_salt[] = {
	'R','a','n','d','o','m','X','-','S','c','a','s','h',0x01
};

static void ntmpool_scash_init(randomx_cache *cache, const void *key, size_t key_size) {
	randomx_init_cache_salted(cache, key, key_size,
		ntmpool_scash_salt, sizeof(ntmpool_scash_salt));
}
*/
import "C"

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"unsafe"

	"github.com/scashcc/ntmpool/internal/hasher"
)

const keepCaches = 2

type cacheVM struct {
	mu    sync.Mutex
	cache *C.randomx_cache
	vm    *C.randomx_vm
}

func (v *cacheVM) destroy() {
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

// Hasher 使用固定 SCASH salt，并以 epochKey+salt 作为两项 LRU 的 identity。
type Hasher struct {
	mu      sync.Mutex
	entries map[string]*cacheVM
	order   []string // 最旧在前；命中会移到末尾
}

var _ Engine = (*Hasher)(nil)

func New() *Hasher { return &Hasher{entries: map[string]*cacheVM{}} }

func (h *Hasher) Name() string { return Algorithm }

func cacheIdentity(key []byte) string {
	id := make([]byte, 0, len(key)+1+len(Salt))
	id = append(id, key...)
	id = append(id, 0x1f)
	id = append(id, Salt...)
	return string(id)
}

// lockVM 返回一个已加锁的 VM；调用方完成 hash 后必须 Unlock。
func (h *Hasher) lockVM(key []byte) (*cacheVM, error) {
	id := cacheIdentity(key)
	h.mu.Lock()
	if v, ok := h.entries[id]; ok {
		for i, existing := range h.order {
			if existing == id {
				h.order = append(h.order[:i], h.order[i+1:]...)
				break
			}
		}
		h.order = append(h.order, id)
		v.mu.Lock()
		h.mu.Unlock()
		if v.vm == nil {
			v.mu.Unlock()
			return nil, fmt.Errorf("rx/scash: VM 已销毁")
		}
		return v, nil
	}

	flags := C.randomx_get_flags()
	cache := C.randomx_alloc_cache(flags)
	if cache == nil {
		flags = C.RANDOMX_FLAG_DEFAULT
		cache = C.randomx_alloc_cache(flags)
		if cache == nil {
			h.mu.Unlock()
			return nil, fmt.Errorf("rx/scash: alloc_cache 失败")
		}
	}
	var keyPtr unsafe.Pointer
	if len(key) > 0 {
		keyPtr = unsafe.Pointer(&key[0])
	}
	C.ntmpool_scash_init(cache, keyPtr, C.size_t(len(key)))
	vm := C.randomx_create_vm(flags, cache, nil)
	if vm == nil {
		C.randomx_release_cache(cache)
		h.mu.Unlock()
		return nil, fmt.Errorf("rx/scash: create_vm 失败")
	}
	v := &cacheVM{cache: cache, vm: vm}
	h.entries[id] = v
	h.order = append(h.order, id)
	var evicted *cacheVM
	if len(h.order) > keepCaches {
		old := h.order[0]
		h.order = h.order[1:]
		evicted = h.entries[old]
		delete(h.entries, old)
	}
	v.mu.Lock()
	h.mu.Unlock()
	if evicted != nil {
		go evicted.destroy()
	}
	return v, nil
}

// Hash 重算 R 与 CM。非 112B 或尾部非零一律拒绝，避免把 solved header 错喂给
// RandomX/commitment；调用方无法取得任何仅比较 R 的 digest 接口。
func (h *Hasher) Hash(key, zeroedHeader []byte) (Result, error) {
	var out Result
	if len(key) != 32 {
		return out, fmt.Errorf("rx/scash: epoch key 长度 %d != 32", len(key))
	}
	if !IsZeroedHeader(zeroedHeader) {
		return out, fmt.Errorf("rx/scash: 输入必须是尾 32B 全零的 112B header")
	}
	v, err := h.lockVM(key)
	if err != nil {
		return out, err
	}
	defer v.mu.Unlock()
	C.randomx_calculate_hash(v.vm,
		unsafe.Pointer(&zeroedHeader[0]), C.size_t(len(zeroedHeader)),
		unsafe.Pointer(&out.R[0]))
	C.randomx_calculate_commitment(
		unsafe.Pointer(&zeroedHeader[0]), C.size_t(len(zeroedHeader)),
		unsafe.Pointer(&out.R[0]), unsafe.Pointer(&out.CM[0]))
	return out, nil
}

// VerifySolvedHeader 用于完整 112B header 的防伪校验：清零 stored R 后重算，且要求
// stored R 逐字节等于重算 R。仅让伪造 R 对应的便宜 CM 达标，不能绕过此检查。
func (h *Hasher) VerifySolvedHeader(key, solvedHeader []byte) (Result, error) {
	var out Result
	if len(solvedHeader) != HeaderSize {
		return out, fmt.Errorf("rx/scash: solved header 长度 %d != 112", len(solvedHeader))
	}
	storedR := append([]byte(nil), solvedHeader[RXOffset:]...)
	zeroed := append([]byte(nil), solvedHeader...)
	clear(zeroed[RXOffset:])
	result, err := h.Hash(key, zeroed)
	if err != nil {
		return out, err
	}
	if !bytes.Equal(storedR, result.R[:]) {
		return out, fmt.Errorf("rx/scash: header stored R 与池端重算不一致")
	}
	return result, nil
}

const (
	genesisZeroedHex = "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4adae5494dffff7f1edb1700000000000000000000000000000000000000000000000000000000000000000000"
	genesisKeyRawHex = "468543b35e74a7bf2c691ed05527be4cd35b5a6ab5ef731229bf1c055e514e22"
	genesisRRawHex   = "86af952d5202ecbf18bef2311391d6cc7951dbb6388a3c78b1a404b6dfdd48e8"
	genesisCMRawHex  = "6848a22489127b6c6cfca8b4052af8d3e1066106aae34ca1eaa50a6a8a380000"
	genesisBlockID   = "0e3ba94819749c208e2526d9b829e0dba109f1bce4e62600c0fc556294f24c82"
)

func (h *Hasher) SelfTest() error {
	key, _ := hex.DecodeString(genesisKeyRawHex)
	header, _ := hex.DecodeString(genesisZeroedHex)
	wantR, _ := hex.DecodeString(genesisRRawHex)
	wantCM, _ := hex.DecodeString(genesisCMRawHex)
	got, err := h.Hash(key, header)
	if err != nil {
		return err
	}
	if !bytes.Equal(got.R[:], wantR) {
		return fmt.Errorf("R 金锚失配: got %x want %x", got.R, wantR)
	}
	if !bytes.Equal(got.CM[:], wantCM) {
		return fmt.Errorf("CM 金锚失配: got %x want %x", got.CM, wantCM)
	}
	// nBits 0x1e7fffff → mantissa 0x7fffff 左移 8*(30-3)。CM 按 uint256 LE 比较。
	target := new(big.Int).Lsh(big.NewInt(0x7fffff), 8*(0x1e-3))
	cmValue := new(big.Int).SetBytes(reverse(got.CM[:]))
	if cmValue.Cmp(target) > 0 {
		return fmt.Errorf("CM 未达到 testnet genesis target")
	}
	solved := append([]byte(nil), header...)
	copy(solved[RXOffset:], got.R[:])
	h1 := sha256.Sum256(solved)
	h2 := sha256.Sum256(h1[:])
	if DisplayHex(h2[:]) != genesisBlockID {
		return fmt.Errorf("block ID 金锚失配: got %s want %s", DisplayHex(h2[:]), genesisBlockID)
	}
	return nil
}

func reverse(in []byte) []byte {
	out := make([]byte, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

func init() {
	h := New()
	Register(h)
	hasher.RegisterStandaloneSelfTest(Algorithm, h.SelfTest)
}
