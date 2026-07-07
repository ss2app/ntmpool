// Package customhttp 自定义 REST 链适配器（zoka/CRB 先例形状）。
//
// 节点侧 API（zoka mining patch v1.6.0 的真实路由，diff 逐行核实）：
//
//	GET  /chain/height                    → {"height":N}
//	GET  /mining/template?address=A       → {"chain_id","height","prev_hash","pool_address",
//	                                         "timestamp","difficulty_bits","epoch_seed_hex",
//	                                         "blob_prefix_hex","template_id"}（无 reward 字段）
//	POST /mining/submit {template_id,nonce(u64)} → {"status":"accepted","height","hash"} |
//	                                         {"status":...,"reason":...}
//	GET  /blocks/{height}                 → {"hash","height","reward_atoms"}（无按 hash 查的路由，
//	                                         孤块判定 = 按我们记录的高度取主链块比 hash）
//
// 钱包侧（打款；zoka 真实打款是 send-from-seed 加密封套，接 zoka 打款时在此扩展）：
//
//	GET  /wallet/balance                  → {"balance_atoms":N}
//	POST /wallet/sendmany {outputs:{addr:atoms}} → {"txid":...}
//	GET  /wallet/tx?txid=T                → {"confirmations":N}
//
// blob 布局（CRB/zoka 先例）：blob_prefix_hex = 完整 PoW 输入、nonce 全零尾部 8 字节；
// 低 4 字节矿工搜索区、高 4 字节连接 tag。difficulty_bits = leading-zero-bits，
// target = 2^(256-bits)−1，hash 大端解释，矿工 target 用 64-hex 大端全量编码。
package customhttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

const nonceSize = 8

// Client 一条自定义 REST 链。同时实现 NodeAdapter / BlobSubmitter / WalletAdapter。
type Client struct {
	name        string
	base        string // 如 http://127.0.0.1:7100
	poolAddress string
	algo        string // 下发矿工的算法名（zoka = rx/0）
	decimals    int
	hc          *http.Client
}

var (
	_ adapter.NodeAdapter   = (*Client)(nil)
	_ adapter.BlobSubmitter = (*Client)(nil)
	_ adapter.WalletAdapter = (*Client)(nil)
)

func New(name, base, poolAddress, algo string, decimals int) *Client {
	if decimals <= 0 {
		decimals = 8
	}
	return &Client{
		name: name, base: strings.TrimRight(base, "/"),
		poolAddress: poolAddress, algo: algo, decimals: decimals,
		hc: &http.Client{Timeout: 150 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

// ---- HTTP 底座（无状态可重连，pitfall C9）----

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
	var h struct {
		Height uint64 `json:"height"`
	}
	if err := c.get(ctx, "/chain/height", &h); err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{Height: h.Height, Synced: true, Connected: true}, nil
}

type templateResp struct {
	Height         uint64 `json:"height"`
	PrevHash       string `json:"prev_hash"`
	DifficultyBits uint32 `json:"difficulty_bits"`
	EpochSeedHex   string `json:"epoch_seed_hex"`
	BlobPrefixHex  string `json:"blob_prefix_hex"`
	TemplateID     string `json:"template_id"`
	RewardAtoms    uint64 `json:"reward_atoms"` // 可选；0 = 链无此字段（记账 reward 空）
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	var t templateResp
	path := "/mining/template?address=" + url.QueryEscape(c.poolAddress)
	if err := c.get(ctx, path, &t); err != nil {
		return nil, err
	}
	blob, err := hexDecode(t.BlobPrefixHex)
	if err != nil || len(blob) <= nonceSize {
		return nil, fmt.Errorf("%s: 模板 blob 非法（len=%d, %v）", c.name, len(blob), err)
	}
	if t.DifficultyBits == 0 || t.DifficultyBits >= 256 {
		return nil, fmt.Errorf("%s: difficulty_bits=%d 非法", c.name, t.DifficultyBits)
	}
	// target = 2^(256-bits) − 1（leading-zero-bits 语义，hash 大端比较）
	target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256-uint(t.DifficultyBits)), big.NewInt(1))
	work := &adapter.BlobWork{
		HashingBlob:   blob,
		NonceOffset:   len(blob) - nonceSize,
		NonceLen:      nonceSize,
		SearchLen:     4,
		SeedHash:      t.EpochSeedHex,
		Algo:          c.algo,
		NetworkTarget: target,
		HashBigEndian: true,
		// zoka/CRB 惯例：64-hex 大端全量 target
		TargetCompactLE: false,
		HeightHint:      t.Height,
		SubmitRef:       t.TemplateID,
	}
	reward := ""
	if t.RewardAtoms > 0 {
		reward = adapter.AtomicToDecimal(t.RewardAtoms, c.decimals)
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.PrevHash,
		CoinbaseValue: reward,
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// SubmitBlock 兼容 NodeAdapter 面（上层 cnjob 走 SubmitBlob）。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	sol, ok := raw.(*adapter.BlobSolution)
	if !ok {
		return fmt.Errorf("customhttp.SubmitBlock: 期望 *BlobSolution, got %T", raw)
	}
	_, err := c.SubmitBlob(ctx, sol)
	return err
}

// SubmitBlob 提交爆块：POST {template_id, nonce}；节点返回权威块 hash。
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

type blockResp struct {
	Hash   string `json:"hash"`
	Height uint64 `json:"height"`
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var b blockResp
	if err := c.get(ctx, fmt.Sprintf("/blocks/%d", height), &b); err != nil {
		return "", err
	}
	return b.Hash, nil
}

// Confirmations 链上无按 hash 查块的路由 → 按我们记录的高度取主链块：
// hash 逐字节一致 = 在主链，conf = tip − height + 1；不一致/404 = 孤块（-1）。
func (c *Client) Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error) {
	var b blockResp
	if err := c.get(ctx, fmt.Sprintf("/blocks/%d", height), &b); err != nil {
		if err == errNotFound {
			return -1, nil // 主链还没到这个高度/已被回滚 → 我们的块不在主链
		}
		return 0, err
	}
	if !strings.EqualFold(b.Hash, blockHash) {
		return -1, nil // 该高度的主链块不是我们的 → 孤块
	}
	st, err := c.Status(ctx)
	if err != nil {
		return 0, err
	}
	if st.Height < height {
		return 0, nil
	}
	return int64(st.Height-height) + 1, nil
}

// ---- WalletAdapter ----

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	var r struct {
		BalanceAtoms uint64 `json:"balance_atoms"`
	}
	if err := c.get(ctx, "/wallet/balance", &r); err != nil {
		return "", err
	}
	return adapter.AtomicToDecimal(r.BalanceAtoms, c.decimals), nil
}

func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	atoms := make(map[string]uint64, len(outputs))
	for addr, amt := range outputs {
		v, err := adapter.DecimalToAtomic(amt, c.decimals)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		atoms[addr] = v
	}
	var r struct {
		TxID string `json:"txid"`
	}
	if err := c.post(ctx, "/wallet/sendmany", map[string]any{"outputs": atoms}, &r); err != nil {
		return "", err
	}
	if r.TxID == "" {
		return "", fmt.Errorf("%s: sendmany 未返回 txid", c.name)
	}
	return r.TxID, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var r struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.get(ctx, "/wallet/tx?txid="+url.QueryEscape(txid), &r); err != nil {
		if err == errNotFound {
			return -1, nil
		}
		return 0, err
	}
	return r.Confirmations, nil
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}
