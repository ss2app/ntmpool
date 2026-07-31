package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

// allocCNHandler 复现 BRVA 的窄 tag 场景：tag 空间只有 space 个取值，
// 由 handler 池化分配（在线唯一、断开归还）。ConnJob 把 connID 回显进 blob，
// 让测试能从 wire 上看到每条连接实际拿到的 tag。
type allocCNHandler struct {
	mu     sync.Mutex
	space  uint32
	next   uint32
	active map[uint32]struct{}

	acquired int // 累计成功分配次数
	refused  int // 累计拒绝次数
}

func newAllocHandler(space uint32) *allocCNHandler {
	return &allocCNHandler{space: space, active: map[uint32]struct{}{}}
}

func (h *allocCNHandler) Algo() string              { return "rx/test" }
func (h *allocCNHandler) LoginExtensions() []string { return []string{"algo", "keepalive", "nicehash"} }

func (h *allocCNHandler) ConnJob(connID uint32, _ float64) (CNWireJob, bool) {
	// blob 末字节 = 该连接的 tag。⚠ 必须按 tag 空间取模，等价于 cnjob.materialize
	// 里的 `tag := connID & searchMask(tagBytes)`——tag 字段装不下的高位会被截掉，
	// 回绕撞车正是这么发生的（BRVA 真实情况：1 字节 tag，掩码 0xFF，space 256）。
	return CNWireJob{
		JobID: "j1", Blob: fmt.Sprintf("00%02x", connID%h.space), Target: "ffffffff",
		Algo: "rx/test", Height: 1, SeedHash: "aa",
	}, true
}

func (h *allocCNHandler) HandleSubmit(_ context.Context, _ CNSubmission) SubmitResult {
	return SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: 1}
}

func (h *allocCNHandler) AcquireConnectionID() (uint32, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if uint32(len(h.active)) >= h.space {
		h.refused++
		return 0, false
	}
	for i := uint32(0); i < h.space; i++ {
		id := h.next % h.space
		h.next++
		if _, used := h.active[id]; used {
			continue
		}
		h.active[id] = struct{}{}
		h.acquired++
		return id, true
	}
	h.refused++
	return 0, false
}

func (h *allocCNHandler) ReleaseConnectionID(connID uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, connID)
}

func (h *allocCNHandler) liveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.active)
}

// cnTagProbe 起一条真 TCP 连接、login、返回池下发 job 的 blob（末字节=tag）与关闭函数。
func cnTagProbe(t *testing.T, d *CNDialect, ln net.Listener, pc config.PortConfig,
	ctx context.Context) (blob string, closed bool, done func()) {
	t.Helper()

	served := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- d.Serve(ctx, conn, pc)
		_ = conn.Close()
	}()

	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))

	send(t, cli, map[string]any{
		"id": 1, "jsonrpc": "2.0", "method": "login",
		"params": map[string]any{"login": "addr", "pass": "x", "agent": "probe"},
	})

	sc := bufio.NewScanner(cli)
	if !sc.Scan() {
		// 池直接断开 = 拒连（tag 空间满）
		_ = cli.Close()
		<-served
		return "", true, func() {}
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("非法响应 %q: %v", sc.Text(), err)
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		_ = cli.Close()
		<-served
		return "", true, func() {}
	}
	job, _ := res["job"].(map[string]any)
	b, _ := job["blob"].(string)
	return b, false, func() { _ = cli.Close(); <-served }
}

