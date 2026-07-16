// Package zephyrwallet implements the ZPH-only Zephyr wallet RPC contract.
package zephyrwallet

import (
	"context"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/cnwallet"
)

const assetZPH = "ZPH"

type Client struct {
	*cnwallet.Client
	name     string
	decimals int
}

var (
	_ adapter.WalletAdapter          = (*Client)(nil)
	_ adapter.SpendableBalanceSource = (*Client)(nil)
)

func New(name, url string, decimals int) *Client {
	if decimals <= 0 {
		decimals = 12
	}
	return &Client{
		Client: cnwallet.New(name, url, decimals), name: name, decimals: decimals,
	}
}

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	var response struct {
		Balances []struct {
			UnlockedBalance uint64 `json:"unlocked_balance"`
			AssetType       string `json:"asset_type"`
		} `json:"balances"`
	}
	if err := c.Call(ctx, "get_balance", map[string]any{
		"account_index": 0, "asset_type": assetZPH,
	}, &response); err != nil {
		return "", err
	}
	for _, balance := range response.Balances {
		if balance.AssetType == assetZPH {
			return adapter.AtomicToDecimal(balance.UnlockedBalance, c.decimals), nil
		}
	}
	return "", fmt.Errorf("%s: get_balance 未返回 ZPH balance", c.name)
}

func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	type destination struct {
		Amount  uint64 `json:"amount"`
		Address string `json:"address"`
	}
	destinations := make([]destination, 0, len(outputs))
	for address, amount := range outputs {
		atomic, err := adapter.DecimalToAtomic(amount, c.decimals)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		destinations = append(destinations, destination{Amount: atomic, Address: address})
	}
	var response struct {
		TxHash string `json:"tx_hash"`
	}
	if err := c.Call(ctx, "transfer", map[string]any{
		"destinations": destinations, "account_index": 0,
		"source_asset": assetZPH, "destination_asset": assetZPH,
		"get_tx_key": true, "do_not_relay": false,
	}, &response); err != nil {
		return "", err
	}
	if response.TxHash == "" {
		return "", fmt.Errorf("%s: transfer 未返回 tx_hash", c.name)
	}
	return response.TxHash, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var response struct {
		Transfer struct {
			Confirmations int64  `json:"confirmations"`
			AssetType     string `json:"asset_type"`
			Type          string `json:"type"`
		} `json:"transfer"`
	}
	if err := c.Call(ctx, "get_transfer_by_txid", map[string]any{
		"txid": txid, "account_index": 0,
	}, &response); err != nil {
		return -1, nil
	}
	transfer := response.Transfer
	if transfer.AssetType != assetZPH {
		return 0, fmt.Errorf("%s: tx %s asset_type=%q，期望 ZPH", c.name, txid, transfer.AssetType)
	}
	switch transfer.Type {
	case "failed":
		return -1, nil
	case "out", "pending":
		return transfer.Confirmations, nil
	default:
		return 0, fmt.Errorf("%s: tx %s type=%q，不是 ZPH payout", c.name, txid, transfer.Type)
	}
}
