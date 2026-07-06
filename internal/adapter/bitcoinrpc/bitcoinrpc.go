// Package bitcoinrpc 标准比特币系 JSON-RPC 节点适配器
// （getblocktemplate/submitblock/sendmany，覆盖 BTC 及绝大多数分叉山寨币）。
//
// 规则（docs/02 §1 + docs/05）：
//   - 无状态可重连：每次调用独立 HTTP 请求，节点重启自愈（pitfall C9）。
//   - timeout ≥ 120s：sendmany 慢是常态，短 timeout+重试 = 双花（pitfall C4）。
//   - 实现 RawTxWallet：打款拆步（签名即定 txid，先落库后广播），崩溃恢复零歧义。
package bitcoinrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

// Client 一个节点的 RPC 客户端。同时实现 NodeAdapter / WalletAdapter / RawTxWallet。
type Client struct {
	name     string
	url      string
	user     string
	pass     string
	decimals int      // satoshi→币 的小数位，绝大多数 bitcoin 系为 8
	gbtRules []string // getblocktemplate rules，默认 ["segwit"]（老分叉币可置空）
	hc       *http.Client
}

// 编译期接口断言：三个角色都必须实现完整。
var (
	_ adapter.NodeAdapter   = (*Client)(nil)
	_ adapter.WalletAdapter = (*Client)(nil)
	_ adapter.RawTxWallet   = (*Client)(nil)
)

func New(name, url, user, pass string) *Client {
	return &Client{
		name:     name,
		url:      url,
		user:     user,
		pass:     pass,
		decimals: 8,
		gbtRules: []string{"segwit"},
		hc:       &http.Client{Timeout: 150 * time.Second},
	}
}

// SetGBTRules 老的 pre-segwit 分叉币可置空 rules。
func (c *Client) SetGBTRules(rules []string) { c.gbtRules = rules }

// SetDecimals 非 8 位小数的链调整换算位数。
func (c *Client) SetDecimals(d int) { c.decimals = d }

func (c *Client) Name() string { return c.name }

// ---- JSON-RPC 底座 ----

// RPCError 节点返回的协议级错误（带 code，恢复逻辑要按 code 分支）。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

const rpcNotFound = -5 // bitcoin 系惯例：块/交易不存在

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0", "id": "ntmpool", "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
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

