//go:build noid

package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/noidrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/noidjob"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildNoidFamily ParanO(1)d (NOID)：noid-rpc 适配器（官方节点 HTTP JSON-RPC 2.0 +
// Bearer）× noidjob 作业管理器（单飞行槽模板持有 + fanout）× noidhttp 矿工面方言
// （官方 parano1d-miner / NTMminer-noid 零改动 HTTP JSON-RPC）× poseidon2b/noid 哈希器
// （cgo 链官方 Rust staticlib，逐字节共识）。
//
// 配置约定（须 -tags noid 构建，否则金锚自检 / buildNoidFamily 拒启动）：
//   - adapter="noid-rpc"，algo="poseidon2b/noid"，decimals=6（1 NOID=1e6 μNOID）
//   - nodes[0].url = 节点 RPC（http://127.0.0.1:9401），nodes[0].pass = mining-key
//     （Bearer；支持 ${NOID_MINING_KEY}，config.Load 时 os.ExpandEnv 解析）
//   - poolAddress = 展示占位（NOID 付款走节点 mining-key 钱包，miner_address 进
//     pow_fields[8..9]；池不持钥、per-miner 走 PPLNS 分账）
//   - 端口 dialect="noidhttp"；一 pplns 一 solo，vardiff 独立
//   - payout.enabled=false（暂无 WalletAdapter；NOID tx 有 144 块 epoch 过期需重签，
//     留后续 RawTxWallet 拆步实现。Confirmations/BlockHashAt 已实现供成熟判定，
//     confirmations 建议 20 = 硬终局 18 块 + 余量）
//   - 全网算力无真值口径 → hashps=nil（API 省略，绝不反推，铁律 C7）
func buildNoidFamily(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	// 确保 poseidon2b/noid 已注册（二进制须 -tags noid 编 + cmd/ntmpool/hashers_noid.go
	// 空导入挂载）；未注册 = 编译时漏挂算法，早失败别晚出废块。
	if _, err := hasher.Get(cfg.Algo); err != nil {
		return nil, err
	}

	n := cfg.Nodes[0]
	c := noidrpc.New(cfg.ID, n.URL, n.Pass) // pass = mining-key（Bearer）

	jm := noidjob.New(cfg.ID, c)
	dialect := stratum.NewNoidDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit func(context.Context) (string, error)) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     nil, // NOID 无全网算力真值口径（铁律 C7）
		classifier: c,   // BlockHashAt + Confirmations
		wallet:     noidWalletStub{},
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"noidhttp": dialect},
		connCount:  dialect.ConnCount,
		// tip 通知：无推送，150ms 轮询 getChainInfo（Source="poll"；2s Status 兜底另有保留）。
		notifiers: []adapter.Notifier{c.TipNotifier()},
	}, nil
}

// noidWalletStub payout.enabled=false 下永不触碰的桩（无 WalletAdapter：NOID tx 绑
// 144 块 epoch 过期需重签，留后续 RawTxWallet 实现）。防御性返回 unsupported。
type noidWalletStub struct{}

func (noidWalletStub) SendMany(context.Context, map[string]string) (string, error) {
	return "", errf("noid 暂无钱包适配器（payout 关；NOID tx 144 块 epoch 过期需重签，待 RawTxWallet）")
}
func (noidWalletStub) TxConfirmations(context.Context, string) (int64, error) {
	return 0, errf("noid 暂无钱包适配器")
}
