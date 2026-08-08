//go:build randomx

package dragonxrx

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/scashcc/ntmpool/internal/hasher"
	rx0 "github.com/scashcc/ntmpool/internal/hasher/randomx"
)

// 三层金锚（引擎合成锚 + 外层拼装锚 + 真链块 #3131000 终极锚，兼裁 seed epoch 1024/64）。
func TestSelfTestGoldenVectors(t *testing.T) {
	h := New()
	if err := h.SelfTest(); err != nil {
		t.Fatal(err)
	}
}

// HashKeyed（单段口径）必须 == TwoStage 的 pow（契约：单段调用方拿到的就是 PoW 值）。
func TestTwoStageConsistency(t *testing.T) {
	h := New()
	key := bytes.Repeat([]byte{0x42}, 32)
	blob := make([]byte, BlobLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	result, pow, err := h.HashKeyedTwoStage(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	single, err := h.HashKeyed(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(single, pow) {
		t.Fatalf("HashKeyed(%x) ≠ TwoStage pow(%x)", single, pow)
	}
	if len(result) != 32 || len(pow) != 32 {
		t.Fatalf("hash 长度异常 result=%d pow=%d", len(result), len(pow))
	}
	// pow 必须真的是 powFromRx(blob, result)
	if !bytes.Equal(pow, powFromRx(blob, result)) {
		t.Fatal("pow ≠ powFromRx(blob, result)：双段编排内部不自洽")
	}
}

// blob 长度硬校验：非 140B 直接拒绝（消费端字节错=废块，宁可 fail-fast）。
func TestBlobLenEnforced(t *testing.T) {
	h := New()
	key := bytes.Repeat([]byte{0x42}, 32)
	if _, _, err := h.HashKeyedTwoStage(key, make([]byte, 139)); err == nil {
		t.Fatal("139B blob 未被拒绝")
	}
	if _, err := h.HashKeyed(key, make([]byte, 141)); err == nil {
		t.Fatal("141B blob 未被拒绝")
	}
}

// 串味 tripwire：同 (key,input) 下 rx/dragonx 内层结果必须 ≠ rx/0。
// 若相等 = 两份库链到了同一套配置常量（构建/前缀隔离失败）= 挖废块级事故。
func TestDivergesFromStock(t *testing.T) {
	drg := New()
	stock := rx0.New()
	key := bytes.Repeat([]byte{0x42}, 32)
	blob := make([]byte, BlobLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	drgResult, _, err := drg.HashKeyedTwoStage(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	stockHash, err := stock.HashKeyed(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(drgResult, stockHash) {
		t.Fatal("rx/dragonx 内层 == rx/0 输出：两库配置串味（符号前缀/构建隔离失败）")
	}
}

// —— 以下为 2026-08-03 多 VM 改造（1 只读 cache + N 个 VM + RWMutex 借还池）新增 ——

// 并发正确性 tripwire：VM 池借还绝不能串味。
//
// ★期望值不是自己算的——用主网块 #3131000 的链上 solution + 链上块 hash 当外部权威。
// 若拿「先跑一遍自己的实现」当基准，错误行为会被写成期望值、单测长期全绿反而锁死 bug。
// 并发下每一条结果都必须逐字节等于链上真值，否则 = 挖废块级事故。
func TestConcurrentHashMatchesChainAnchor(t *testing.T) {
	h := New()
	full, err := hex.DecodeString(anchorFull346)
	if err != nil || len(full) != FullHeaderLen || full[BlobLen] != solutionPreamble {
		t.Fatalf("锚串损坏 len=%d", len(full))
	}
	seedDisp, err := hex.DecodeString(anchorSeedDisplay)
	if err != nil {
		t.Fatal(err)
	}
	key := reverse32(seedDisp)
	blob := full[:BlobLen]
	wantResult := full[BlobLen+1:]

	const goroutines, iters = 24, 6
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines*iters)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				result, pow, err := h.HashKeyedTwoStage(key, blob)
				if err != nil {
					errCh <- fmt.Sprintf("g%d i%d: %v", g, i, err)
					return
				}
				if !bytes.Equal(result, wantResult) {
					errCh <- fmt.Sprintf("g%d i%d: 内层 solution 失配 got %x want %x", g, i, result, wantResult)
					return
				}
				if got := hex.EncodeToString(reverse32(pow)); got != anchorBlockHash {
					errCh <- fmt.Sprintf("g%d i%d: 块 hash 失配 got %s want %s", g, i, got, anchorBlockHash)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}

// seed 轮换 × 并发：LRU 淘汰走 `go destroy()`，必须与在途 hash 安全共存。
// destroy 拿写锁等在途算完，计算侧持读锁 → 不得 panic / use-after-free / 数据竞争。
// 配合 -race 跑才有意义。规模刻意压小：每个 seed 要 alloc+init 一份 256MB cache。
func TestConcurrentSeedRotationSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过：每个 seed 需 init 一份 256MB cache")
	}
	h := New()
	blob := make([]byte, BlobLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// 3 个 seed 轮转，keepSeeds=2 ⇒ 必然触发 LRU 淘汰 + 异步 destroy
			for i := 0; i < 3; i++ {
				key := bytes.Repeat([]byte{byte(i + 1)}, 32)
				// 允许返回「VM 已销毁（seed 已过轮换窗口）」错误，但不得崩
				_, _ = h.HashKeyed(key, blob)
			}
		}(g)
	}
	wg.Wait()
}

// 并发吞吐 benchmark：验证 powVerifyConcurrency 不再是摆设。
// ★真实数字必须真机实测并贴命令输出（铁律⑤），不采信任何自报值。
func BenchmarkHashKeyedParallel(b *testing.B) {
	h := New()
	key := bytes.Repeat([]byte{0x42}, 32)
	blob := make([]byte, BlobLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	if _, err := h.HashKeyed(key, blob); err != nil { // 预热：建 cache+VM 池不计入
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := h.HashKeyed(key, blob); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// 注册表启动门禁路径。
func TestRegisteredInKeyedRegistry(t *testing.T) {
	if _, err := hasher.GetKeyed("rx/dragonx"); err != nil {
		t.Fatal(err)
	}
	if err := hasher.SelfTestAll([]string{"rx/dragonx"}); err != nil {
		t.Fatal(err)
	}
}
