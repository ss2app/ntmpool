package coininstance

import (
	"context"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
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
//   - nodes[1:] = 可选的异地「感知副节点」，只订阅 longpoll 抢先报新块，
//     不出账、不供模板、无需钱包/pubkey（详见下方接线处注释）
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

	// 新块感知多通道（方案①「多节点竞速」）：
	//   nodes[0] = 主节点，GBT/提交/钱包/打款全走它，语义一个字都不能变；
	//   nodes[1:] = 纯感知通道，各自挂一条独立 longpoll（独立 Client ⇒ 独立
	//               lastLongPollID，互不串状态），谁先看到新块谁先发 TipEvent。
	// 上层 coininstance 按 (Height,Hash) 去重后，仍然只从主节点重拉模板
	// （Refresh 绑的是 jm→c），所以副节点即便落后/分叉也只会多一次幂等 GBT，
	// 绝不会把坏模板喂给矿工，更不参与任何出账。
	// 副产品：各通道 Source 带节点序号，同高度两条日志的墙钟差＝真实相对传播延迟。
	notis := []adapter.Notifier{c.LongPollNotifierLabeled("longpoll#0")}
	for i, extra := range cfg.Nodes[1:] {
		ec := dragonxrpc.New(cfg.ID, extra.URL, extra.User, extra.Pass, cfg.PoolAddress)
		notis = append(notis, ec.LongPollNotifierLabeled(fmt.Sprintf("longpoll#%d", i+1)))
	}

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
		// GBT longpoll：挂等请求在节点上，链头一动立即唤醒（比 ZMQ 少一次拉取 RTT）；
		// 配了多个 nodes 就是多通道竞速，取最先到者（见上方注释）
		notifiers: notis,
	}, nil
}
