// Package cnwallet 门罗系 wallet-rpc 打款适配器（daemon 与钱包分离是 CN 惯例）。
//
// wallet-rpc（/json_rpc）：get_balance / transfer / get_transfer_by_txid。
// 实现 adapter.WalletAdapter：transfer 单笔多 destinations（绝不逐地址串行，
// pitfall C2）；金额十进制字符串 ↔ 原子单位纯整数换算（金额铁律）。
// 不实现 RawTxWallet（CN 钱包没有「签名即定 txid 不广播」的拆步原语），
// 打款引擎自动退回 SendMany 路径。
package cnwallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

// Client 一个 wallet-rpc 的客户端。实现 WalletAdapter。
type Client struct {
	name     string
	url      string
	decimals int
	hc       *http.Client
}

var _ adapter.WalletAdapter = (*Client)(nil)

func New(name, url string, decimals int) *Client {
	if decimals <= 0 {
		decimals = 12
	}
	return &Client{
		name: name, url: strings.TrimRight(url, "/"), decimals: decimals,
		hc: &http.Client{Timeout: 150 * time.Second}, // transfer 慢是常态（pitfall C4）
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("cnwallet error %d: %s", e.Code, e.Message) }

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

// ---- WalletAdapter ----

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	var r struct {
		UnlockedBalance uint64 `json:"unlocked_balance"`
	}
	if err := c.call(ctx, "get_balance", map[string]any{"account_index": 0}, &r); err != nil {
		return "", err
	}
	return adapter.AtomicToDecimal(r.UnlockedBalance, c.decimals), nil
}

// SendMany 单笔多输出 transfer，返回 tx_hash。
func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	type dest struct {
		Amount  uint64 `json:"amount"`
		Address string `json:"address"`
	}
	dests := make([]dest, 0, len(outputs))
	for addr, amt := range outputs {
		v, err := adapter.DecimalToAtomic(amt, c.decimals)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		dests = append(dests, dest{Amount: v, Address: addr})
	}
	var r struct {
		TxHash string `json:"tx_hash"`
	}
	err := c.call(ctx, "transfer", map[string]any{
		"destinations":  dests,
		"account_index": 0,
		"get_tx_key":    true,
		"do_not_relay":  false,
	}, &r)
	if err != nil {
		return "", err
	}
	if r.TxHash == "" {
		return "", fmt.Errorf("%s: transfer 未返回 tx_hash", c.name)
	}
	return r.TxHash, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var r struct {
		Transfer struct {
			Confirmations int64  `json:"confirmations"`
			Type          string `json:"type"`
		} `json:"transfer"`
	}
	if err := c.call(ctx, "get_transfer_by_txid", map[string]any{"txid": txid}, &r); err != nil {
		// 钱包不认识该 tx（被双花顶掉/清出）→ 掉链
		return -1, nil
	}
	if r.Transfer.Type == "failed" {
		return -1, nil
	}
	return r.Transfer.Confirmations, nil
}
