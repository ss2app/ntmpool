// Package domrpc 对接 DOM 节点的矿池挖矿 JSON-RPC，以及既有只读 HTTP RPC。
//
// ntm_get_work / ntm_submit_work 使用 JSON-RPC 2.0 + Bearer token；token 由
// DOM_RPC_TOKEN、节点显式配置或 ~/.dom/rpc_token 产生，矿池通过 nodes[0].pass
// 注入同一个值。/status、/block、/chain/scan 是 DOM 节点既有只读 HTTP 面，
// 用于健康检查、孤块判定和爆块后的权威 reward（subsidy + fees）回填。
package domrpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

const (
	PreimageLen         = 224
	NonceOffset         = 216
	NonceLen            = 8
	TargetCompactOffset = 180
	workPollInterval    = 200 * time.Millisecond
	domDecimals         = 8
	halvingInterval     = uint64(330_000)
	initialRewardNoms   = uint64(3_300_000_000)
)

// Work 是 ntm_get_work 的已校验、归一化结果。
type Work struct {
	JobID         string
	Height        uint64
	PrevHash      string
	Preimage      []byte
	SeedHash      string
	NextSeedHash  string // 空字符串表示 JSON null
	TargetHex     string
	NetworkTarget *big.Int
	TargetCompact uint32
}

// SubmitResult 是 ntm_submit_work 的 result。
type SubmitResult struct {
	Accepted  bool    `json:"accepted"`
	BlockHash string  `json:"block_hash"`
	Height    uint64  `json:"height"`
	Error     *string `json:"error"`
}

