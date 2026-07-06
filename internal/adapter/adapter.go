// Package adapter 定义「节点适配层」——NTMPool 与 miningcore 的根本差异所在：
// 节点对接形态与算法完全正交，任何形态的链都是一等公民。
//
// 五种形态（工厂实战全部踩过，见 docs/02-架构设计.md）：
//   - bitcoin-rpc     标准 getblocktemplate/submitblock/sendmany（BTC 系/绝大多数山寨币）
//   - cryptonote-rpc  门罗系 daemon + wallet RPC
//   - custom-http     自定义 REST 链（zoka 先例）
//   - embedded        无 RPC 链，池内嵌节点库（bitcoin09 先例）
//   - push            节点主动推 work（quantus QUIC 先例）
package adapter

import (
	"context"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// BlockTemplate 是适配器归一化后的挖矿模板。
// Raw 由各币适配器自行解释（GBT JSON / blob prefix / QUIC job……），
// stratum 方言层与算法层只消费归一化字段。
type BlockTemplate struct {
	Height        uint64
	PrevHash      string
	NetworkTarget string // 全网目标（hex，大端语义由适配器归一）
	CoinbaseValue string // 十进制字符串（币）；铁律：空块只 claim subsidy（pitfall C5）
	MinTime       int64
	Raw           any
	FetchedAt     time.Time
}

// ChainStatus 用于孤块比对与确认追踪。
type ChainStatus struct {
	Height    uint64
	TipHash   string
	Synced    bool
	Connected bool
}

// NodeAdapter 是一条链的全部节点交互。实现必须无状态可重连
// （节点重启自愈，绝不用有状态长连接拨号——pitfall C9 libp2p 教训）。
type NodeAdapter interface {
	Name() string

	// Status 节点健康检查（多节点 failover 的依据）。
	Status(ctx context.Context) (ChainStatus, error)

	// GetTemplate 拉最新挖矿模板。
	GetTemplate(ctx context.Context) (*BlockTemplate, error)

	// SubmitBlock 提交完整块。多节点部署时上层会向所有健康节点并发提交（提高传播）。
	SubmitBlock(ctx context.Context, raw any) error

	// BlockHashAt 返回主链上指定高度的块哈希。
	// 孤块判定铁律：成熟入账前用它与我们记录的块 ID 逐字节比对（不是比高度）。
	BlockHashAt(ctx context.Context, height uint64) (string, error)

	// Confirmations 查询我们提交的块当前确认数（<0 = 已不在主链）。
	Confirmations(ctx context.Context, blockHash string) (int64, error)
}

// WalletAdapter 打款侧。与 NodeAdapter 分离：有的链钱包在节点里，有的独立进程。
type WalletAdapter interface {
	// SpendableBalance 池钱包已成熟可花余额（十进制字符串，币）。
	SpendableBalance(ctx context.Context) (string, error)

	// SendMany 单笔多输出批量打款，返回 txid。
	// 铁律：绝不逐地址串行发 tx（UTXO 互撞，pitfall C2）；
	// 调用方保证「先扣余额再调用，失败绝不自动重发」（pitfall C4）。
	// 对 sendmany 有 bug 的链，适配器内部可自行退化实现，但对上层仍是一次调用一个 txid。
	SendMany(ctx context.Context, outputs map[string]string) (txid string, err error)

	// TxConfirmations 追踪打款 tx 的确认数（<0 = 掉出主链/被双花顶掉）。
	// 这是全行业开源池的空白（miningcore 只记 txid），NTMPool 的核心超越点之一。
	TxConfirmations(ctx context.Context, txid string) (int64, error)
}

// Notifier 新块事件源。一个币可挂多个（ZMQ + 轮询兜底并存），
// 上层按 (Height,Hash) 去重取最先到者。轮询通道永远保留——推送断了池不能瞎。
type Notifier interface {
	// Run 阻塞运行，把 tip 变化写入 ch；ctx 取消时退出。实现自带重连。
	Run(ctx context.Context, ch chan<- core.TipEvent) error
}
