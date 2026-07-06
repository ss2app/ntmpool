package coininstance

import (
	"context"
	"fmt"
	"log"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/bitcoinrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/jobmanager"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildFamily 按 cfg.Adapter 选链家族并拼装部件（M3 选型化的入口）。
func buildFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	switch cfg.Adapter {
	case "", "bitcoin-rpc":
		return buildBitcoinFamily(ctx, cfg, decimals, inst)
	case "custom-http", "cryptonote-rpc":
		return buildBlobFamily(ctx, cfg, decimals, inst)
	default:
		return nil, errf("[%s] 未知适配器 %q（可选 bitcoin-rpc / cryptonote-rpc / custom-http）", cfg.ID, cfg.Adapter)
	}
}

// buildBitcoinFamily bitcoin-rpc + GBT jobmanager + stratum1 方言（M1 竖切原管线）。
func buildBitcoinFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	n := cfg.Nodes[0]
	node := bitcoinrpc.New(cfg.ID, n.URL, n.User, n.Pass)
	node.SetDecimals(decimals)

	hsh, err := hasher.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}

	reg := stratum.NewJobRegistry()
	jm := jobmanager.New(cfg.ID, node, hsh, reg, extraNonce2Size, decimals)
	if err := jm.Init(ctx, cfg.PoolAddress); err != nil {
		return nil, errf("[%s] 初始化矿池地址脚本失败: %v", cfg.ID, err)
	}
	dialect := stratum.NewV1Dialect(cfg.ID, jm)
	jm.SetCallbacks(dialect.BroadcastJob, inst.blockSink(), inst.shareSink())

	return &familyParts{
		status:     node,
		hashps:     node, // bitcoin 系有 getnetworkhashps 真值口径
		classifier: node,
		wallet:     node,
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"stratum1": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}

// blockSink 爆块入账回调（家族共用）。
func (inst *Instance) blockSink() func(ctx context.Context, b core.FoundBlock, rawHex string) error {
	return func(ctx context.Context, b core.FoundBlock, rawHex string) error {
		hashShort := b.Hash
		if len(hashShort) > 12 {
			hashShort = hashShort[:12]
		}
		log.Printf("[%s] ★爆块 height=%d hash=%s finder=%s", inst.cfg.ID, b.Height, hashShort, b.Finder)
		inst.notifyEvent("block_found", "★爆块", map[string]string{
			"height": fmt.Sprint(b.Height), "hash": b.Hash,
			"finder": short(b.Finder), "reward": b.Reward,
		})
		return inst.ledger.RecordBlock(ctx, b, rawHex)
	}
}

// shareSink accepted share 记账回调（家族共用）。
func (inst *Instance) shareSink() func(ctx context.Context, s core.Share) {
	return func(ctx context.Context, s core.Share) {
		_ = inst.ledger.RecordShare(ctx, s, s.Difficulty)
		inst.tracker.Record(s.Address, s.Worker, s.Difficulty, s.At)
	}
}

// 编译期断言：bitcoinrpc.Client 同时满足家族所需的全部面。
var _ adapter.HashPSSource = (*bitcoinrpc.Client)(nil)
