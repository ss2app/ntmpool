// Package cnrpc 门罗系（CryptoNote）daemon JSON-RPC 适配器。
//
// daemon（/json_rpc）：get_info / get_block_template / submit_block /
// on_get_block_hash / get_block_header_by_hash。钱包独立进程见 cnwallet。
//
// M3 约束（见 adapter.BlobWork 注释）：不做 tx_extra reserved 空间 extranonce
// （那需要重建 miner tx hash → merkle root），用 nicehash 分片替代：
// 池按连接钉 nonce 最高字节（blob[42]），矿工只滚低 3 字节（login extensions
// 声明 nicehash，XMRig 原生支持）。代价 = 每 job ≤256 条连接，工厂规模足够；
// 真门罗系大池需求出现时按 docs/03 M3+ 补 merkle 重建。
//
// 全网算力：门罗系没有 getnetworkhashps 真值口径，本适配器不实现 HashPSSource
// （铁律 pitfall C7：绝不用难度反推冒充），API 侧省略该字段。
package cnrpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/cnwork"
)

// monero 系 blob 惯例：4 字节 nonce 在 hashing blob 偏移 39（主网 varint 布局下的
// 行业硬编值，XMRig 同款）；nicehash 分片 = 矿工滚低 3 字节。
const (
	nonceOffset = 39
	nonceLen    = 4
	searchLen   = 3
)

// Client 一个 daemon 的 RPC 客户端。实现 NodeAdapter / BlobSubmitter。
type Client struct {
	name        string
	url         string // 如 http://127.0.0.1:18081（自动补 /json_rpc）
	algo        string // rx/0 等
	decimals    int    // 门罗系惯例 12
	poolAddress string // get_block_template 用
	hc          *http.Client
}

var (
	_ adapter.NodeAdapter   = (*Client)(nil)
	_ adapter.BlobSubmitter = (*Client)(nil)
)