func rpcCode(err error) (int, bool) {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

// satoshiToCoin 整数换算，绝不走浮点（金额铁律）。
func satoshiToCoin(v int64, decimals int) string {
	div := int64(1)
	for i := 0; i < decimals; i++ {
		div *= 10
	}
	return fmt.Sprintf("%d.%0*d", v/div, decimals, v%div)
}

// ---- NodeAdapter ----

func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	var info struct {
		Blocks               uint64 `json:"blocks"`
		Headers              uint64 `json:"headers"`
		BestBlockHash        string `json:"bestblockhash"`
		InitialBlockDownload bool   `json:"initialblockdownload"`
	}
	if err := c.call(ctx, "getblockchaininfo", []any{}, &info); err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{
		Height:    info.Blocks,
		TipHash:   info.BestBlockHash,
		Synced:    !info.InitialBlockDownload && info.Blocks >= info.Headers,
		Connected: true,
	}, nil
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	params := []any{map[string]any{"rules": c.gbtRules}}
	var raw json.RawMessage
	if err := c.call(ctx, "getblocktemplate", params, &raw); err != nil {
		return nil, err
	}
	var t struct {
		Height            uint64 `json:"height"`
		PreviousBlockHash string `json:"previousblockhash"`
		Target            string `json:"target"`
		CoinbaseValue     int64  `json:"coinbasevalue"`
		MinTime           int64  `json:"mintime"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.PreviousBlockHash,
		NetworkTarget: t.Target,
		CoinbaseValue: satoshiToCoin(t.CoinbaseValue, c.decimals),
		MinTime:       t.MinTime,
		Raw:           raw,
		FetchedAt:     time.Now(),
	}, nil
}

// SubmitBlock raw = 完整块 hex 字符串。"duplicate" 视为成功（多节点并发提交时常见）。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	hexStr, ok := raw.(string)
	if !ok {
		return fmt.Errorf("bitcoinrpc.SubmitBlock: 期望块 hex 字符串, got %T", raw)
	}
	var result *string
	if err := c.call(ctx, "submitblock", []any{hexStr}, &result); err != nil {
		return err
	}
	if result != nil && *result != "" && !strings.HasPrefix(*result, "duplicate") {
		return fmt.Errorf("submitblock 被拒: %s", *result)
	}
	return nil
}

// PayoutScript 取地址的 scriptPubKey（hex）。铁律：不自己解地址格式，问节点要脚本。
func (c *Client) PayoutScript(ctx context.Context, address string) (string, error) {
	var info struct {
		ScriptPubKey string `json:"scriptPubKey"`
		IsValid      bool   `json:"isvalid"`
	}
	if err := c.call(ctx, "getaddressinfo", []any{address}, &info); err != nil {
		// 老分叉币可能只有 validateaddress
		if err := c.call(ctx, "validateaddress", []any{address}, &info); err != nil {
			return "", err
		}
	}
	if info.ScriptPubKey == "" {
		return "", fmt.Errorf("地址 %s 无 scriptPubKey（可能非本钱包地址或无效）", address)
	}
	return info.ScriptPubKey, nil
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var hash string
	if err := c.call(ctx, "getblockhash", []any{height}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// Confirmations 块不在主链返回 -1（getblockheader 对孤块本身就返回 confirmations=-1；
// 节点完全不认识该块（-5）同样归一为 -1，调用方按孤块处理）。
func (c *Client) Confirmations(ctx context.Context, blockHash string) (int64, error) {
	var hdr struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.call(ctx, "getblockheader", []any{blockHash}, &hdr); err != nil {
		if code, ok := rpcCode(err); ok && code == rpcNotFound {
			return -1, nil
		}
		return 0, err
	}
	return hdr.Confirmations, nil
}

// ---- WalletAdapter ----

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	// getbalance 返回 JSON number；用 RawMessage 原样透传，绝不过 float64。
	var raw json.RawMessage
	if err := c.call(ctx, "getbalance", []any{}, &raw); err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(raw)), nil
}

// outputsParam 金额字符串原样作为 JSON number 传给节点，绕开浮点。
func outputsParam(outputs map[string]string) (map[string]json.RawMessage, error) {
	m := make(map[string]json.RawMessage, len(outputs))
	for addr, amt := range outputs {
		if !json.Valid([]byte(amt)) {
			return nil, fmt.Errorf("非法金额 %q (地址 %s)", amt, addr)
		}
		m[addr] = json.RawMessage(amt)
	}
	return m, nil
}

func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	m, err := outputsParam(outputs)
	if err != nil {
		return "", err
	}
	var txid string
	if err := c.call(ctx, "sendmany", []any{"", m}, &txid); err != nil {
		return "", err
	}
	return txid, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var tx struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.call(ctx, "gettransaction", []any{txid}, &tx); err != nil {
		return 0, err
	}
	return tx.Confirmations, nil
}

// ---- RawTxWallet（拆步打款：崩溃恢复零歧义，docs/05 场景 A）----

func (c *Client) PrepareSendMany(ctx context.Context, outputs map[string]string) (string, string, error) {
	m, err := outputsParam(outputs)
	if err != nil {
		return "", "", err
	}
	// ① 构造（无输入，由 fund 补）
	var unfunded string
	if err := c.call(ctx, "createrawtransaction", []any{[]any{}, m}, &unfunded); err != nil {
		return "", "", fmt.Errorf("createrawtransaction: %w", err)
	}
	// ② 选币付费
	var funded struct {
		Hex string `json:"hex"`
	}
	if err := c.call(ctx, "fundrawtransaction", []any{unfunded}, &funded); err != nil {
		return "", "", fmt.Errorf("fundrawtransaction: %w", err)
	}
	// ③ 签名（SegWit 后签名即定 txid）
	var signed struct {
		Hex      string `json:"hex"`
		Complete bool   `json:"complete"`
	}
	if err := c.call(ctx, "signrawtransactionwithwallet", []any{funded.Hex}, &signed); err != nil {
		return "", "", fmt.Errorf("signrawtransactionwithwallet: %w", err)
	}
	if !signed.Complete {
		return "", "", fmt.Errorf("signrawtransactionwithwallet: 签名不完整")
	}
	// ④ 取 txid（不广播）
	var decoded struct {
		TxID string `json:"txid"`
	}
	if err := c.call(ctx, "decoderawtransaction", []any{signed.Hex}, &decoded); err != nil {
		return "", "", fmt.Errorf("decoderawtransaction: %w", err)
	}
	return decoded.TxID, signed.Hex, nil
}

// Broadcast 幂等：重播同一笔已签名交易（恢复流程）返回成功而非报错。
func (c *Client) Broadcast(ctx context.Context, rawtx string) error {
	var txid string
	err := c.call(ctx, "sendrawtransaction", []any{rawtx}, &txid)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if code, ok := rpcCode(err); ok && code == -27 { // Transaction already in block chain
		return nil
	}
	if strings.Contains(msg, "already in the mempool") ||
		strings.Contains(msg, "txn-already-in-mempool") ||
		strings.Contains(msg, "already known") ||
		strings.Contains(msg, "txn-already-known") {
		return nil
	}
	return err
}

// TxExists 崩溃恢复判据：plannedtxid 是否已在 mempool 或链上。
// 打款 tx 是本钱包交易，gettransaction 一定认识已广播的它。
func (c *Client) TxExists(ctx context.Context, txid string) (bool, error) {
	if err := c.call(ctx, "getmempoolentry", []any{txid}, nil); err == nil {
		return true, nil
	}
	err := c.call(ctx, "gettransaction", []any{txid}, nil)
	if err == nil {
		return true, nil
	}
	if code, ok := rpcCode(err); ok && code == rpcNotFound {
		return false, nil
	}
	return false, err
}
