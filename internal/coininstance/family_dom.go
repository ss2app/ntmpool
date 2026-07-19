package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter/domrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/domjob"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildDOMFamily DOM（Mimblewimble + stock RandomX）家族：
// DOM JSON-RPC work × domjob 224B preimage × 复用 CN login/submit/vardiff 外壳
// × rx/0 keyed hasher。coinbase 由节点内置钱包构造和接收，池只做份额/round/余额。
//
// 配置约定：
//   - adapter="dom-rpc"，algo="rx/0"，须 -tags randomx 生产构建
//   - nodes[0].url 默认 http://127.0.0.1:33369
//   - nodes[0].pass 注入 DOM Bearer token（DOM_RPC_TOKEN 或 ~/.dom/rpc_token）
//   - payout.enabled 必须 false；Mimblewimble 交互式付款另做专项实现
//   - 端口 dialect="ntm"（wire 登录/提交兼容现有 CN rx 方言）
func buildDOMFamily(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	if cfg.Algo != "rx/0" {
		return nil, errf("[%s] dom-rpc 只支持 stock RandomX algo=rx/0", cfg.ID)
	}
	if cfg.Payout.Enabled {
		return nil, errf("[%s] DOM Mimblewimble 自动打款尚未实现，payout.enabled 必须为 false", cfg.ID)
	}
	kh, err := hasher.GetKeyed(cfg.Algo)
	if err != nil {
		return nil, err
	}
	n := cfg.Nodes[0]
	c, err := domrpc.New(cfg.ID, n.URL, n.Pass)
	if err != nil {
		return nil, err
	}

	jm := domjob.New(cfg.ID, cfg.Algo, c, kh)
	dialect := stratum.NewCNDialect(cfg.ID, jm)
	sink := inst.blockSink(c) // accepted 后从 /chain/scan 权威回填 subsidy + fees
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit domjob.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     nil, // DOM 节点当前无全网 hash/s 真值 RPC，API 诚实省略
		classifier: c,
		wallet:     domWalletStub{},
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"ntm": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}

// domWalletStub 明确保留未来打款器的 adapter.WalletAdapter 形状，同时防止管理员
// 在 payout=false 阶段误触 fee sweep 时 nil panic。DOM 付款需要 Mimblewimble
// 交互式 transaction/slate 流程，本轮绝不伪装成 SendMany 已实现。
type domWalletStub struct{}

func (domWalletStub) SendMany(context.Context, map[string]string) (string, error) {
	return "", errf("DOM 自动打款未实现（Mimblewimble 交互式交易待专项接入）")
}

func (domWalletStub) TxConfirmations(context.Context, string) (int64, error) {
	return 0, errf("DOM 自动打款未实现")
}
