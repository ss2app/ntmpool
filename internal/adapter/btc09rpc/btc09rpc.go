// Package btc09rpc 对接 Bitcoin 09 (09C) 的 poolnode 守护
// （coins/bitcoin09/poolnode/：内嵌全节点 + loopback HTTP 矿池 API）。
//
// 路由契约（poolnode api.go 是唯一事实源）：
//
//	GET  /status                       {height, tip, peers, mempool, bits, balance}
//	GET  /mining/template[?longpoll_id=&timeout=]
//	                                   {template_id, height, header(88B hex, nonce=0),
//	                                    target(64hex BE), bits, reward(币串), prev, longpoll_id}
//	POST /mining/submit                {template_id, nonce} → {status, hash, reason}
//	GET  /blocks/{height}              {height, hash, time, confirmations}
//	GET  /tx/{txid}                    {confirmations, height|mempool}
//	GET  /wallet/balance               {balance, address}
//	POST /wallet/sendmany              {outputs:{addr:币串}, fee?} → {txid}
//	GET  /networkhashps                {hashps}   ← 真值口径（120 块真实时间跨度）
package btc09rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
)

// AlgoName 下发矿工/hasher 注册表共用的算法名。
const AlgoName = "argon2id-btc09"

type Client struct {
	name string
	base string
	hc   *http.Client

	mu             sync.Mutex
	lastLongPollID string
}

func New(name, base string) *Client {
	return &Client{
		name: name, base: strings.TrimRight(base, "/"),
		hc: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

// ---- HTTP 底座（无状态可重连，pitfall C9）----

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	return c.do(c.hc, req, path, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(c.hc, req, path, out)
}

func (c *Client) do(hc *http.Client, req *http.Request, path string, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: http %d: %s", c.name, path, resp.StatusCode, trim(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: 非法响应: %w", c.name, path, err)
		}
	}
	return nil
}

var errNotFound = fmt.Errorf("not found")

func trim(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---- NodeAdapter ----

func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	var s struct {
		Height uint64 `json:"height"`
		Tip    string `json:"tip"`
	}
	if err := c.get(ctx, "/status", &s); err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{Height: s.Height, TipHash: s.Tip, Synced: true, Connected: true}, nil
}

type templateResp struct {
	TemplateID string `json:"template_id"`
	Height     uint64 `json:"height"`
	Header     string `json:"header"`
	Target     string `json:"target"`
	Reward     string `json:"reward"`
	Prev       string `json:"prev"`
	LongPollID string `json:"longpoll_id"`
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	var t templateResp
	if err := c.get(ctx, "/mining/template", &t); err != nil {
		return nil, err
	}
	return c.templateFromResp(&t)
}

func (c *Client) templateFromResp(t *templateResp) (*adapter.BlockTemplate, error) {
	header, err := hex.DecodeString(t.Header)
	if err != nil || len(header) != 88 {
		return nil, fmt.Errorf("%s: 模板 header 非 88 字节 (len=%d, err=%v)", c.name, len(header), err)
	}
	target, ok := new(big.Int).SetString(t.Target, 16)
	if !ok || target.Sign() <= 0 {
		return nil, fmt.Errorf("%s: 模板 target 非法 %q", c.name, t.Target)
	}
	if t.TemplateID == "" {
		return nil, fmt.Errorf("%s: 模板缺 template_id", c.name)
	}
	c.mu.Lock()
	c.lastLongPollID = t.LongPollID
	c.mu.Unlock()

	work := &adapter.BlobWork{
		HashingBlob: header,
		NonceOffset: 80,
		NonceLen:    8,
		SearchLen:   5, // 语义占位；btc09job 用 44-bit 窗口模型，不走字节分片
		Algo:        AlgoName,

		NetworkTarget: target,
		HashBigEndian: true,

		HeightHint: t.Height,
		JobKey:     t.TemplateID,
		SubmitRef:  t.TemplateID,
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.Prev,
		NetworkTarget: t.Target,
		CoinbaseValue: t.Reward, // poolnode 已归一为十进制币串；空块也带 subsidy（pitfall C5）
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// SubmitBlock（NodeAdapter 形状）：blob 家族实际走 SubmitBlob。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	sol, ok := raw.(*adapter.BlobSolution)
	if !ok {
		return fmt.Errorf("%s: SubmitBlock 只接受 *BlobSolution", c.name)
	}
	_, err := c.SubmitBlob(ctx, sol)
	return err
}

// ---- BlobSubmitter ----

func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	tid, _ := sol.Work.SubmitRef.(string)
	if tid == "" {
		return "", fmt.Errorf("%s: 模板缺 template_id", c.name)
	}
	var resp struct {
		Status string `json:"status"`
		Hash   string `json:"hash"`
		Reason string `json:"reason"`
	}
	if err := c.post(ctx, "/mining/submit", map[string]any{
		"template_id": tid, "nonce": sol.Nonce,
	}, &resp); err != nil {
		return "", err
	}
	if !strings.EqualFold(resp.Status, "accepted") {
		reason := resp.Reason
		if reason == "" {
			reason = resp.Status
		}
		return "", fmt.Errorf("%s: 提交被拒: %s", c.name, reason)
	}
	return resp.Hash, nil
}

