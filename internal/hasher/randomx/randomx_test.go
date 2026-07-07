//go:build randomx

package randomx

import (
	"testing"

	"github.com/scashcc/ntmpool/internal/hasher"
)

// 官方向量金锚（SelfTest 内含 4 组，与 NTMminer rx_kat.c STOCK 段同源）。
func TestSelfTestGoldenVectors(t *testing.T) {
	h := New()
	if err := h.SelfTest(); err != nil {
		t.Fatal(err)
	}
}

// seed 轮换：多个 seed 交替使用，LRU 只留 keepSeeds 个但结果始终正确。
func TestSeedRotation(t *testing.T) {
	h := New()
	in := []byte("This is a test")
	ref, err := h.HashKeyed([]byte("test key 000"), in)
	if err != nil {
		t.Fatal(err)
	}
	// 挤掉 key 000（keepSeeds=2 → 加两个新 seed）
	if _, err := h.HashKeyed([]byte("seed-A"), in); err != nil {
		t.Fatal(err)
	}
	if _, err := h.HashKeyed([]byte("seed-B"), in); err != nil {
		t.Fatal(err)
	}
	// 重建后结果必须逐字节一致
	got, err := h.HashKeyed([]byte("test key 000"), in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(ref) {
		t.Fatalf("seed 重建后结果漂移: %x vs %x", got, ref)
	}
}

// 注册表启动门禁路径：SelfTestAll 走 keyed 注册表。
func TestRegisteredInKeyedRegistry(t *testing.T) {
	if _, err := hasher.GetKeyed("rx/0"); err != nil {
		t.Fatal(err)
	}
	if err := hasher.SelfTestAll([]string{"rx/0"}); err != nil {
		t.Fatal(err)
	}
}

// 并发安全冒烟：多 goroutine 同 seed 压 VM 锁。
func TestConcurrentHash(t *testing.T) {
	h := New()
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := h.HashKeyed([]byte("test key 000"), []byte("This is a test"))
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
