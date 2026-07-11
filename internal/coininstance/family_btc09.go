package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/btc09rpc"
	"github.com/scashcc/ntmpool/internal/btc09job"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// btc09MaxTargetBits 09C 主网 maxTarget（难度标尺：diff = maxTarget/target，
// diff1 ≈ 65537 hash/share——"廉价" share，矿工难度是 sub-1 量级）。链常量非部署参数。
const btc09MaxTargetBits = 0x1f00ffff

// buildBtc09Family Bitcoin 09 (09C)：btc09-http 适配器（对接 coins/bitcoin09/
// poolnode 守护）× btc09job 作业管理器 × btc09 方言（裸TCP+换行JSON，NTMminer
// -a btc09 现役协议）× argon2id-btc09 纯 Go 哈希器。
//
// 配置约定：
//   - adapter="btc09-http"，algo="argon2id-btc09"（纯 Go，无需 randomx tag）
//   - nodes[0].url = poolnode 的矿池 API（默认 127.0.0.1:4409，务必内网）
//   - nodes[0].zmq 可选 = poolnode 的 -zmq 端点（longpoll + ZMQ + 轮询三通道并存）
//   - poolAddress 仅信息展示：coinbase 收款地址由 poolnode -pooladdr 决定
//   - 端口 dialect="btc09"；vardiff 是 sub-1 量级（start 0.05 / min 0.02）
func buildBtc09Family(_ context.Context, cfg config.CoinConfig, _ int, inst *Instance) (*familyParts, error) {
	h, err := hasher.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}

	n := cfg.Nodes[0]
	c := btc09rpc.New(cfg.ID, n.URL)

	jm := btc09job.New(cfg.ID, c, h, btc09MaxTargetBits)
	dialect := stratum.NewBtc09Dialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit btc09job.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     c,
		hashps:     c, // poolnode /networkhashps = 120 块真实时间跨度真值口径
		classifier: c,
		wallet:     c,
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"btc09": dialect},
		connCount:  dialect.ConnCount,
		// 模板 longpoll：挂等在 poolnode 上，新 tip/新模板立即返回（ZMQ 走通用接线）
		notifiers: []adapter.Notifier{c.LongPollNotifier()},
	}, nil
}
