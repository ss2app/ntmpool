//go:build randomx

package dragonxrx

import (
	"bytes"
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

// 注册表启动门禁路径。
func TestRegisteredInKeyedRegistry(t *testing.T) {
	if _, err := hasher.GetKeyed("rx/dragonx"); err != nil {
		t.Fatal(err)
	}
	if err := hasher.SelfTestAll([]string{"rx/dragonx"}); err != nil {
		t.Fatal(err)
	}
}
