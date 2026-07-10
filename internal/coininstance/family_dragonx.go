package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter/dragonxrpc"
	"github.com/scashcc/ntmpool/internal/cnjob"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildDragonXFamily DragonX（Hush/Komodo 系 RandomX 隐私链）杂交家族：
// bitcoin 系 JSON-RPC 节点 × blob 作业管线（cnjob）× CN 方言（drg-xmrig/NTMminer）
// × rx/dragonx 双段哈希器 × z_* 隐私钱包（异步 opid 打款 + shield 整备）。
//
// 配置约定：
//   - adapter="dragonx-rpc"，algo="rx/dragonx"（须 -tags randomx 构建，否则启动即拒）
//   - poolAddress = 金库 zs 地址（打款出账源 + z_shieldcoinbase 目标）
//   - nodes[0] = dragonxd RPC（节点即钱包）；部署铁律：DRAGONX.conf 钉 pubkey=
//     到池 R 地址（否则 GBT coinbasetxn 缺失/付错地址，见 _knowledge/地址簿.md）
//   - payout.confirmations=10（用户拍板：10 确认即垫付，coinbase 100 确认成熟后
//     Maintainer 自动 shield 回补金库）
func buildDragonXFamily(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] dragonx-rpc 需要 poolAddress = 金库 zs 地址", cfg.ID)
	}

	n := cfg.Nodes[0]
	c := dragonxrpc.New(cfg.ID, n.URL, n.User, n.Pass, cfg.PoolAddress)

	jm := cnjob.New(cfg.ID, cfg.Algo, c, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit cnjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     c, // komodo 系有 getnetworkhashps 真值口径
		classifier: c,
		wallet:     c, // 同时实现 WalletMaintainer → coininstance 自动接线 shield
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"cryptonote": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}
