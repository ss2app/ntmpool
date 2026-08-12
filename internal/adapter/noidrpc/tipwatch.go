package noidrpc

import (
	"context"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// tipPollInterval getChainInfo 轮询间隔。NOID 无任何推送（无 ZMQ/WS/SSE），只能轮询；
// 100-250ms 对节点无压力（10 QPS），轮询延迟被模板制备 7-35s 完全淹没。
const tipPollInterval = 150 * time.Millisecond

// TipWatcher 实现 adapter.Notifier：轮询 getChainInfo，(height,best_hash) 变化即发
// core.TipEvent（Source="poll"）。自带错误退避（HTTP 无状态，下一轮自愈）。
type TipWatcher struct {
	c        *Client
	interval time.Duration
	label    string
}

// TipNotifier 返回本节点的轮询 tip 通知器。
func (c *Client) TipNotifier() *TipWatcher {
	return &TipWatcher{c: c, interval: tipPollInterval, label: "poll"}
}

// Run 阻塞轮询，链头变化写入 ch；ctx 取消退出。
func (w *TipWatcher) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	var lastH uint64
	var lastHash string
	tick := time.NewTicker(w.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		ci, err := w.c.getChainInfo(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 错误退避：小睡一下避免节点抖动时打满日志（下一轮 tick 继续）。
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if ci.Height == lastH && strings.EqualFold(ci.BestHash, lastHash) {
			continue
		}
		lastH, lastHash = ci.Height, ci.BestHash
		ev := core.TipEvent{
			Coin: w.c.name, Height: ci.Height, Hash: ci.BestHash,
			Source: w.label, At: time.Now(),
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