// ---- 分类器（payout.NodeClassifier）----

type blockResp struct {
	Hash          string `json:"hash"`
	Height        uint64 `json:"height"`
	Confirmations int64  `json:"confirmations"`
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var b blockResp
	if err := c.get(ctx, fmt.Sprintf("/blocks/%d", height), &b); err != nil {
		return "", err
	}
	return b.Hash, nil
}

// Confirmations 孤块判定铁律：按高度取主链块、hash 逐字节比对。
// 404 = 主链没到这个高度/已回滚；hash 不一致 = 该高度主链块不是我们的 → 孤块。
func (c *Client) Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error) {
	var b blockResp
	if err := c.get(ctx, fmt.Sprintf("/blocks/%d", height), &b); err != nil {
		if err == errNotFound {
			return -1, nil
		}
		return 0, err
	}
	if !strings.EqualFold(b.Hash, blockHash) {
		return -1, nil
	}
	return b.Confirmations, nil
}

// ---- WalletAdapter ----

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	var b struct {
		Balance string `json:"balance"`
	}
	if err := c.get(ctx, "/wallet/balance", &b); err != nil {
		return "", err
	}
	return b.Balance, nil
}

func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	var r struct {
		TxID  string `json:"txid"`
		Error string `json:"error"`
	}
	if err := c.post(ctx, "/wallet/sendmany", map[string]any{"outputs": outputs}, &r); err != nil {
		return "", err
	}
	if r.TxID == "" {
		return "", fmt.Errorf("%s: sendmany 无 txid: %s", c.name, r.Error)
	}
	return r.TxID, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var r struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.get(ctx, "/tx/"+txid, &r); err != nil {
		return 0, err
	}
	return r.Confirmations, nil
}

// ---- HashPSSource（真值口径：poolnode 按 120 块真实时间跨度计算）----

func (c *Client) NetworkHashPS(ctx context.Context) (float64, error) {
	var r struct {
		HashPS float64 `json:"hashps"`
	}
	if err := c.get(ctx, "/networkhashps", &r); err != nil {
		return 0, err
	}
	return r.HashPS, nil
}

// ---- Notifier：模板 longpoll（挂等在 poolnode 上，链头一动带新模板返回）----

type LongPoll struct {
	c    *Client
	lphc *http.Client // 挂等专用：无总超时，生命周期由 ctx 控制
}

func (c *Client) LongPollNotifier() *LongPoll {
	return &LongPoll{c: c, lphc: &http.Client{}}
}

func (n *LongPoll) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	down := false
	for ctx.Err() == nil {
		n.c.mu.Lock()
		lpid := n.c.lastLongPollID
		n.c.mu.Unlock()

		path := "/mining/template"
		if lpid != "" {
			path = fmt.Sprintf("/mining/template?longpoll_id=%s&timeout=55", lpid)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.c.base+path, nil)
		if err != nil {
			return err
		}
		var t templateResp
		if err := n.c.do(n.lphc, req, path, &t); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !down {
				down = true
				log.Printf("[%s] longpoll 通道不可用（静默退避重连）: %v", n.c.name, err)
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if down {
			down = false
			log.Printf("[%s] longpoll 通道已恢复", n.c.name)
		}
		n.c.mu.Lock()
		same := t.LongPollID == lpid && lpid != ""
		n.c.lastLongPollID = t.LongPollID
		n.c.mu.Unlock()
		if same {
			continue // 挂等超时返回同一模板：无事发生
		}
		ev := core.TipEvent{Coin: n.c.name, Height: t.Height, Hash: t.Prev, Source: "longpoll", At: time.Now()}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// 编译期断言。
var (
	_ adapter.NodeAdapter   = (*Client)(nil)
	_ adapter.BlobSubmitter = (*Client)(nil)
	_ adapter.WalletAdapter = (*Client)(nil)
	_ adapter.HashPSSource  = (*Client)(nil)
	_ adapter.Notifier      = (*LongPoll)(nil)
)
