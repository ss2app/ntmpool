// Package notify 运营通知（docs/03 M2：爆块/打款/孤块/节点失联/对账不平）。
//
// 设计原则：
//   - 通知**绝不阻塞**挖矿/打款主路径：Publish 非阻塞投递到有界队列，
//     队列满直接丢弃并计数（通知是尽力而为的旁路，会计/日志才是事实源）。
//   - 去重责任在事件源：node_down/frozen 这类持续状态只在**翻转时**发一次
//     （调用方保证），Hub 不做时间窗节流——孤块连发这类「刷屏」恰恰必须看到。
//   - Sink 可插拔：webhook（通用 JSON POST，接任何自动化）+ Telegram bot。
//     单 sink 失败只记日志，不影响其他 sink。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Event 一条通知事件。
type Event struct {
	Kind   string            `json:"kind"` // block_found/block_confirmed/block_orphaned/payout_sent/payout_failed/reconcile_frozen/node_down/node_up/fee_collected
	Coin   string            `json:"coin,omitempty"`
	Title  string            `json:"title"`
	Fields map[string]string `json:"fields,omitempty"`
	At     time.Time         `json:"at"`
}

// Text 人读单行/多行文本（Telegram 等纯文本 sink 用）。Fields 按 key 排序保证稳定。
func (e Event) Text() string {
	s := fmt.Sprintf("[%s] %s", e.Coin, e.Title)
	if e.Coin == "" {
		s = e.Title
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s += fmt.Sprintf("\n%s: %s", k, e.Fields[k])
	}
	return s
}

// Sink 一个通知出口。
type Sink interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// Hub 异步分发中枢。
type Hub struct {
	ch      chan Event
	sinks   []Sink
	dropped atomic.Int64
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

// NewHub 启动后台分发循环。sinks 为空也可用（Publish 变空操作，接线代码不用判 nil）。
func NewHub(sinks []Sink) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{ch: make(chan Event, 256), sinks: sinks, cancel: cancel}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-h.ch:
				for _, s := range h.sinks {
					sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					if err := s.Send(sctx, e); err != nil {
						log.Printf("[notify] %s 发送失败 (%s): %v", s.Name(), e.Kind, err)
					}
					cancel()
				}
			}
		}
	}()
	return h
}

// Publish 非阻塞投递；队列满丢弃并计数。
func (h *Hub) Publish(e Event) {
	if len(h.sinks) == 0 {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	select {
	case h.ch <- e:
	default:
		n := h.dropped.Add(1)
		if n%100 == 1 { // 防日志刷屏
			log.Printf("[notify] ⚠ 通知队列满，累计丢弃 %d 条（最新: %s）", n, e.Kind)
		}
	}
}

// Dropped 累计丢弃数（监控用）。
func (h *Hub) Dropped() int64 { return h.dropped.Load() }

// Close 停止分发（进程退出时调；队列内未发送的丢弃）。
func (h *Hub) Close() {
	h.cancel()
	h.wg.Wait()
}

// ---- sinks ----

// WebhookSink 通用 JSON POST。
type WebhookSink struct {
	URL    string
	Client *http.Client
}

func NewWebhookSink(url string) *WebhookSink {
	return &WebhookSink{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *WebhookSink) Name() string { return "webhook" }

func (s *WebhookSink) Send(ctx context.Context, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 回 %d", resp.StatusCode)
	}
	return nil
}

// TelegramSink Telegram bot sendMessage。
type TelegramSink struct {
	BotToken string
	ChatID   string
	Client   *http.Client
	// baseURL 可注入（测试）；空 = 官方 API
	BaseURL string
}

func NewTelegramSink(botToken, chatID string) *TelegramSink {
	return &TelegramSink{BotToken: botToken, ChatID: chatID,
		Client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *TelegramSink) Name() string { return "telegram" }

func (s *TelegramSink) Send(ctx context.Context, e Event) error {
	base := s.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	body, err := json.Marshal(map[string]string{
		"chat_id": s.ChatID,
		"text":    e.Text(),
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", base, s.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram 回 %d", resp.StatusCode)
	}
	return nil
}
