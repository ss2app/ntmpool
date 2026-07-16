package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter/scashrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/scashrx"
	"github.com/scashcc/ntmpool/internal/scashjob"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildScashFamily 装配 SCASH 独立家族：标准 V1 wire × SCASH-specific job ×
// salted RandomX commitment × 112B submitblock × Bitcoin 透明钱包。
func buildScashFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] scash-rpc 需要 poolAddress", cfg.ID)
	}
	pow, err := scashrx.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}
	n := cfg.Nodes[0]
	client := scashrpc.New(cfg.ID, n.URL, n.User, n.Pass)
	client.SetDecimals(decimals)

	reg := stratum.NewJobRegistry()
	jobs := scashjob.New(cfg.ID, client, pow, reg, extraNonce2Size, decimals)
	if err := jobs.Init(ctx, cfg.PoolAddress); err != nil {
		return nil, errf("[%s] 初始化 SCASH 矿池地址脚本失败: %v", cfg.ID, err)
	}
	dialect := stratum.NewV1Dialect(cfg.ID, jobs)
	sink := inst.blockSink() // 标准单资产 coinbase，不挂 ZEPH reward 回填
	jobs.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, block core.FoundBlock, rawHex string, submit scashjob.SubmitFunc) error {
			return sink(ctx, block, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status: client, hashps: client, classifier: client, wallet: client,
		jobs: jobs, dialects: map[string]stratum.Dialect{"stratum1": dialect},
		connCount: dialect.ConnCount,
	}, nil
}
