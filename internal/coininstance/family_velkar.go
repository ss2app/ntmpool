package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc"
	"github.com/scashcc/ntmpool/internal/adapter/velkarwallet"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
	"github.com/scashcc/ntmpool/internal/velkarjob"
)

// buildVelkarFamily Velkar(VELK，Kaspa 系) 家族：gRPC 双向流节点 × velkarjob 作业管线 ×
// Kaspa(EthereumStratum) 方言 × VelkarHash 纯 Go 重算。
//
// 配置约定：
//   - adapter="velkar-rpc"，algo="velkarhash"
//   - poolAddress = 矿池 velkar 地址（coinbase 收款；GetBlockTemplate payAddress）
//   - nodes[0].url = velkard gRPC host:port（如 127.0.0.1:26210，可带 grpc:// 前缀）
//   - feePercent=5（用户拍板）；payout.enabled=false（打款第一期延后 = accrue-only，
//     shares 计入 PPLNS 但不自动打款，先手动 wallet/walletd）
//   - vardiff 用小数难度（Diff1=2^224-1 口径：难度 1≈2^32 次哈希，见 velkarjob）
//
// 0ms 触发 = gRPC NewBlockTemplate/BlockAdded 推送（velkar 无 Bitcoin ZMQ）+ 2s 轮询兜底。
func buildVelkarFamily(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	if cfg.PoolAddress == "" {
		return nil, errf("[%s] velkar-rpc 需要 poolAddress = 矿池 velkar 地址（coinbase 收款）", cfg.ID)
	}
	n := cfg.Nodes[0]
	node, err := velkarrpc.New(cfg.ID, n.URL, cfg.PoolAddress)
	if err != nil {
		return nil, err
	}

	jm := velkarjob.New(cfg.ID, cfg.Algo, node)
	dialect := stratum.NewKaspaDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit velkarjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	// 打款钱包（可选）：配置了独立 walletd 端点（cfg.wallet.url）才接线，否则 wallet=nil =
	// accrue-only（payout.enabled 应为 false，engine 跳过所有 wallet 调用）。
	// velkar-walletd 常驻已解锁 + 经 wRPC 连节点；确认追踪用 node(velkarrpc) 作 mempool 探针。
	var wallet adapter.WalletAdapter
	if cfg.Wallet.URL != "" {
		wc, werr := velkarwallet.New(cfg.ID+"-wallet", cfg.Wallet.URL, cfg.PoolAddress, cfg.Decimals, node)
		if werr != nil {
			return nil, werr
		}
		wallet = wc
	}

	return &familyParts{
		status:     node,
		hashps:     nil, // 第一期不报全网算力（velkar 有 estimate RPC，后续接；绝不反推 pitfall C7）
		classifier: node,
		wallet:     wallet, // 配了 walletd 则自动打款（Kaspa UTXO 签名走 walletd），否则 nil=accrue-only
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"kaspa": dialect},
		connCount:  dialect.ConnCount,
		// gRPC NewBlockTemplate/BlockAdded 0ms 推送（唯一主通道，velkar 无 ZMQ）；2s 轮询兜底在 coininstance
		notifiers: []adapter.Notifier{node.Notifier()},
	}, nil
}