// 核心回归：tag 空间用完前，任意两条【同时在线】的连接必须拿到不同的 tag。
//
// 修复前 Serve 用纯自增 connSeq 当 connID，累计第 space+1 条连接就与第 1 条撞上
// （tag = connID & 0xFF），两个在线矿工扫完全相同的 nonce 空间，池有效算力=单机。
// 本测试在小空间(4)上把回绕压缩成 5 条连接即可复现。
func TestCNConnTagNeverCollidesWhileOnline(t *testing.T) {
	const space = 4
	h := newAllocHandler(space)
	d := NewCNDialect("brva-test", h)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc := config.PortConfig{
		Port: 0, Mode: "pplns", Dialect: "cryptonote",
		Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1000, MinDiff: 1, MaxDiff: 100000, TargetSeconds: 10},
	}

	// 开满整个 tag 空间，全部保持在线
	seen := map[string]int{}
	var closers []func()
	for i := 0; i < space; i++ {
		blob, refused, done := cnTagProbe(t, d, ln, pc, ctx)
		if refused {
			t.Fatalf("第 %d 条连接就被拒（空间 %d）", i+1, space)
		}
		if prev, dup := seen[blob]; dup {
			t.Fatalf("连接 #%d 与 #%d 拿到相同 tag（blob=%s）→ 两个在线矿工会扫相同 nonce", i+1, prev, blob)
		}
		seen[blob] = i + 1
		closers = append(closers, done)
	}
	// 空间已满：第 space+1 条必须被【拒绝】，而不是回绕发出一个已在线的 tag。
	// 这条就是回归的靶心——修复前 Serve 用自增 connSeq，这条会成功连上并拿到
	// 与第 1 条相同的 tag（seq=space+1 → (space+1)%space = 1）。
	extraBlob, refused, done := cnTagProbe(t, d, ln, pc, ctx)
	if !refused {
		done()
		if prev, dup := seen[extraBlob]; dup {
			t.Fatalf("tag 空间已满仍接受新连接，且与在线的 #%d 撞了同一个 tag（blob=%s）"+
				" → 两个在线矿工扫完全相同的 nonce 空间，池有效算力=单机（指标却全正常）", prev, extraBlob)
		}
		t.Fatalf("tag 空间已满仍接受新连接（blob=%s）→ 迟早发出重复 tag", extraBlob)
	}
	if h.refused == 0 {
		t.Fatal("分配器未被调用（Serve 仍在用自增 connSeq？）")
	}
	if h.liveCount() != space {
		t.Fatalf("在线 tag 数 = %d，want %d", h.liveCount(), space)
	}

	// 断开一条 → tag 归还 → 新连接可复用，且与其余在线连接仍不冲突
	closers[1]()
	closers = append(closers[:1], closers[2:]...)
	deadline := time.Now().Add(2 * time.Second)
	for h.liveCount() != space-1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.liveCount() != space-1 {
		t.Fatalf("断开后 tag 未归还，在线 = %d，want %d", h.liveCount(), space-1)
	}
	blob, refused, done := cnTagProbe(t, d, ln, pc, ctx)
	if refused {
		t.Fatal("归还后仍拒连")
	}
	defer done()
	for _, c := range closers {
		defer c()
	}
	// 复用的 tag 不能与仍在线的任何一条相同
	live := map[string]bool{}
	for b, idx := range seen {
		if idx != 2 { // #2 已断开，它的 tag 可被复用
			live[b] = true
		}
	}
	if live[blob] {
		t.Fatalf("复用的 tag 与仍在线的连接冲突（blob=%s）", blob)
	}
}

// handler 不实现分配器时必须回退纯自增，宽 tag 的币（dragonx 等）行为不变。
func TestCNConnTagFallsBackWhenHandlerHasNoAllocator(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0", outcome: core.OutcomeAccepted}
	if _, ok := interface{}(h).(CNConnectionIDAllocator); ok {
		t.Fatal("fakeCNHandler 不该实现分配器（本测试前提）")
	}
	sc, cli, done := startCN(t, h)
	defer done()
	send(t, cli, map[string]any{
		"id": 1, "jsonrpc": "2.0", "method": "login",
		"params": map[string]any{"login": "addr1", "pass": "x", "agent": "x"},
	})
	m := recv(t, sc)
	if m["error"] != nil {
		t.Fatalf("无分配器的 handler 不该被拒: %v", m["error"])
	}
	if res, _ := m["result"].(map[string]any); res == nil || res["job"] == nil {
		t.Fatalf("未拿到 job: %v", m)
	}
}