type getWorkResult struct {
	JobID         string  `json:"job_id"`
	Height        uint64  `json:"height"`
	PrevHash      string  `json:"prev_hash"`
	Preimage      string  `json:"preimage"`
	SeedHash      string  `json:"seed_hash"`
	NextSeedHash  *string `json:"next_seed_hash"`
	Target        string  `json:"target"`
	TargetCompact uint32  `json:"target_compact"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// Client 同时实现 coininstance 所需的 status/classifier/reward source。
type Client struct {
	name  string
	base  string
	token string
	http  *http.Client
	seq   atomic.Uint64

	pollMu   sync.Mutex
	lastPoll time.Time
}

// New 创建 DOM 客户端。token 不能为空：挖矿 RPC 是 Bearer 认证面，空 token
// 必须启动即失败，不能等到线上持续 401 才暴露配置错误。
func New(name, rawURL, token string) (*Client, error) {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if rawURL == "" {
		return nil, fmt.Errorf("[%s] DOM RPC URL 为空", name)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("[%s] DOM RPC URL 非法 %q", name, rawURL)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("[%s] DOM RPC Bearer token 为空（nodes[0].pass 应注入 DOM_RPC_TOKEN 或 ~/.dom/rpc_token 内容）", name)
	}
	return &Client{
		name: name, base: rawURL, token: token,
		http: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *Client) Name() string { return c.name }

// GetWork 按契约保证相邻 ntm_get_work 请求起点至少相隔 200ms；即使上层并发
// force refresh，也不会把节点打成低于契约的高频轮询。
func (c *Client) GetWork(ctx context.Context) (*Work, error) {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	if wait := workPollInterval - time.Since(c.lastPoll); !c.lastPoll.IsZero() && wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	c.lastPoll = time.Now()

	var raw getWorkResult
	if err := c.call(ctx, "ntm_get_work", map[string]any{}, &raw); err != nil {
		return nil, err
	}
	return normalizeWork(&raw)
}

// SubmitWork 提交 nonce 原始 8 字节的 hex（即 preimage[216:224]）。
func (c *Client) SubmitWork(ctx context.Context, jobID, nonceHex string) (*SubmitResult, error) {
	jobID = strings.ToLower(strings.TrimSpace(jobID))
	nonceHex = strings.ToLower(strings.TrimSpace(nonceHex))
	if !validHexLen(jobID, 8) {
		return nil, fmt.Errorf("DOM job_id 必须是 8 字节 hex")
	}
	if !validHexLen(nonceHex, NonceLen) {
		return nil, fmt.Errorf("DOM nonce 必须是 8 字节 hex")
	}
	var out SubmitResult
	if err := c.call(ctx, "ntm_submit_work", map[string]string{
		"job_id": jobID,
		"nonce":  nonceHex,
	}, &out); err != nil {
		return nil, err
	}
	if out.BlockHash != "" && !validHexLen(out.BlockHash, 32) {
		return nil, fmt.Errorf("ntm_submit_work 返回非法 block_hash %q", out.BlockHash)
	}
	out.BlockHash = strings.ToLower(out.BlockHash)
	return &out, nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.seq.Add(1)
	reqBody := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("[%s] DOM RPC %s: %w", c.name, method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("[%s] DOM RPC %s 读响应: %w", c.name, method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("[%s] DOM RPC %s HTTP %d: %s", c.name, method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope rpcResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("[%s] DOM RPC %s 响应非 JSON: %w", c.name, method, err)
	}
	if envelope.JSONRPC != "2.0" {
		return fmt.Errorf("[%s] DOM RPC %s 响应 jsonrpc=%q", c.name, method, envelope.JSONRPC)
	}
	if envelope.Error != nil {
		return fmt.Errorf("[%s] DOM RPC %s error %d: %s", c.name, method, envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return fmt.Errorf("[%s] DOM RPC %s 缺少 result", c.name, method)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("[%s] DOM RPC %s result 解码: %w", c.name, method, err)
	}
	return nil
}

func normalizeWork(raw *getWorkResult) (*Work, error) {
	jobID := strings.ToLower(strings.TrimSpace(raw.JobID))
	prevHash := strings.ToLower(strings.TrimSpace(raw.PrevHash))
	seedHash := strings.ToLower(strings.TrimSpace(raw.SeedHash))
	targetHex := strings.ToLower(strings.TrimSpace(raw.Target))
	if !validHexLen(jobID, 8) {
		return nil, fmt.Errorf("ntm_get_work job_id 必须是 8 字节 hex")
	}
	if !validHexLen(prevHash, 32) {
		return nil, fmt.Errorf("ntm_get_work prev_hash 必须是 32 字节 hex")
	}
	if !validHexLen(seedHash, 32) {
		return nil, fmt.Errorf("ntm_get_work seed_hash 必须是 32 字节 hex")
	}
	preimage, err := hex.DecodeString(strings.TrimSpace(raw.Preimage))
	if err != nil || len(preimage) != PreimageLen {
		return nil, fmt.Errorf("ntm_get_work preimage 必须是 %d 字节 hex", PreimageLen)
	}
	if !allZero(preimage[NonceOffset : NonceOffset+NonceLen]) {
		return nil, fmt.Errorf("ntm_get_work preimage nonce 区 [%d:%d] 未置零", NonceOffset, NonceOffset+NonceLen)
	}
	if got := binary.LittleEndian.Uint32(preimage[TargetCompactOffset : TargetCompactOffset+4]); got != raw.TargetCompact {
		return nil, fmt.Errorf("ntm_get_work target_compact=%08x 与 preimage=%08x 不一致", raw.TargetCompact, got)
	}
	if !validHexLen(targetHex, 32) {
		return nil, fmt.Errorf("ntm_get_work target 必须是 32 字节 BE hex")
	}
	targetBytes, _ := hex.DecodeString(targetHex)
	target := new(big.Int).SetBytes(targetBytes)
	if target.Sign() <= 0 {
		return nil, fmt.Errorf("ntm_get_work target 必须大于 0")
	}
	expanded, err := expandCompact(raw.TargetCompact)
	if err != nil {
		return nil, fmt.Errorf("ntm_get_work target_compact 非法: %w", err)
	}
	if expanded.Cmp(target) != 0 {
		return nil, fmt.Errorf("ntm_get_work target 与 compact 展开值不一致")
	}
	next := ""
	if raw.NextSeedHash != nil {
		next = strings.ToLower(strings.TrimSpace(*raw.NextSeedHash))
		if !validHexLen(next, 32) {
			return nil, fmt.Errorf("ntm_get_work next_seed_hash 必须是 null 或 32 字节 hex")
		}
	}
	return &Work{
		JobID: jobID, Height: raw.Height,
		PrevHash: prevHash, Preimage: preimage,
		SeedHash: seedHash, NextSeedHash: next,
		TargetHex: targetHex, NetworkTarget: target,
		TargetCompact: raw.TargetCompact,
	}, nil
}

// expandCompact 逐字节复现 dom-pow CompactTarget::to_target。源码虽称
// Bitcoin-style，但 mantissa 的低字节先写入 BE target 的高位位置；不能使用
// Bitcoin Core 常见的 mantissa<<8*(exponent-3) 整数公式，否则会拒绝合法 DOM work。
func expandCompact(compact uint32) (*big.Int, error) {
	if compact&0x00800000 != 0 {
		return nil, fmt.Errorf("负数符号位已置位")
	}
	exponent := int(compact >> 24)
	mantissa := compact & 0x007fffff
	if mantissa == 0 {
		return nil, fmt.Errorf("mantissa 为 0")
	}
	if exponent > 32 {
		return nil, fmt.Errorf("exponent %d > 32", exponent)
	}
	var target [32]byte
	if exponent >= 1 {
		target[32-exponent] = byte(mantissa)
	}
	if exponent >= 2 {
		target[33-exponent] = byte(mantissa >> 8)
	}
	if exponent >= 3 {
		target[34-exponent] = byte(mantissa >> 16)
	}
	t := new(big.Int).SetBytes(target[:])
	// dom-core MIN_TARGET_BYTES / MAX_TARGET_BYTES 的精确整数边界。
	minTarget := new(big.Int).Lsh(big.NewInt(0xffff), 32)
	maxTarget := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 240), big.NewInt(1))
	if t.Cmp(minTarget) < 0 || t.Cmp(maxTarget) > 0 {
		return nil, fmt.Errorf("展开后超出 DOM 共识 target 边界")
	}
	return t, nil
}

func validHexLen(s string, n int) bool {
	s = strings.TrimSpace(s)
	if len(s) != n*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

type statusResponse struct {
	ChainHeight uint64 `json:"chain_height"`
}

type blockHeaderResponse struct {
	Height uint64 `json:"height"`
	Hash   string `json:"hash"`
}

func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	var s statusResponse
	if err := c.get(ctx, "/status", &s); err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{Height: s.ChainHeight, Connected: true, Synced: true}, nil
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	h, err := c.block(ctx, strconv.FormatUint(height, 10))
	if err != nil {
		return "", err
	}
	return strings.ToLower(h.Hash), nil
}

func (c *Client) Confirmations(ctx context.Context, blockHash string, _ uint64) (int64, error) {
	h, err := c.block(ctx, blockHash)
	if err != nil {
		if isNotFound(err) {
			return -1, nil
		}
		return 0, err
	}
	canonical, err := c.BlockHashAt(ctx, h.Height)
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(canonical, blockHash) {
		return -1, nil
	}
	status, err := c.Status(ctx)
	if err != nil {
		return 0, err
	}
	if status.Height < h.Height {
		return 0, nil
	}
	return int64(status.Height-h.Height) + 1, nil
}

func (c *Client) block(ctx context.Context, heightOrHash string) (*blockHeaderResponse, error) {
	var h blockHeaderResponse
	if err := c.get(ctx, "/block/"+url.PathEscape(heightOrHash), &h); err != nil {
		return nil, err
	}
	if !validHexLen(h.Hash, 32) {
		return nil, fmt.Errorf("DOM /block 返回非法 hash %q", h.Hash)
	}
	return &h, nil
}

type scanResponse struct {
	Blocks []struct {
		Height uint64 `json:"height"`
		Hash   string `json:"hash"`
		Fees   uint64 `json:"fees"`
	} `json:"blocks"`
}

// BlockReward 返回权威块 subsidy + fees。用于 blockSink 在 ntm_submit_work
// accepted 后回填 RewardPending，避免只记补贴、漏掉 Mimblewimble 模板交易费。
func (c *Client) BlockReward(ctx context.Context, blockHash string) (string, error) {
	h, err := c.block(ctx, blockHash)
	if err != nil {
		return "", err
	}
	var scan scanResponse
	path := "/chain/scan?from=" + strconv.FormatUint(h.Height, 10) + "&to=" + strconv.FormatUint(h.Height, 10)
	if err := c.get(ctx, path, &scan); err != nil {
		return "", err
	}
	for _, b := range scan.Blocks {
		if b.Height == h.Height && strings.EqualFold(b.Hash, blockHash) {
			reward, ok := addUint64(blockSubsidyNoms(h.Height), b.Fees)
			if !ok {
				return "", fmt.Errorf("DOM block reward 溢出")
			}
			return formatNoms(reward), nil
		}
	}
	return "", fmt.Errorf("DOM /chain/scan 未返回权威块 %s", blockHash)
}

func blockSubsidyNoms(height uint64) uint64 {
	epoch := height / halvingInterval
	reward := initialRewardNoms
	for i := uint64(0); i < epoch && reward > 0; i++ {
		reward = reward * 67 / 100
	}
	return reward
}

func formatNoms(n uint64) string {
	const unit = uint64(100_000_000)
	return fmt.Sprintf("%d.%0*d", n/unit, domDecimals, n%unit)
}

func addUint64(a, b uint64) (uint64, bool) {
	s := a + b
	return s, s >= a
}

type httpStatusError struct {
	code int
	body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.body)
}

func isNotFound(err error) bool {
	e, ok := err.(*httpStatusError)
	return ok && e.code == http.StatusNotFound
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	// 既有只读面当前公开；仍带 Bearer 可兼容节点后续收紧访问控制。
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("[%s] DOM GET %s: %w", c.name, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("[%s] DOM GET %s 解码: %w", c.name, path, err)
	}
	return nil
}
