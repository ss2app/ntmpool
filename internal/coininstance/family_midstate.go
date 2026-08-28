package coininstance

import (
	"context"
	"encoding/hex"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/midstaterpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/midjob"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildMidstateFamily midstate (MDS)：midstate-rpc 适配器（官方节点 HTTP RPC）×
// midjob 作业管理器（model-a coinbase 直付分账，docs/07）× midstate 方言
// （裸TCP+换行JSON，NTMminer -a midstate 现役协议）× midvdf 纯 Go 哈希器。
//
// 配置约定：
//   - adapter="midstate-rpc"，algo="midvdf"，decimals=9（1 gMDS=1e9 units，
//     账本整数=链上 units 精确对齐）
//   - nodes[0].url = 官方节点 RPC（如 http://172.30.0.10:8545，务必内网）
//   - poolAddress = 费/残差收款地址（hex64，模板费输出直達；池不持钥）
//   - payout.enabled=false 永不开——「打款」= coinbase 直付（爆块即到账），
//     classify/ConfirmBlock 照跑（直付块按快照入账）；minPayout 兼作尘埃阈值
//   - 端口 dialect="midstate"；vardiff 连续难度（start ≈ 2^15，target 12s）
//   - 全网算力无真值口径 → hashps=nil（API 省略，绝不反推）
func buildMidstateFamily(_ context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	h, err := hasher.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}
	if !isHex64Addr(cfg.PoolAddress) {
		return nil, errf("[%s] poolAddress 必须是 64-hex（32 字节 midstate 地址）", cfg.ID)
	}

	c := midstaterpc.New(cfg.ID, cfg.Nodes[0].URL)

	minUnits := uint64(0)
	if cfg.Payout.MinPayout != "" {
		if sat, err := accounting.ParseAmount(cfg.Payout.MinPayout, decimals); err == nil && sat > 0 {
			minUnits = uint64(sat)
		}
	}
	jm := midjob.New(cfg.ID, c, h, inst.ledger, midjob.Config{
		FeePercent:     cfg.Payout.FeePercent,
		MinPayoutUnits: minUnits,
		PplnsFactor:    cfg.Payout.PplnsFactor,
		PoolAddress:    cfg.PoolAddress,
		Decimals:       decimals,
	})
	dialect := stratum.NewMidstateDialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit midjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     nil, // midstate 无全网算力真值口径（API 省略，铁律 C7）
		classifier: c,
		wallet:     midstateWalletStub{},
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"midstate": dialect},
		connCount:  dialect.ConnCount,
		// tip 通道：midstate 节点无 ZMQ/longpoll，用 300ms 探 /state 变化触发
		// force refresh，把 2s 轮询的秒级滞后压到亚秒级（滞后=矿工在旧 job 上
		// 白挖，60s 出块下 2.5s ≈ 4% 算力）。轮询兜底仍在 startLoops 里保留。
		notifiers: []adapter.Notifier{c.TipNotifier(0)},
	}, nil
}

// midstateWalletStub midstate 无池端转账腿（打款=coinbase 直付；费提现走既有
// consolidate SOP）。打款引擎 enabled=false 永不触碰这里——防御性返回。
type midstateWalletStub struct{}

func (midstateWalletStub) SpendableBalance(context.Context) (string, error) { return "0", nil }
func (midstateWalletStub) SendMany(context.Context, map[string]string) (string, error) {
	return "", errf("midstate 无池端转账腿（coinbase 直付，docs/07）")
}
func (midstateWalletStub) TxConfirmations(context.Context, string) (int64, error) {
	return 0, errf("midstate 无池端转账腿")
}

func isHex64Addr(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
