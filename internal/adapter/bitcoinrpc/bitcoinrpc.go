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
	"math/big"
	"net/http"
	"strings"
	"sync"
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

	mu             sync.Mutex
	lastLongPollID string // 最近一次 GBT 返回的 longpollid（供 GBT longpoll 挂等，见 longpoll.go）
}

// 编译期接口断言：三个角色都必须实现完整。
var (
	_ adapter.NodeAdapter     = (*Client)(nil)
	_ adapter.WalletAdapter   = (*Client)(nil)
	_ adapter.RawTxWallet     = (*Client)(nil)
	_ adapter.ChainAuditor    = (*Client)(nil)
	_ adapter.CoinbaseAuditor = (*Client)(nil)
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
		LongPollID        string `json:"longpollid"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	if t.LongPollID != "" {
		c.mu.Lock()
		c.lastLongPollID = t.LongPollID
		c.mu.Unlock()
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

// NetworkHashPS 全网算力真值（getnetworkhashps，最近 120 块实际 work÷实际时间）。
// 铁律（pitfall C7）：全网算力只用此口径，绝不用「难度÷目标出块时间」反推。
func (c *Client) NetworkHashPS(ctx context.Context) (float64, error) {
	var hps float64
	if err := c.call(ctx, "getnetworkhashps", []any{120, -1}, &hps); err != nil {
		return 0, err
	}
	return hps, nil
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
// 节点完全不认识该块（-5）同样归一为 -1，调用方按孤块处理）。height 不需要。
func (c *Client) Confirmations(ctx context.Context, blockHash string, _ uint64) (int64, error) {
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

// walletHistoryEntry 保留 amount 的 JSON 原文，金额转换全程不经过 float64。
type walletHistoryEntry struct {
	Address       string          `json:"address"`
	Category      string          `json:"category"`
	Amount        json.RawMessage `json:"amount"`
	TxID          string          `json:"txid"`
	BlockHash     string          `json:"blockhash"`
	BlockHeight   uint64          `json:"blockheight"`
	Confirmations int64           `json:"confirmations"`
}

const chainAuditInitialLookback = uint64(1000)

// recentWalletHistory 用 listsinceblock 取近期钱包流水。首次审计只回看最近 1000 块，
// 避免升级时把建库前的历史钱包转账误判为未知出账；后续由审计器传入重叠游标。
func (c *Client) recentWalletHistory(ctx context.Context, sinceHeight uint64) ([]walletHistoryEntry, error) {
	start := sinceHeight
	if start == 0 {
		var tip uint64
		if err := c.call(ctx, "getblockcount", []any{}, &tip); err != nil {
			return nil, fmt.Errorf("getblockcount: %w", err)
		}
		if tip > chainAuditInitialLookback {
			start = tip - chainAuditInitialLookback
		}
	} else {
		// listsinceblock 不含锚块本身，回退一块保证 sinceHeight 是闭区间。
		start--
	}

	var blockHash string
	if err := c.call(ctx, "getblockhash", []any{start}, &blockHash); err != nil {
		return nil, fmt.Errorf("getblockhash(%d): %w", start, err)
	}
	var result struct {
		Transactions []walletHistoryEntry `json:"transactions"`
	}
	if err := c.call(ctx, "listsinceblock", []any{blockHash, 1, true}, &result); err != nil {
		return nil, fmt.Errorf("listsinceblock: %w", err)
	}
	return result.Transactions, nil
}

// ListRecentOutbound 实现 adapter.ChainAuditor。listsinceblock 对同一 tx 的每个
// send 明细各返回一行，这里按 txid 合并为一笔，并用 getaddressinfo/validateaddress
// 标出自有地址，让上层安全排除找零和钱包内部整理。
func (c *Client) ListRecentOutbound(ctx context.Context, sinceHeight uint64) ([]adapter.OutboundTx, error) {
	entries, err := c.recentWalletHistory(ctx, sinceHeight)
	if err != nil {
		return nil, err
	}
	byTx := make(map[string]*adapter.OutboundTx)
	order := make([]string, 0)
	owned := make(map[string]bool)
	ownedKnown := make(map[string]bool)
	for _, entry := range entries {
		if entry.Category != "send" || entry.TxID == "" {
			continue
		}
		units, err := jsonAmountToUnits(entry.Amount, c.decimals)
		if err != nil {
			return nil, fmt.Errorf("tx %s amount: %w", entry.TxID, err)
		}
		if units < 0 {
			if units == -1<<63 {
				return nil, fmt.Errorf("tx %s amount 绝对值溢出", entry.TxID)
			}
			units = -units
		}
		isMine := false
		if entry.Address != "" {
			if !ownedKnown[entry.Address] {
				owned[entry.Address], err = c.isMineAddress(ctx, entry.Address)
				if err != nil {
					return nil, fmt.Errorf("tx %s address %s ownership: %w", entry.TxID, entry.Address, err)
				}
				ownedKnown[entry.Address] = true
			}
			isMine = owned[entry.Address]
		}
		tx := byTx[entry.TxID]
		if tx == nil {
			tx = &adapter.OutboundTx{TxID: entry.TxID, Confirmations: entry.Confirmations, Height: entry.BlockHeight}
			byTx[entry.TxID] = tx
			order = append(order, entry.TxID)
		}
		if entry.Confirmations > tx.Confirmations {
			tx.Confirmations = entry.Confirmations
		}
		if entry.BlockHeight > tx.Height {
			tx.Height = entry.BlockHeight
		}
		tx.Outputs = append(tx.Outputs, adapter.OutboundOutput{
			Address: entry.Address, Amount: units, IsMine: isMine,
		})
	}
	out := make([]adapter.OutboundTx, 0, len(order))
	for _, txid := range order {
		out = append(out, *byTx[txid])
	}
	return out, nil
}

// ListRecentCoinbase 实现方向 B：generate/immature 都是池钱包收到的 coinbase；
// orphan 不构成受控资产，故不纳入 missing round 告警。
func (c *Client) ListRecentCoinbase(ctx context.Context, sinceHeight uint64) ([]adapter.CoinbaseReceipt, error) {
	entries, err := c.recentWalletHistory(ctx, sinceHeight)
	if err != nil {
		return nil, err
	}
	byTx := make(map[string]*adapter.CoinbaseReceipt)
	order := make([]string, 0)
	for _, entry := range entries {
		if (entry.Category != "generate" && entry.Category != "immature") || entry.TxID == "" || entry.BlockHash == "" {
			continue
		}
		units, err := jsonAmountToUnits(entry.Amount, c.decimals)
		if err != nil {
			return nil, fmt.Errorf("coinbase tx %s amount: %w", entry.TxID, err)
		}
		if units < 0 {
			return nil, fmt.Errorf("coinbase tx %s 金额为负", entry.TxID)
		}
		r := byTx[entry.TxID]
		if r == nil {
			r = &adapter.CoinbaseReceipt{TxID: entry.TxID, BlockHash: entry.BlockHash,
				Confirmations: entry.Confirmations, Height: entry.BlockHeight}
			byTx[entry.TxID] = r
			order = append(order, entry.TxID)
		}
		if units > 0 && r.Amount > int64(^uint64(0)>>1)-units {
			return nil, fmt.Errorf("coinbase tx %s 金额溢出", entry.TxID)
		}
		r.Amount += units
	}
	out := make([]adapter.CoinbaseReceipt, 0, len(order))
	for _, txid := range order {
		out = append(out, *byTx[txid])
	}
	return out, nil
}

func (c *Client) isMineAddress(ctx context.Context, address string) (bool, error) {
	var info struct {
		IsMine bool `json:"ismine"`
	}
	err := c.call(ctx, "getaddressinfo", []any{address}, &info)
	if code, ok := rpcCode(err); ok && code == -32601 {
		err = c.call(ctx, "validateaddress", []any{address}, &info)
	}
	return info.IsMine, err
}

// jsonAmountToUnits 把 JSON 十进制金额精确转成最小单位整数；不接受指数形式，
// 因 Bitcoin RPC 钱包金额契约是定点十进制，异常格式宁可使本轮审计不可用也不猜。
func jsonAmountToUnits(raw json.RawMessage, decimals int) (int64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || decimals < 0 {
		return 0, fmt.Errorf("非法金额 %q", s)
	}
	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || (parts[0] == "" && (len(parts) == 1 || parts[1] == "")) {
		return 0, fmt.Errorf("非法金额 %q", string(raw))
	}
	whole := parts[0]
	if whole == "" {
		whole = "0"
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	for _, digits := range []string{whole, frac} {
		for _, ch := range digits {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("非法金额 %q", string(raw))
			}
		}
	}
	if len(frac) > decimals {
		for _, ch := range frac[decimals:] {
			if ch != '0' {
				return 0, fmt.Errorf("金额精度超过 %d 位: %q", decimals, string(raw))
			}
		}
		frac = frac[:decimals]
	}
	frac += strings.Repeat("0", decimals-len(frac))
	n := new(big.Int)
	if _, ok := n.SetString(whole+frac, 10); !ok {
		return 0, fmt.Errorf("非法金额 %q", string(raw))
	}
	if neg {
		n.Neg(n)
	}
	if !n.IsInt64() {
		return 0, fmt.Errorf("金额溢出: %q", string(raw))
	}
	return n.Int64(), nil
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
	// 新节点(Bitcoin Core ≥0.17)用 signrawtransactionwithwallet；老节点(PIVX 等)只有
	// legacy signrawtransaction（同 {hex,complete} 返回）。方法不存在(-32601)时回退，
	// 一份代码兼容两类节点（brva=Bitcoin Core v30 走新方法，noctari=PIVX 走 legacy）。
	signErr := c.call(ctx, "signrawtransactionwithwallet", []any{funded.Hex}, &signed)
	if code, ok := rpcCode(signErr); ok && code == -32601 {
		signErr = c.call(ctx, "signrawtransaction", []any{funded.Hex}, &signed)
	}
	if signErr != nil {
		return "", "", fmt.Errorf("sign raw tx: %w", signErr)
	}
	if !signed.Complete {
		return "", "", fmt.Errorf("sign raw tx: 签名不完整")
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
