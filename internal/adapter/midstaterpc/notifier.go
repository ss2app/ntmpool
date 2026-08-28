package midstaterpc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// defaultTipPollInterval tip 通道探测间隔。
//
// 取值依据：midstate 目标出块 60s，coininstance 主循环 2s 轮询兜底 → 最坏
// 情况矿工在旧 job 上白挖 2s+模板生成 ~0.5s ≈ 2.5s，占一个块周期的 ~4%。
// 300ms 把这段压到 ~0.8s（≈1.3%）。再快收益递减（模板生成本身就要 ~0.5s），
// 徒增节点请求。
const defaultTipPollInterval = 300 * time.Millisecond

// TipNotifier midstate 新块事件源（实现 adapter.Notifier）。
//
// 为什么是「高频轮询」而不是真推送：midstate 节点没有 ZMQ pub，也没有
// bitcoin 系的 longpollid（GBT 阻塞返回）——rpc/server.rs 的路由表里
// /state 就是个普通 GET，唯一的推送式接口是 WebRTC light_protocol，
// 那是给轻客户端用的、接入代价远超收益。所以这里用「高频探 + 变化才投递」
// 实现推送语义：对上层 coininstance 而言它和 ZMQ 通道行为一致
// （只在 tip 真变了才触发 force refresh），只是发现延迟由探测间隔决定。
//
// /state 是节点最轻的端点（实测 latency=0ms，纯内存读 State 结构），
// 3~4 次/秒对节点无压力；池本来就在以 2s 打同一个端点。
//
// 铁律：本通道只做加速，coininstance 的 2s 轮询兜底永远保留——推送断了
// 池不能瞎（docs/02）。所以这里所有错误都只做降噪日志、不向上抛致命错，
// Run 只在 ctx 取消时返回。
type TipNotifier struct {
	c        *Client
	interval time.Duration
}

// TipNotifier 构造 tip 通道；interval<=0 用默认值。
func (c *Client) TipNotifier(interval time.Duration) *TipNotifier {
	if interval <= 0 {
		interval = defaultTipPollInterval
	}
	return &TipNotifier{c: c, interval: interval}
}

// Run 阻塞运行，把 tip 变化写入 ch；ctx 取消时退出。
func (n *TipNotifier) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	t := time.NewTicker(n.interval)
	defer t.Stop()

	var lastKey string
	var failStreak int

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}

		st, err := n.c.State(ctx)
		if err != nil {
			failStreak++
			// 降噪：3~4 次/秒的通道一旦节点抖动就会刷屏，而 2s 轮询兜底
			// 仍在跑（失联告警由 coininstance 的健康检测统一发），这里只在
			// 首次和每 ~30s 各记一条。
			if failStreak == 1 || failStreak%100 == 0 {
				log.Printf("[%s] tip 通道读 /state 失败(连续 %d 次): %v", n.c.name, failStreak, err)
			}
			continue
		}
		if failStreak > 0 {
			log.Printf("[%s] tip 通道已恢复(此前连续失败 %d 次)", n.c.name, failStreak)
			failStreak = 0
		}

		// 补链中 tip 无意义：midjob.Refresh 本来就会在 is_syncing 时暂停模板，
		// 这里提前跳过，避免拿过时高度去触发一轮无用的 force refresh。
		if st.IsSyncing {
			continue
		}

		key := fmt.Sprintf("%d|%s", st.Height, strings.ToLower(st.HeaderHash))
		if key == lastKey {
			continue
		}
		// 首轮只建基线不投递：池启动时 coininstance 已经做过一次 Refresh，
		// 这里再投一次纯属重复。
		if lastKey == "" {
			lastKey = key
			continue
		}
		lastKey = key

		select {
		case ch <- core.TipEvent{
			Coin:   n.c.name,
			Height: st.Height,
			Hash:   st.HeaderHash,
			Source: "midstate-tip",
			At:     time.Now(),
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