func New(name, url, algo string, decimals int) *Client {
	if decimals <= 0 {
		decimals = 12
	}
	return &Client{
		name: name, url: strings.TrimRight(url, "/"), algo: algo, decimals: decimals,
		hc: &http.Client{Timeout: 150 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

// rpcError daemon 协议级错误。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("cnrpc error %d: %s", e.Code, e.Message) }

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "ntmpool", "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/json_rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("%s %s: 非法响应(http %d): %w", c.name, method, resp.StatusCode, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// ---- NodeAdapter ----

func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	var info struct {
		Height        uint64 `json:"height"`
		TopBlockHash  string `json:"top_block_hash"`
		TargetHeight  uint64 `json:"target_height"`
		Synchronized  bool   `json:"synchronized"`
		Status        string `json:"status"`
	}
	if err := c.call(ctx, "get_info", map[string]any{}, &info); err != nil {
		return adapter.ChainStatus{}, err
	}
	synced := info.Synchronized || info.TargetHeight == 0 || info.Height >= info.TargetHeight
	return adapter.ChainStatus{
		Height: info.Height, TipHash: info.TopBlockHash,
		Synced: synced, Connected: true,
	}, nil
}

type gbtResp struct {
	BlocktemplateBlob string `json:"blocktemplate_blob"`
	BlockhashingBlob  string `json:"blockhashing_blob"`
	Difficulty        uint64 `json:"difficulty"`
	Height            uint64 `json:"height"`
	PrevHash          string `json:"prev_hash"`
	SeedHash          string `json:"seed_hash"`
	ExpectedReward    uint64 `json:"expected_reward"`
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	// nicehash 分片不用 reserved 空间 → reserve_size 给 1（部分 daemon 要求 ≥1）
	var t gbtResp
	err := c.call(ctx, "get_block_template", map[string]any{
		"wallet_address": c.poolAddress, "reserve_size": 1,
	}, &t)
	if err != nil {
		return nil, err
	}
	hashing, err := hex.DecodeString(t.BlockhashingBlob)
	if err != nil || len(hashing) < nonceOffset+nonceLen {
		return nil, fmt.Errorf("%s: blockhashing_blob 非法（len=%d, %v）", c.name, len(hashing), err)
	}
	if t.Difficulty == 0 {
		return nil, fmt.Errorf("%s: difficulty=0", c.name)
	}
	work := &adapter.BlobWork{
		HashingBlob:   hashing,
		NonceOffset:   nonceOffset,
		NonceLen:      nonceLen,
		SearchLen:     searchLen,
		SeedHash:      t.SeedHash,
		Algo:          c.algo,
		NetworkTarget: cnwork.TargetFromDiff(float64(t.Difficulty)),
		HashBigEndian: false, // monero 惯例：hash 小端解释
		TargetCompactLE: true, // XMRig 惯例：8-hex compact 小端
		Nicehash:      true,
		HeightHint:    t.Height,
		SubmitRef:     t.BlocktemplateBlob,
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.PrevHash,
		CoinbaseValue: adapter.AtomicToDecimal(t.ExpectedReward, c.decimals),
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// SetPoolAddress 模板要用的矿池钱包地址（启动时注入一次）。
func (c *Client) SetPoolAddress(addr string) { c.poolAddress = addr }

// SubmitBlock 兼容 NodeAdapter 面（上层 cnjob 走 SubmitBlob）。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	sol, ok := raw.(*adapter.BlobSolution)
	if !ok {
		return fmt.Errorf("cnrpc.SubmitBlock: 期望 *BlobSolution, got %T", raw)
	}
	_, err := c.SubmitBlob(ctx, sol)
	return err
}

// SubmitBlob 把 nonce 写回 blocktemplate_blob（偏移 39，与 hashing blob 同位）后提交。
// 权威块 hash：submit_block 无返回值，提交成功后按高度取块头 hash
// （刚被受理的块就是该高度主链块；同毫秒被孪生块顶掉的窗口由成熟期分类兜底，
// 真门罗系接入时用 CN 块 id 计算收紧——docs/03 M3+）。
func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	tplHex, _ := sol.Work.SubmitRef.(string)
	if tplHex == "" {
		return "", fmt.Errorf("%s: 模板缺 blocktemplate_blob", c.name)
	}
	blk, err := hex.DecodeString(tplHex)
	if err != nil || len(blk) < nonceOffset+nonceLen {
		return "", fmt.Errorf("%s: blocktemplate_blob 非法", c.name)
	}
	cnwork.PutNonceLE(blk, nonceOffset, nonceLen, sol.Nonce)
	if err := c.call(ctx, "submit_block", []any{hex.EncodeToString(blk)}, nil); err != nil {
		return "", err
	}
	// 取回权威块 hash（我们的块刚成为该高度主链块）
	height := blockHeightFromWork(sol)
	var hdr struct {
		BlockHeader struct {
			Hash string `json:"hash"`
		} `json:"block_header"`
	}
	if err := c.call(ctx, "get_block_header_by_height", map[string]any{"height": height}, &hdr); err != nil {
		return "", fmt.Errorf("%s: 提交成功但取块 hash 失败: %w", c.name, err)
	}
	return hdr.BlockHeader.Hash, nil
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var hash string
	if err := c.call(ctx, "on_get_block_hash", []any{height}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// Confirmations 孤块 → -1；主链块 → depth+1。
func (c *Client) Confirmations(ctx context.Context, blockHash string) (int64, error) {
	var hdr struct {
		BlockHeader struct {
			Depth        uint64 `json:"depth"`
			OrphanStatus bool   `json:"orphan_status"`
		} `json:"block_header"`
	}
	if err := c.call(ctx, "get_block_header_by_hash", map[string]any{"hash": blockHash}, &hdr); err != nil {
		// daemon 不认识该块 = 从未上链/已被清 → 按孤块处理
		return -1, nil
	}
	if hdr.BlockHeader.OrphanStatus {
		return -1, nil
	}
	return int64(hdr.BlockHeader.Depth) + 1, nil
}

// blockHeightFromWork 从解里取高度（SubmitBlob 需要；height 在模板级记录，
// 由 cnjob 放进 Work 旁路——这里通过 HeightHint 传递）。
func blockHeightFromWork(sol *adapter.BlobSolution) uint64 { return sol.Work.HeightHint }
