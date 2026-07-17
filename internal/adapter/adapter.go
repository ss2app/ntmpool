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
	"errors"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// ErrNoHeightIndex 由无「高度→hash」索引的链（Kaspa 类 DAG）的 BlockHashAt 返回。
// 打款引擎见此 sentinel 跳过「按高度取主链块 hash 逐字节比对」这道成熟闸，改为信任
// 该链 Confirmations 内建的主链判定（DAG 用 isChainBlock：不在 selected chain = 孤块）。
var ErrNoHeightIndex = errors.New("adapter: chain has no height→hash index (DAG); orphan check relies on Confirmations")

// ErrNotBroadcast 包装「确定未广播」的打款失败——如交易在构造阶段就被拒（Kaspa KIP-9
// storage mass 超单笔上限）。打款引擎见此 sentinel 可安全退回余额；这与 SendMany 超时的
// 「unknown（可能已广播）」状态相反，后者绝不自动退回（防双付，交人工）。适配器只在
// 能确证交易未离开本进程（未 broadcast）时才包这个 sentinel。
var ErrNotBroadcast = errors.New("adapter: payout tx was not broadcast (safe to refund)")

// BatchPlanner 是打款侧的可选能力：按链特有的单笔约束（如 Kaspa KIP-9 storage mass：
// 输出金额越小单笔越装不下）把一批打款输出预分成若干子批，每子批可安全放进一笔交易。
// 打款引擎对实现了本接口的钱包，逐子批独立原子打款；未实现的链 = 整批一笔（引擎默认）。
type BatchPlanner interface {
	// PlanBatches 把 outputs（地址→十进制金额）分成若干子批。返回空或单元素 = 不拆。
	// 实现须保证并集 = 输入、无重复地址（引擎按子批独立扣款/记账，重复会重复扣）。
	PlanBatches(outputs map[string]string) []map[string]string
}

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
	// DAG 链（Kaspa 类）无高度→hash 索引，返回 ErrNoHeightIndex，打款引擎改走 Confirmations。
	BlockHashAt(ctx context.Context, height uint64) (string, error)

	// Confirmations 查询我们提交的块当前确认数（<0 = 已不在主链）。
	// height 是我们记录的块高度：只有按高度查块 API 的链（zoka 类 REST）靠它定位，
	// bitcoin/门罗系按 hash 查的忽略即可。
	Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error)
}

// BlockRewardSource is an optional daemon capability for chains whose template
// reward is not the amount ultimately paid in the pool's accounting asset.
// It is queried with the authoritative block id returned by submission.
type BlockRewardSource interface {
	BlockReward(ctx context.Context, blockHash string) (string, error)
}

// WalletAdapter 打款侧。与 NodeAdapter 分离：有的链钱包在节点里，有的独立进程。
type WalletAdapter interface {
	// SendMany 单笔多输出批量打款，返回 txid。
	// 铁律：绝不逐地址串行发 tx（UTXO 互撞，pitfall C2）；
	// 调用方保证「先扣余额再调用，失败绝不自动重发」（pitfall C4）。
	// 对 sendmany 有 bug 的链，适配器内部可自行退化实现，但对上层仍是一次调用一个 txid。
	SendMany(ctx context.Context, outputs map[string]string) (txid string, err error)

	// TxConfirmations 追踪打款 tx 的确认数（<0 = 掉出主链/被双花顶掉）。
	// 这是全行业开源池的空白（miningcore 只记 txid），NTMPool 的核心超越点之一。
	TxConfirmations(ctx context.Context, txid string) (int64, error)
}

// OutboundOutput 是钱包转出交易的一个对外输出。Amount 使用链的最小单位整数，
// 绝不经过浮点；IsMine 表示适配器已确证该地址属于本钱包，供审计层排除找零/自转。
type OutboundOutput struct {
	Address string
	Amount  int64
	IsMine  bool
}

// OutboundTx 是钱包近期一笔转出交易。Height=0 表示仍在 mempool 或节点未提供高度。
type OutboundTx struct {
	TxID          string
	Outputs       []OutboundOutput
	Confirmations int64
	Height        uint64
}

// ChainAuditor 可选：能列出钱包近期出账，供 chain-to-book 反向对账。
// 适配器实现它才参与审计；不实现的币跳过并诚实记录 audit unavailable。
type ChainAuditor interface {
	// ListRecentOutbound 返回钱包近期转出交易（txid、输出、确认数）。
	// sinceHeight=0 表示由适配器选取一个保守的近期回看窗口。
	ListRecentOutbound(ctx context.Context, sinceHeight uint64) ([]OutboundTx, error)
}

