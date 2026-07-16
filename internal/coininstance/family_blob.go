package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/cnrpc"
	"github.com/scashcc/ntmpool/internal/adapter/cnwallet"
	"github.com/scashcc/ntmpool/internal/adapter/customhttp"
	"github.com/scashcc/ntmpool/internal/adapter/zephyrrpc"
	"github.com/scashcc/ntmpool/internal/adapter/zephyrwallet"
	"github.com/scashcc/ntmpool/internal/cnjob"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/payout"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// blobNode 是 blob 家族对节点适配器的组合要求。
type blobNode interface {
	adapter.NodeAdapter
	adapter.BlobSubmitter
}

// buildBlobFamily blob 系（CryptoNote/RandomX 家族 + zoka 类自定义链）：
// cryptonote-rpc / custom-http 适配器 + cnjob 作业管理器 + cryptonote 方言。
func buildBlobFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}

	var (
		node         blobNode
		wallet       adapter.WalletAdapter
		classifier   payout.NodeClassifier
		hashps       adapter.HashPSSource // blob 链通常无真值口径 → nil（绝不反推）
		rewardSource adapter.BlockRewardSource
	)
	n := cfg.Nodes[0]
	switch cfg.Adapter {
	case "custom-http":
		c := customhttp.New(cfg.ID, n.URL, cfg.PoolAddress, cfg.Algo, decimals)
		node, wallet, classifier = c, c, c
	case "cryptonote-rpc":
		if cfg.Wallet.URL == "" {
			return nil, errf("[%s] cryptonote-rpc 需要独立 wallet 端点（cfg.wallet.url）", cfg.ID)
		}
		c := cnrpc.New(cfg.ID, n.URL, cfg.Algo, decimals)
		c.SetPoolAddress(cfg.PoolAddress)
		node, classifier = c, c
		wallet = cnwallet.New(cfg.ID+"-wallet", cfg.Wallet.URL, decimals)
	case "zephyr-rpc":
		if cfg.Wallet.URL == "" {
			return nil, errf("[%s] zephyr-rpc 需要独立 wallet 端点（cfg.wallet.url）", cfg.ID)
		}
		c := zephyrrpc.New(cfg.ID, n.URL, cfg.Algo, decimals)
		c.SetPoolAddress(cfg.PoolAddress)
		node, classifier, rewardSource = c, c, c
		wallet = zephyrwallet.New(cfg.ID+"-wallet", cfg.Wallet.URL, decimals)
	default:
		return nil, errf("[%s] blob 家族不支持适配器 %q", cfg.ID, cfg.Adapter)
	}

	jm := cnjob.New(cfg.ID, cfg.Algo, node, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink(rewardSource)
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit cnjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     node,
		hashps:     hashps,
		classifier: classifier,
		wallet:     wallet,
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"cryptonote": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}
