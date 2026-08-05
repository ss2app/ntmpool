package dragonxrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// errNoLongPoll 节点 GBT 不带 longpollid = 不支持挂等。继续跑会退化成忙轮询
// 打爆节点，直接退出本通道（轮询兜底仍在，池不会瞎）。
var errNoLongPoll = errors.New("节点 GBT 无 longpollid（不支持 longpoll）")

// LongPoll GBT longpoll 通知器（adapter.Notifier）。
//
// 比 ZMQ 还快的原因：ZMQ 只告诉你「有新块」，池还要再发一次 GBT 拉模板；
// longpoll 是把 GBT 请求【预先挂在节点上】，节点链头一动，挂着的请求立即
// 带着新模板返回——感知与拿模板一步到位（docs/02 Notifier 谱系：
// embedded > ZMQ/push > longpoll > 短轮询；本实现挂等唤醒后仍走统一的
// Refresh→GetTemplate 路径重拉一次，内网 RTT 几 ms，换取管线单一无状态）。
//
// 假唤醒无害：dragonxd 约每 10min 会主动返回同高度模板（bitcoin 系 longpoll
// 惯例），上层 Refresh(force) 后 cnjob 按 JobKey 判定同工作、不打断矿工。
type LongPoll struct {
	c *Client
	// lphc 挂等专用 HTTP client：无总超时（挂等本身就是长时间连接），
	// 生命周期完全由 ctx 控制。与 c.hc（150s 超时）分开，绝不共用。
	lphc *http.Client
	// label 写进 TipEvent.Source。多节点竞速时每条通道给不同 label，
	// 日志才能看出「哪个节点先感知到新块」——同一高度两条日志的墙钟差
	// ＝纯粹的相对传播延迟（不含挖矿耗时，不依赖块头时间戳）。
	label string
}

// LongPollNotifier 返回本节点的 GBT longpoll 通知器（Source="longpoll"）。
func (c *Client) LongPollNotifier() *LongPoll {
	return c.LongPollNotifierLabeled("longpoll")
}

// LongPollNotifierLabeled 同 LongPollNotifier，但自定义 TipEvent.Source。
// 用于多节点竞速：每个节点一条通道、各自一个 label。
func (c *Client) LongPollNotifierLabeled(label string) *LongPoll {
	if label == "" {
		label = "longpoll"
	}
	return &LongPoll{c: c, lphc: &http.Client{}, label: label}
}

// Run 阻塞运行：循环挂等 GBT，链头变化即发 TipEvent。失败退避重试（自带重连）。
func (n *LongPoll) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	for ctx.Err() == nil {
		height, hash, err := n.waitTip(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errNoLongPoll) {
				log.Printf("[%s] %s 通道退出：%v（轮询兜底继续）", n.c.name, n.label, err)
				return err
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		ev := core.TipEvent{Coin: n.c.name, Height: height, Hash: hash, Source: n.label, At: time.Now()}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// waitTip 一轮挂等：有 longpollid 就挂到链头变化；还没有（进程刚启动、
// GetTemplate 尚未跑过）就普通 GBT 立即返回先取一个 id（多发的那个事件
// 经上层 Refresh(force)+JobKey 去重，无害）。返回新 tip 的高度与前块 hash。
func (n *LongPoll) waitTip(ctx context.Context) (uint64, string, error) {
	n.c.mu.Lock()
	lpid := n.c.lastLongPollID
	n.c.mu.Unlock()

	params := []any{map[string]any{}}
	if lpid != "" {
		params = []any{map[string]any{"longpollid": lpid}}
	}
	var t struct {
		Height            uint64 `json:"height"`
		PreviousBlockHash string `json:"previousblockhash"`
		LongPollID        string `json:"longpollid"`
	}
	if err := n.callLP(ctx, params, &t); err != nil {
		return 0, "", err
	}
	if t.LongPollID == "" {
		return 0, "", errNoLongPoll
	}
	n.c.mu.Lock()
	n.c.lastLongPollID = t.LongPollID
	n.c.mu.Unlock()
	return t.Height, t.PreviousBlockHash, nil
}

// callLP 与 Client.call 同构，但走无超时的挂等 client。
func (n *LongPoll) callLP(ctx context.Context, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0", "id": "ntmpool-lp", "method": "getblocktemplate", "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.c.user != "" {
		req.SetBasicAuth(n.c.user, n.c.pass)
	}
	resp, err := n.lphc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("longpoll 响应解析失败 (http %d): %w", resp.StatusCode, err)
	}
	if env.Error != nil {
		return env.Error
	}
	return json.Unmarshal(env.Result, out)
}
