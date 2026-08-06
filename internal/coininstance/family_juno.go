package coininstance

import (
	"context"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/junorpc"
	"github.com/scashcc/ntmpool/internal/cnjob"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildJunoFamily Juno Cash（Zcash 6.x 系 RandomX 隐私链）家族：
// bitcoin 系 JSON-RPC 节点 × blob 作业管线（cnjob）× CN 方言（官方 junorig = xmrig fork）
// × rx/juno 哈希器（= stock RandomX，与 rx/0 同引擎）× z_* 隐私钱包（异步 opid 打款）。
//
// 与 dragonx 家族同构，配置约定：
//   - adapter="juno-rpc"，algo="rx/juno"（须 -tags randomx 构建，否则启动即拒）
//   - poolAddress = 金库 j1 统一地址（Orchard；打款出账源 + z_shieldcoinbase 目标）
//   - nodes[0] = junocashd RPC（节点即钱包）；部署铁律：junocashd 必须配
//     -mineraddress=<池的透明 t1 地址>（GBT coinbasetxn 来源；必须透明地址，
//     Orchard UA coinbase 无 vout 池取不到奖励金额）
//   - nodes[1:] = 可选异地副节点：① longpoll 多通道竞速抢先报新块
//     ② 爆块并发广播。不出账、不供模板、无需钱包（dragonx 方案①⑤同款）
//   - fCoinbaseMustBeShielded=true：coinbase 成熟(100确认)后 Maintainer 自动
//     z_shieldcoinbase 进金库，打款从 Orchard 出（payout.confirmations 建议 ≥110
//     给 shield 留余量）
func buildJunoFamily(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] juno-rpc 需要 poolAddress = 金库 j1 统一地址", cfg.ID)
	}

	n := cfg.Nodes[0]
	c := junorpc.New(cfg.ID, n.URL, n.User, n.Pass, cfg.PoolAddress)

	notis := []adapter.Notifier{c.LongPollNotifierLabeled("longpoll#0")}
	var peers []*junorpc.Client
	for i, extra := range cfg.Nodes[1:] {
		ec := junorpc.New(cfg.ID, extra.URL, extra.User, extra.Pass, cfg.PoolAddress)
		peers = append(peers, ec)
		notis = append(notis, ec.LongPollNotifierLabeled(fmt.Sprintf("longpoll#%d", i+1)))
	}
	c.SetBroadcastPeers(peers)

	jm := cnjob.New(cfg.ID, cfg.Algo, c, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit cnjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     c, // juno 保留 getnetworkhashps 真值口径
		classifier: c,
		wallet:     c, // 同时实现 WalletMaintainer → coininstance 自动接线 shield
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"cryptonote": dialect},
		connCount:  dialect.ConnCount,
		// GBT longpoll：挂等请求在节点上，链头一动立即唤醒；多 nodes = 多通道竞速
		notifiers: notis,
	}, nil
}
