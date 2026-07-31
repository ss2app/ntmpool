package cnjob

import (
	"testing"

	"github.com/scashcc/ntmpool/internal/stratum"
)

// 编译期 + 运行期双保险：cnjob.Manager 必须满足 stratum.CNConnectionIDAllocator，
// 否则 CNDialect.Serve 的类型断言会静默失败、退回自增 connID，窄 tag 的币（BRVA）
// 就会在累计连接数回绕后让两个在线矿工挖同一段 nonce（池侧指标全正常，极难发现）。
var _ stratum.CNConnectionIDAllocator = (*Manager)(nil)

func TestManagerIsRecognizedAsConnIDAllocator(t *testing.T) {
	m := New("brva-test", "rx/brva", &narrowTagNode{height: 1}, testKeyed{})
	m.SetConnIDSpace(256)

	// 走与 CNDialect.Serve 完全相同的类型断言路径
	var h stratum.CNShareHandler = m
	alloc, ok := h.(stratum.CNConnectionIDAllocator)
	if !ok {
		t.Fatal("Manager 未被识别为 CNConnectionIDAllocator → Serve 会退回自增 connID，P0 复发")
	}
	id, got := alloc.AcquireConnectionID()
	if !got {
		t.Fatal("首次 acquire 就失败")
	}
	if id >= 256 {
		t.Fatalf("connID %d 越界（tag 仅 1 字节）", id)
	}
	alloc.ReleaseConnectionID(id)
}
