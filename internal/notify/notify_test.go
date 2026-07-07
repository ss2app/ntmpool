package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// webhook：JSON 形状 + 事件字段完整送达。
func TestWebhookSink(t *testing.T) {
	var mu sync.Mutex
	var got []Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Errorf("webhook 收到非法 JSON: %v", err)
		}
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}))
	defer srv.Close()

	h := NewHub([]Sink{NewWebhookSink(srv.URL)})
	defer h.Close()
	h.Publish(Event{Kind: "block_found", Coin: "tst", Title: "爆块 #101",
		Fields: map[string]string{"height": "101", "hash": "abc"}})

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook 未收到事件")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0].Kind != "block_found" || got[0].Fields["height"] != "101" || got[0].At.IsZero() {
		t.Fatalf("事件内容错: %+v", got[0])
	}
}

// telegram：路径含 token、body 含 chat_id 与拼好的文本。
func TestTelegramSink(t *testing.T) {
	var gotPath, gotText, gotChat string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		gotPath, gotText, gotChat = r.URL.Path, body["text"], body["chat_id"]
		mu.Unlock()
	}))
	defer srv.Close()

	s := NewTelegramSink("TOKEN123", "chat42")
	s.BaseURL = srv.URL
	err := s.Send(context.Background(), Event{Kind: "payout_sent", Coin: "tst",
		Title: "打款已广播", Fields: map[string]string{"txid": "deadbeef", "addrs": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/botTOKEN123/sendMessage" || gotChat != "chat42" {
		t.Fatalf("path=%s chat=%s", gotPath, gotChat)
	}
	if !strings.Contains(gotText, "[tst] 打款已广播") || !strings.Contains(gotText, "txid: deadbeef") {
		t.Fatalf("text=%q", gotText)
	}
}

// 队列满不阻塞：慢 sink 下 Publish 立即返回，超量丢弃计数。
func TestPublishNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	slow := &funcSink{fn: func(context.Context, Event) error { <-block; return nil }}
	h := NewHub([]Sink{slow})
	defer func() { close(block); h.Close() }()

	start := time.Now()
	for i := 0; i < 1000; i++ { // 远超 256 队列
		h.Publish(Event{Kind: "spam"})
	}
	if time.Since(start) > time.Second {
		t.Fatal("Publish 阻塞了主路径")
	}
	if h.Dropped() == 0 {
		t.Fatal("超量应有丢弃计数")
	}
}

// 无 sink：Publish 是空操作（接线代码不用判 nil）。
func TestNoSinksNoop(t *testing.T) {
	h := NewHub(nil)
	defer h.Close()
	h.Publish(Event{Kind: "x"})
	if h.Dropped() != 0 {
		t.Fatal("无 sink 不应计丢弃")
	}
}

// 单 sink 失败不影响其他 sink。
func TestSinkIsolation(t *testing.T) {
	var okCount int
	var mu sync.Mutex
	bad := &funcSink{fn: func(context.Context, Event) error { return context.DeadlineExceeded }}
	good := &funcSink{fn: func(context.Context, Event) error {
		mu.Lock()
		okCount++
		mu.Unlock()
		return nil
	}}
	h := NewHub([]Sink{bad, good})
	defer h.Close()
	h.Publish(Event{Kind: "k"})

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := okCount
		mu.Unlock()
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("好 sink 未收到事件")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type funcSink struct {
	fn func(context.Context, Event) error
}

func (f *funcSink) Name() string                            { return "func" }
func (f *funcSink) Send(ctx context.Context, e Event) error { return f.fn(ctx, e) }
