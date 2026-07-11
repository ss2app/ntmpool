// Package midstaterpc 对接 midstate 官方节点的 HTTP RPC（纯官方 v3.5.1，
// 无私有 patch；契约事实源 = 节点 src/rpc/{server,handlers,types}.rs +
// fork 池 midstate-pool/src/main.rs node_request，见 docs/07）。
//
// 路由契约：
//
//	GET  /state           {height, target(hex64), block_reward(u64 units),
//	                       header_hash(hex64), is_syncing, ...}
//	POST /block_template  {"coinbase":[{address:hex64,value:u64(2 的幂),salt:hex64},…]}
//	                      → 200 {mining_midstate, target, batch_template(Batch JSON),
//	                             total_fees, block_reward}
//	                      → 4xx {"error":"Coinbase mismatch. Expected: <N>"}（重建重试）
//	POST /submit_batch    batch_template 原样 + extension 替换 → {accepted:true}
//	GET  /block/{h}       该高度 Batch JSON；块身份比对 = extension.final_hash
//
// midstate 特殊点：模板流是「池先给 coinbase 分账 → 节点给挖矿 hash」，
// 与 GBT 系相反，所以不实现 adapter.NodeAdapter 的 GetTemplate/SubmitBlock，
// midjob 直接消费本包的强类型 API。
package midstaterpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

// AlgoName hasher 注册表算法名。
const AlgoName = "midvdf"

type Client struct {
	name string
	base string
	hc   *http.Client
}

func New(name, base string) *Client {
	return &Client{
		name: name, base: strings.TrimRight(base, "/"),
		hc: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

// ---- HTTP 底座（无状态短连接，节点重启下一请求自愈，pitfall C9）----

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, path, out)
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
	return c.do(req, path, out)
}

// nodeError 节点 4xx 的 {"error":"…"} 信封。
type nodeError struct {
	path string
	code int
	msg  string
}

func (e *nodeError) Error() string {
	return fmt.Sprintf("midstate %s: http %d: %s", e.path, e.code, e.msg)
}

func (c *Client) do(req *http.Request, path string, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		msg := envelope.Error
		if msg == "" {
			msg = trim(raw)
		}
		return &nodeError{path: path, code: resp.StatusCode, msg: msg}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: 非法响应: %w", c.name, path, err)
		}
	}
	return nil
}

func trim(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---- /state ----

// State /state 里池要用的字段。
type State struct {
	Height      uint64 `json:"height"`
	Target      string `json:"target"`       // hex64 大端
	BlockReward uint64 `json:"block_reward"` // units
	HeaderHash  string `json:"header_hash"`  // hex64；模板重切触发之一
	IsSyncing   bool   `json:"is_syncing"`   // true = 暂停模板（上游明确要求）
}

func (c *Client) State(ctx context.Context) (State, error) {
	var s State
	err := c.get(ctx, "/state", &s)
	return s, err
}

// Status 节点健康面（coininstance 失联检测/高度轮询）。
func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	s, err := c.State(ctx)
	if err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{
		Height: s.Height, TipHash: s.HeaderHash,
		Synced: !s.IsSyncing, Connected: true,
	}, nil
}

// ---- /block_template ----

// CoinbaseOut 模板请求里的一个 coinbase 输出（value 必须非零 2 的幂）。
type CoinbaseOut struct {
	Address string `json:"address"` // hex64
	Value   uint64 `json:"value"`
	Salt    string `json:"salt"` // hex64
}

// Template /block_template 200 响应。BatchTemplate 原样保存（提交时只换 extension，
// 绝不重编码——节点的 Batch JSON 对我们是黑盒）。
type Template struct {
	MiningMidstate string          `json:"mining_midstate"` // hex64；矿工 grind 的就是它
	Target         string          `json:"target"`          // hex64 网络目标
	BatchTemplate  json.RawMessage `json:"batch_template"`
	TotalFees      uint64          `json:"total_fees"`
	BlockReward    uint64          `json:"block_reward"`
}