// CoinbaseReceipt 是钱包收到的一笔挖矿收入。Amount 使用最小单位整数；
// BlockHash 是与内部 blocks round 反查的稳定键。
type CoinbaseReceipt struct {
	TxID          string
	BlockHash     string
	Amount        int64
	Confirmations int64
	Height        uint64
}

// CoinbaseAuditor 是 chain-to-book 方向 B 的可选能力。不能列 coinbase 的钱包
// 不实现它；审计层只跳过该方向，绝不伪装成已审。
type CoinbaseAuditor interface {
	ListRecentCoinbase(ctx context.Context, sinceHeight uint64) ([]CoinbaseReceipt, error)
}

// SpendableBalanceSource is an optional wallet capability used by the payout
// engine's pre-flight solvency gate. Wallets without it retain legacy behavior.
type SpendableBalanceSource interface {
	SpendableBalance(ctx context.Context) (string, error)
}

// TxTracker 可选扩展：比 TxConfirmations 更精确的打款 tx 状态——除确认数外还报告
// 交易「是否仍被节点知道」（在 mempool 或已上链）。打款确认追踪器据此安全判定
// 「广播出去后又丢失」（known=false 且过 grace = 确定既不在 mempool 也不在链上 →
// 退回矿工余额下轮重付；2026-07 bitcoin09 batch 91/95 广播后节点重启丢 mempool 的漏洞）。
// 只有 confirmations 的钱包退化为「conf<0 才判丢失」（保守，绝不误退在 mempool 排队的 tx）。
type TxTracker interface {
	// TxStatus 返回 (confirmations, known)：
	//   confirmations <0 = 掉出主链/被双花顶掉；≥0 = 主链确认数（0=未上链）。
	//   known = 交易仍在 mempool 或链上（节点认识它）。known=false + conf≤0 = 确定丢失。
	TxStatus(ctx context.Context, txid string) (confirmations int64, known bool, err error)
}

// Notifier 新块事件源。一个币可挂多个（ZMQ + 轮询兜底并存），
// 上层按 (Height,Hash) 去重取最先到者。轮询通道永远保留——推送断了池不能瞎。
type Notifier interface {
	// Run 阻塞运行，把 tip 变化写入 ch；ctx 取消时退出。实现自带重连。
	Run(ctx context.Context, ch chan<- core.TipEvent) error
}

// WalletMaintainer 可选扩展：钱包整备（note/UTXO 定时合并）。
// 隐私链（DragonX/Zcash 系）coinbase 必须先 shield，池 z 地址积累大量小 note；
// 打款要花掉几百个 note → 交易体积超限/构造超时/被网络拒收（dragonx 实战：
// z_shieldcoinbase/z_mergetoaddress 单笔惯例上限 ~45-50 个、shield 10min 超时、分页处理）。
// bitcoin 系的小额 coinbase UTXO、门罗系的碎 output 同理。
// 引擎按配置定时调用；调用方持有该币打款锁（与正常打款/fee sweep 串行互斥），
// 整备 tx 同样走意图落库（payment_batches kind='consolidate'）。
type WalletMaintainer interface {
	// NeedsMaintenance 报告当前碎片程度（note/UTXO 数）是否达到触发阈值，desc 供日志。
	NeedsMaintenance(ctx context.Context) (need bool, desc string, err error)

	// Maintain 执行一轮整备（一批 shield / merge / consolidate），返回产生的 txid。
	// 一轮只做一批（尊重单笔输入上限），碎片多时由引擎多轮推进，不长时间独占打款锁。
	Maintain(ctx context.Context) (txids []string, err error)
}

// RawTxWallet 可选扩展（bitcoin 系支持）：拆步打款，txid 在广播前就确定，
// 崩溃恢复零歧义（docs/05 场景 A）。打款引擎优先走此接口；
// 不支持的链退回 SendMany + unknown 状态人工恢复。
type RawTxWallet interface {
	// PrepareSendMany 构造并签名批量交易但【不广播】。SegWit 后签名即定 txid。
	// 调用方先把 (txid, rawtx) 落库再调 Broadcast。
	PrepareSendMany(ctx context.Context, outputs map[string]string) (txid string, rawtx string, err error)

	// Broadcast 广播已签名交易。必须幂等：节点报 already-in-mempool /
	// already-known / txn-already-known 一律视为成功（恢复重播同一笔 rawtx 不可能双花）。
	Broadcast(ctx context.Context, rawtx string) error

	// TxExists 查该 txid 是否已在 mempool 或链上（崩溃恢复：判定「广播出去没有」）。
	TxExists(ctx context.Context, txid string) (bool, error)
}
