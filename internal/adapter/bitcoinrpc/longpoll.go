package bitcoinrpc

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
// 打爆节点，直接退出本通道（2s 轮询兜底仍在，池不会瞎）。
var errNoLongPoll = errors.New("节点 GBT 无 longpollid（不支持 longpoll）")

// LongPoll GBT longpoll 通知器（adapter.Notifier），标准 bitcoin 系通用。
//
// 比 ZMQ 少一次拉取 RTT：ZMQ 只说「有新块」、池还得再发一次 GBT；longpoll 把 GBT
// 请求预先挂在节点上，链头一动挂着的请求立即带着新模板返回（感知+拿模板一步到位）。
// 唤醒后仍走统一 Refresh→GetTemplate 重拉一次（内网 RTT 几 ms，换管线单一无状态）。
// 假唤醒无害：bitcoin 系约每 ~1min 会主动返回同高度模板，上层 Refresh(force)+cnjob
// JobKey 判同工作、不打断矿工。与 ZMQ 并存时 coininstance 取最先到者。
type LongPoll struct {
	c *Client
	// lphc 挂等专用 HTTP client：无总超时（挂等本身就是长连接），生命周期由 ctx 控制。
	lphc *http.Client
}

// LongPollNotifier 返回本节点的 GBT longpoll 通知器。
func (c *Client) LongPollNotifier() *LongPoll {
	return &LongPoll{c: c, lphc: &http.Client{}}
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
				log.Printf("[%s] longpoll 通道退出：%v（2s 轮询兜底继续）", n.c.name, err)
				return err
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		ev := core.TipEvent{Coin: n.c.name, Height: height, Hash: hash, Source: "longpoll", At: time.Now()}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// waitTip 一轮挂等：有 longpollid 就挂到链头变化；还没有（刚启动、GetTemplate 尚未跑过）
// 就普通 GBT 立即返回先取一个 id（多发的那个事件经上层 Refresh(force)+JobKey 去重，无害）。
func (n *LongPoll) waitTip(ctx context.Context) (uint64, string, error) {
	n.c.mu.Lock()
	lpid := n.c.lastLongPollID
	n.c.mu.Unlock()

	p := map[string]any{"rules": n.c.gbtRules}
	if lpid != "" {
		p["longpollid"] = lpid
	}
	var t struct {
		Height            uint64 `json:"height"`
		PreviousBlockHash string `json:"previousblockhash"`
		LongPollID        string `json:"longpollid"`
	}
	if err := n.callLP(ctx, []any{p}, &t); err != nil {
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