// MismatchError coinbase 总额与节点期望不符（reward+mempool fees 变了）；
// 调用方按 Expected 重建分账重试（fork 池同款，≤4 次收敛）。
type MismatchError struct {
	Expected uint64
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("coinbase 总额不符，节点期望 %d", e.Expected)
}

func (c *Client) BlockTemplate(ctx context.Context, coinbase []CoinbaseOut) (*Template, error) {
	var t Template
	err := c.post(ctx, "/block_template", map[string]any{"coinbase": coinbase}, &t)
	if err != nil {
		var ne *nodeError
		if errors.As(err, &ne) {
			// "Coinbase mismatch. Expected: <N>"（node.rs finish_template 逐字）
			if n, ok := parseExpected(ne.msg); ok {
				return nil, &MismatchError{Expected: n}
			}
		}
		return nil, err
	}
	if t.MiningMidstate == "" || t.Target == "" || len(t.BatchTemplate) == 0 {
		return nil, fmt.Errorf("%s /block_template: 响应缺字段", c.name)
	}
	return &t, nil
}

func parseExpected(msg string) (uint64, bool) {
	const marker = "Expected: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.ParseUint(rest[:end], 10, 64)
	return n, err == nil && end > 0
}

// ---- /submit_batch ----

// SubmitBatch 用缓存的 batch_template 拼完整块提交：只替换顶层 "extension" 为
// {"nonce":<u64>,"final_hash":[32 字节数组]}（fork 池 recomputed.to_vec() 同款编码），
// 其余字节原样透传。返回错误时注意：「Block validation timed out」是已知假阴性
// （节点可能已 apply+广播），调用方留给分类器按链归位，绝不重交（memory 教训）。
func (c *Client) SubmitBatch(ctx context.Context, batchTemplate json.RawMessage, nonce uint64, finalHash []byte) error {
	if len(finalHash) != 32 {
		return fmt.Errorf("final_hash 长度 %d ≠ 32", len(finalHash))
	}
	var batch map[string]json.RawMessage
	if err := json.Unmarshal(batchTemplate, &batch); err != nil {
		return fmt.Errorf("batch_template 非法: %w", err)
	}
	var fh [32]uint8 // 定长数组 → JSON 数字数组（非 base64——Rust [u8;32] serde 契约）
	copy(fh[:], finalHash)
	ext, err := json.Marshal(map[string]any{"nonce": nonce, "final_hash": fh})
	if err != nil {
		return err
	}
	batch["extension"] = ext
	var resp struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.post(ctx, "/submit_batch", batch, &resp); err != nil {
		return err
	}
	if !resp.Accepted {
		return fmt.Errorf("%s /submit_batch: 节点未受理", c.name)
	}
	return nil
}

// ---- 孤块分类（payout.NodeClassifier）----
//
// midstate 块身份 = hex(extension.final_hash)：header_hash 不含 extension（同模板
// 两个 nonce 解同 header_hash），final_hash 才每解唯一，且 /block/{h} 直接可比。

// BlockHashAt 主链该高度块的身份（= final_hash hex）。
func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var b struct {
		Extension struct {
			FinalHash [32]uint8 `json:"final_hash"`
		} `json:"extension"`
	}
	if err := c.get(ctx, "/block/"+strconv.FormatUint(height, 10), &b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b.Extension.FinalHash[:]), nil
}

// Confirmations 我们的块当前确认数；主链同高度不是我们的块 → -1（孤块）。
// ⚠ midstate 的 /state.height = 下一个块的高度（tip = height−1，2026-07-11 真链
// 实测 state=195392 时最新块在 195391）→ 确认数 = state.height − blockHeight
// （刚落地 = 1）。
func (c *Client) Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error) {
	s, err := c.State(ctx)
	if err != nil {
		return 0, err
	}
	if s.Height <= height {
		return 0, nil // 链还没收进该高度（提交在飞/停摆），继续等
	}
	mainHash, err := c.BlockHashAt(ctx, height)
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(mainHash, blockHash) {
		return -1, nil
	}
	return int64(s.Height - height), nil
}
