package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter/brisviarpc"
	"github.com/scashcc/ntmpool/internal/cnjob"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildBrisviaFamily Brisvia (BRVA)：透明比特币 GBT × blob 作业管线（cnjob）×
// CN 方言（NTMminer rx/brva）× rx/brva 单段 RandomX 哈希器 × 透明钱包（sendmany/RawTx）。
//
// 与 dragonx 家族的差异：dragonx=Hush 系 z_ 隐私钱包 + 双段(外层 double-SHA)；
// Brisvia=透明 Bitcoin 系，钱包与 coinbase 全透明，PoW 单段（rx_hash 直接比 target）。
// 复用同一套通用件：cnjob(blob 作业) + stratum.NewCNDialect + inst.blockSink/shareSink。
//
// 配置约定：
//   - adapter="brisvia-rpc"，algo="rx/brva"（须 -tags randomx 构建，否则启动即拒）
//   - poolAddress = 矿池收款地址（brv1.../tbrv... bech32），coinbase 直付于此
//   - nodes[0] = brisvia bitcoind 的 RPC（rpcuser/rpcpassword）
//   - coinbase 成熟 100 确认（比特币规则）；payout.confirmations 按需垫付
func buildBrisviaFamily(_ context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] brisvia-rpc 需要 poolAddress = 矿池收款地址（brv1.../tbrv...）", cfg.ID)
	}

	n := cfg.Nodes[0]
	c := brisviarpc.New(cfg.ID, n.URL, n.User, n.Pass, cfg.PoolAddress)
	c.SetDecimals(decimals)

	// wire 算法名（对矿工声明）：默认=内部 algo；BRVA 设 stratumAlgo=rx/0 让通用 RandomX 锄头能连。
	// hasher 路由仍用 cfg.Algo（rx/brva → brisviarx），wire 与内部标识解耦。
	wireAlgo := cfg.StratumAlgo
	if wireAlgo == "" {
		wireAlgo = cfg.Algo
	}
	jm := cnjob.New(cfg.ID, wireAlgo, c, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit cnjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     c, // 透明比特币系有 getnetworkhashps 真值口径
		classifier: c,
		wallet:     c, // 透明钱包：sendmany + RawTx 拆步打款（bitcoinrpc 内嵌）
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"cryptonote": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}
