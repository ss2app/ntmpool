package dragonxrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// longpoll 契约：首拉（无 longpollid）立即返回并缓存 id；带 id 的请求挂等到
// 「链头变化」（假节点用延迟模拟）才返回新模板；id 逐轮传递。
func TestLongPollNotifier(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		lpid := ""
		if len(req.Params) > 0 {
			if v, ok := req.Params[0]["longpollid"].(string); ok {
				lpid = v
			}
		}
		n := calls.Add(1)
		switch {
		case lpid == "":
			// 首拉：立即返回
			writeGBT(w, 100, "aa11", "lp1")
		case lpid == "lp1":
			// 挂等：模拟 250ms 后链头变化
			time.Sleep(250 * time.Millisecond)
			writeGBT(w, 101, "bb22", "lp2")
		default:
			// lp2 之后一直挂着（测试结束前不返回）
			time.Sleep(5 * time.Second)
			writeGBT(w, 101, "bb22", "lp2")
		}
		_ = n
	}))
	defer srv.Close()

	c := New("t", srv.URL, "u", "p", "zsVault")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch := make(chan core.TipEvent, 4)
	go func() { _ = c.LongPollNotifier().Run(ctx, ch) }()

	// 事件 1：首拉（无 id，立即）
	ev1 := waitEvent(t, ctx, ch)
	if ev1.Height != 100 || ev1.Hash != "aa11" || ev1.Source != "longpoll" {
		t.Fatalf("首拉事件错: %+v", ev1)
	}
	// 事件 2：挂等唤醒（带 lp1，~250ms 后）
	start := time.Now()
	ev2 := waitEvent(t, ctx, ch)
	if ev2.Height != 101 || ev2.Hash != "bb22" {
		t.Fatalf("挂等事件错: %+v", ev2)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("第二个事件来得太快（%v）——没真挂等", d)
	}
	if calls.Load() < 2 {
		t.Fatalf("调用数 %d < 2", calls.Load())
	}
}

func writeGBT(w http.ResponseWriter, height uint64, prev, lpid string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{
			"height": height, "previousblockhash": prev, "longpollid": lpid,
		},
		"error": nil, "id": "ntmpool-lp",
	})
}

func waitEvent(t *testing.T, ctx context.Context, ch <-chan core.TipEvent) core.TipEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-ctx.Done():
		t.Fatal("超时未收到事件")
		return core.TipEvent{}
	}
}
