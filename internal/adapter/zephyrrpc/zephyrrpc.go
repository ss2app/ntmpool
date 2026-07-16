// Package zephyrrpc implements the Zephyr Protocol daemon contract while
// reusing the opaque CryptoNote blob/nonce/submit behavior from cnrpc.
package zephyrrpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/cnrpc"
)

const assetZPH = "ZPH"

// Client is a thin Zephyr specialization of the common CryptoNote RPC client.
type Client struct {
	*cnrpc.Client
	decimals int
}

var (
	_ adapter.NodeAdapter       = (*Client)(nil)
	_ adapter.BlobSubmitter     = (*Client)(nil)
	_ adapter.BlockRewardSource = (*Client)(nil)
)

func New(name, url, algo string, decimals int) *Client {
	if decimals <= 0 {
		decimals = 12
	}
	return &Client{Client: cnrpc.New(name, url, algo, decimals), decimals: decimals}
}

// BlockReward returns the ZPH amount paid to the pool miner output.
func (c *Client) BlockReward(ctx context.Context, blockHash string) (string, error) {
	var response struct {
		JSON string `json:"json"`
	}
	if err := c.Call(ctx, "get_block", map[string]any{
		"hash": blockHash, "fill_pow_hash": false,
	}, &response); err != nil {
		return "", err
	}
	if response.JSON == "" {
		return "", fmt.Errorf("zephyrrpc: get_block(%s) 未返回 json", blockHash)
	}

	var block struct {
		MinerTX struct {
			Vout []struct {
				Amount uint64 `json:"amount"`
				Target struct {
					TaggedKey *struct {
						AssetType string `json:"asset_type"`
					} `json:"tagged_key"`
				} `json:"target"`
			} `json:"vout"`
		} `json:"miner_tx"`
	}
	if err := json.Unmarshal([]byte(response.JSON), &block); err != nil {
		return "", fmt.Errorf("zephyrrpc: get_block(%s).json 非法: %w", blockHash, err)
	}
	if len(block.MinerTX.Vout) == 0 {
		return "", fmt.Errorf("zephyrrpc: get_block(%s) miner_tx.vout 为空", blockHash)
	}

	// Zephyr commit 67c5f53b 的
	// zephyr/src/cryptonote_core/cryptonote_tx_utils.cpp:190-205 先以
	// out_index=0 构造矿工主奖励并 push 到 vout；治理 output 仅在之后的
	// :207-258 追加，异资产 fee outputs 又在 :261-283 追加。因此只认
	// vout[0]，并强制它是 ZPH；汇总所有 ZPH output 会把治理款误付矿工。
	miner := block.MinerTX.Vout[0]
	if miner.Target.TaggedKey == nil {
		return "", fmt.Errorf("zephyrrpc: get_block(%s) miner_tx.vout[0] 不是 tagged_key", blockHash)
	}
	if miner.Target.TaggedKey.AssetType != assetZPH {
		return "", fmt.Errorf("zephyrrpc: get_block(%s) miner_tx.vout[0] asset_type=%q，期望 ZPH",
			blockHash, miner.Target.TaggedKey.AssetType)
	}
	return adapter.AtomicToDecimal(miner.Amount, c.decimals), nil
}
