package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/noctarirpc"
	"github.com/scashcc/ntmpool/internal/cnjob"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildNoctariFamily Noctari (NCTI)：透明比特币 GBT × blob 作业管线（cnjob）×
// CN 方言（NTMminer quark）× quark 无 key 哈希器 × 透明钱包（sendmany/RawTx）。
//
// 与 brisvia 家族的唯一差异：算法 quark（无 seed）而非 rx/brva（RandomX 需 epoch seed）。
// 二者都是透明 Bitcoin 系，共用同一套通用件：cnjob(blob 作业) + stratum.NewCNDialect +
// inst.blockSink/shareSink，以及内嵌 brisviarpc 的标准 GBT/coinbase/组块/透明钱包。
// quark 走 CN 管线仍需 KeyedHasher（cnjob 硬依赖），故用注册在 keyedRegistry 的
// quark keyed 适配（internal/hasher/quark/keyed.go，忽略 key = quark_hash）。
//
// 配置约定：
//   - adapter="noctari-rpc"，algo="quark"（须 -tags quark 构建，否则启动金锚门禁即拒）
//   - poolAddress = 矿池透明收款地址（Noctari/PIVX 系），coinbase 直付于此
//   - nodes[0] = noctari 节点的 RPC（rpcuser/rpcpassword）
//   - 公平启动 PoW 窗口（区块 1..20159）结束后转纯 PoS，锄头/矿池随窗口关闭退场
func buildNoctariFamily(_ context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] noctari-rpc 需要 poolAddress = 矿池收款地址", cfg.ID)
	}

	n := cfg.Nodes[0]
	c := noctarirpc.New(cfg.ID, n.URL, n.User, n.Pass, cfg.PoolAddress)
	c.SetDecimals(decimals)

	jm := cnjob.New(cfg.ID, cfg.Algo, c, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit cnjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     c, // 透明比特币系有 getnetworkhashps 真值口径（PIVX 亦有）
		classifier: c,
		wallet:     c, // 透明钱包：sendmany + RawTx 拆步打款（bitcoinrpc 内嵌）
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"cryptonote": dialect},
		connCount:  dialect.ConnCount,
		// GBT longpoll：挂等请求在节点上，链头一动立即唤醒（比 ZMQ 少一次拉取 RTT）。
		// ZMQ 通道由 nodes[0].zmq 配置驱动，两者并存取最先到者。
		notifiers: []adapter.Notifier{c.LongPollNotifier()},
	}, nil
}
