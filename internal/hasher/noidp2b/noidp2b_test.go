//go:build noid

package noidp2b

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/scashcc/ntmpool/internal/hasher"
)

type katEntry struct {
	FieldsHex string `json:"fields_hex"`
	NonceHex  string `json:"nonce_hex"`
	DigestHex string `json:"digest_hex"`
}

// loadKAT testdata/kat.json 由 third_party/noidpow 的 gen_kat 生成
// （金值来源=官方 poseidon_pow_digest_from_fields 直调，独立于 FFI 路径）。
// 重新生成：cd third_party/noidpow && cargo run --release --bin gen_kat > .../testdata/kat.json
func loadKAT(t *testing.T) []katEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/kat.json")
	if err != nil {
		t.Fatalf("读 KAT 文件失败（先跑 gen_kat 生成）: %v", err)
	}
	var kat []katEntry
	if err := json.Unmarshal(raw, &kat); err != nil {
		t.Fatalf("解析 kat.json 失败: %v", err)
	}
	if len(kat) < 64 {
		t.Fatalf("KAT 向量太少: %d < 64", len(kat))
	}
	return kat
}

func decodeEntry(t *testing.T, i int, e katEntry) (fields, nonce, digest []byte) {
	t.Helper()
	var err error
	if fields, err = hex.DecodeString(e.FieldsHex); err != nil || len(fields) != FieldsWireBytes {
		t.Fatalf("KAT[%d] fields 坏 (err=%v len=%d)", i, err, len(fields))
	}
	if nonce, err = hex.DecodeString(e.NonceHex); err != nil || len(nonce) != NonceWireBytes {
		t.Fatalf("KAT[%d] nonce 坏 (err=%v len=%d)", i, err, len(nonce))
	}
	if digest, err = hex.DecodeString(e.DigestHex); err != nil || len(digest) != 32 {
		t.Fatalf("KAT[%d] digest 坏 (err=%v len=%d)", i, err, len(digest))
	}
	return
}

// TestKATDigest 全量 KAT 逐条逐字节比对：cgo Digest == 官方参考 digest。
func TestKATDigest(t *testing.T) {
	kat := loadKAT(t)
	for i, e := range kat {
		fields, nonce, want := decodeEntry(t, i, e)
		got := Digest(fields, nonce)
		if !bytes.Equal(got[:], want) {
			t.Fatalf("KAT[%d] digest 失配:\n  fields=%s\n  nonce =%s\n  got   =%x\n  want  =%s",
				i, e.FieldsHex, e.NonceHex, got, e.DigestHex)
		}
	}
	t.Logf("验证 %d 条 KAT 向量全部逐字节一致", len(kat))
}

// le256Add1/Sub1 256-bit 小端 ±1（测试自备，独立于被测代码）。
func le256Add1(v []byte) ([]byte, bool) {
	out := append([]byte(nil), v...)
	for i := range out {
		out[i]++
		if out[i] != 0 {
			return out, true
		}
	}
	return out, false // 溢出（v 全 0xFF）
}

func le256Sub1(v []byte) ([]byte, bool) {
	out := append([]byte(nil), v...)
	for i := range out {
		out[i]--
		if out[i] != 0xFF {
			return out, true
		}
	}
	return out, false // 下溢（v 全 0x00）
}

// TestCheckStrictLess Check 的严格 `<` 边界：digest+1 接受 / ==digest 拒绝 / digest-1 拒绝。
func TestCheckStrictLess(t *testing.T) {
	kat := loadKAT(t)
	for i, e := range kat[:8] { // 边界+随机各覆盖若干条即可，语义与数据无关
		fields, nonce, digest := decodeEntry(t, i, e)

		if up, ok := le256Add1(digest); ok {
			if !Check(fields, nonce, up) {
				t.Fatalf("KAT[%d]: target=digest+1 应接受，被拒绝 (digest=%s)", i, e.DigestHex)
			}
		}
		if Check(fields, nonce, digest) {
			t.Fatalf("KAT[%d]: target==digest 应拒绝（严格 <），被接受 (digest=%s)", i, e.DigestHex)
		}
		if down, ok := le256Sub1(digest); ok {
			if Check(fields, nonce, down) {
				t.Fatalf("KAT[%d]: target=digest-1 应拒绝，被接受 (digest=%s)", i, e.DigestHex)
			}
		}
		// 极端 target
		if !Check(fields, nonce, bytes.Repeat([]byte{0xFF}, 32)) {
			t.Fatalf("KAT[%d]: max target 应接受", i)
		}
		if Check(fields, nonce, make([]byte, 32)) {
			t.Fatalf("KAT[%d]: zero target 应拒绝", i)
		}
	}
}

// TestConcurrentDigest 线程安全实证：并发重算全量 KAT 结果仍逐字节一致
// （Rust 侧仅 OnceLock 懒表，纯函数无锁——这里用真并发压一遍佐证）。
func TestConcurrentDigest(t *testing.T) {
	kat := loadKAT(t)
	var wg sync.WaitGroup
	errs := make(chan string, len(kat)*4)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, e := range kat {
				fields, _ := hex.DecodeString(e.FieldsHex)
				nonce, _ := hex.DecodeString(e.NonceHex)
				want, _ := hex.DecodeString(e.DigestHex)
				got := Digest(fields, nonce)
				if !bytes.Equal(got[:], want) {
					errs <- "KAT digest=" + e.DigestHex + " 并发下失配"
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("并发失配: %s", msg)
	}
}

// TestHasherInterface Hash(fields‖nonce) 与 Digest 同值；注册表可取到；长度错返回 error。
func TestHasherInterface(t *testing.T) {
	kat := loadKAT(t)
	fields, nonce, want := decodeEntry(t, 0, kat[5])

	h, err := hasher.Get("poseidon2b/noid")
	if err != nil {
		t.Fatalf("注册表取 poseidon2b/noid 失败: %v", err)
	}
	got, err := h.Hash(append(append([]byte(nil), fields...), nonce...))
	if err != nil {
		t.Fatalf("Hash 报错: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Hash(fields‖nonce) 与 KAT 失配: got %x want %x", got, want)
	}
	if _, err := h.Hash(make([]byte, 100)); err == nil {
		t.Fatal("错误长度 input 应返回 error")
	}
	if err := h.SelfTest(); err != nil {
		t.Fatalf("SelfTest 失败: %v", err)
	}
}
