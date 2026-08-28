package midstaterpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// fakeStateNode 最小 /state 假节点：高度/同步态/宕机态可并发改（测试 goroutine
// 与 notifier goroutine 同时读写，必须原子，否则 -race 报警）。
type fakeStateNode struct {
	height  atomic.Uint64
	syncing atomic.Bool
	down    atomic.Bool
}

func (f *fakeStateNode) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" {
			http.NotFound(w, r)
			return
		}
		if f.down.Load() {
			http.Error(w, "node down", http.StatusServiceUnavailable)
			return
		}
		h := f.height.Load()
		_ = json.NewEncoder(w).Encode(State{
			Height:      h,
			Target:      fmt.Sprintf("%064x", 1),
			BlockReward: 1 << 30,
			HeaderHash:  fmt.Sprintf("%064x", h), // 高度变则 hash 变
			IsSyncing:   f.syncing.Load(),
		})
	}))
}

func startNotifier(t *testing.T, base string, iv time.Duration) <-chan core.TipEvent {
	t.Helper()
	c := New("mds-test", base)
	n := c.TipNotifier(iv)
	ch := make(chan core.TipEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = n.Run(ctx, ch) }()
	return ch
}

// tip 只在真变化时投递一次：首轮建基线不投递、变化投递、不变不重复。
// 这是本通道的全部契约——多投一次就是白白打断矿工一轮工作。
func TestTipNotifierEmitsOnlyOnChange(t *testing.T) {
	f := &fakeStateNode{}
	f.height.Store(100)
	srv := f.server()
	defer srv.Close()

	ch := startNotifier(t, srv.URL, 10*time.Millisecond)

	// 首轮只建基线：池启动时 coininstance 已 Refresh 过，这里再投是重复。
	select {
	case ev := <-ch:
		t.Fatalf("首轮不应投递，却收到 %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	f.height.Store(101)
	select {
	case ev := <-ch:
		if ev.Height != 101 {
			t.Fatalf("投递高度 = %d，want 101", ev.Height)
		}
		if ev.Source != "midstate-tip" {
			t.Fatalf("Source = %q，want midstate-tip", ev.Source)
		}
		if ev.Hash != fmt.Sprintf("%064x", 101) {
			t.Fatalf("Hash = %q，与高度不符", ev.Hash)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tip 变化后未投递事件")
	}

	// tip 不动就必须安静，哪怕探测了几十次。
	select {
	case ev := <-ch:
		t.Fatalf("tip 未变不应重复投递，却收到 %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// 补链中(is_syncing)的 tip 是过时高度，投出去只会触发一轮无用的 force
// refresh——midjob.Refresh 在 is_syncing 时本来就会暂停模板。
func TestTipNotifierSkipsWhileSyncing(t *testing.T) {
	f := &fakeStateNode{}
	f.height.Store(200)
	srv := f.server()
	defer srv.Close()

	ch := startNotifier(t, srv.URL, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // 让它建好基线

	f.syncing.Store(true)
	f.height.Store(201)
	select {
	case ev := <-ch:
		t.Fatalf("is_syncing 期间不应投递，却收到 %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	// 同步结束后恢复投递（不能因为跳过就把基线弄丢、从此哑掉）。
	f.syncing.Store(false)
	select {
	case ev := <-ch:
		if ev.Height != 201 {
			t.Fatalf("恢复后投递高度 = %d，want 201", ev.Height)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("同步结束后未恢复投递")
	}
}

// 节点抖动时 Run 不能返回：轮询兜底虽然还在跑，但本通道自己必须自愈，
// 否则一次 503 就让加速通道永久哑火（且不会有任何告警）。
func TestTipNotifierSurvivesNodeOutage(t *testing.T) {
	f := &fakeStateNode{}
	f.height.Store(300)
	srv := f.server()
	defer srv.Close()

	ch := startNotifier(t, srv.URL, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // 建基线

	f.down.Store(true)
	f.height.Store(301) // 宕机期间链在推进，池看不到
	time.Sleep(200 * time.Millisecond)
	select {
	case ev := <-ch:
		t.Fatalf("节点 503 期间不应投递，却收到 %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	f.down.Store(false)
	select {
	case ev := <-ch:
		if ev.Height != 301 {
			t.Fatalf("节点恢复后投递高度 = %d，want 301", ev.Height)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("节点恢复后未继续投递（Run 可能已提前返回）")
	}
}
