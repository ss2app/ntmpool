// Package accounting 会计层：share 计权、round/PPLNS 分账、孤块与追缴、守恒对账。
//
// 分层原则（对齐 miningcore PayoutManager 模式）：
//   - Ledger 是【纯状态】——只管 share/block/balance/debt 的记账数学，不碰节点。
//   - 依赖节点的孤块判定（查确认数、主链 hash 逐字节比对）由打款引擎编排后
//     调 ConfirmBlock/OrphanBlock 通知 Ledger。
//
// 蓝本 = bitcoin09 pool/payout.go（孤块作废+debts）× btx pool（Postgres 状态机）。
//
// 不变量（Reconcile 每周期校验，破则冻结打款+告警）：
//
//	Σ已确认块奖励 = Σ已付 + Σ矿工余额 + Σ手续费 + Σ在途打款 + Σ债务净额
//
// 铁律：
//   - 孤块判定用主链块 hash 逐字节比对（打款引擎侧，不是比高度）。
//   - 算力统计窗口与 PPLNS 窗口彻底分开（pitfall C6）。
//   - PPLNS 窗口不满时按实际份额归一化付满 100%（Snipa22 #349 冷启动 bug）。
//   - 孤块 round 的 share 并入后续窗口（NOMP 派，对矿工最友好）——本实现中
//     share 存于统一滚动窗口，孤块不单独裁剪，天然并入。
package accounting

import (
	"context"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// Ledger 纯状态会计接口。实现：memory（M1/测试）/ postgres（M4 多实例）。
type Ledger interface {
	// RecordShare 记一条已接受 share（计权难度 = vardiff 计权后的 creditDiff）。
	RecordShare(ctx context.Context, s core.Share, weight float64) error

	// RecordBlock 记一个池找到的块（初始 status=submitting，rawHex 供恢复重播/审计）。
	RecordBlock(ctx context.Context, b core.FoundBlock, rawHex string) error
	// MarkBlockPending 块已成功提交给节点（submitting→pending）。
	MarkBlockPending(ctx context.Context, coin, hash string) error

	// PendingBlocks 取所有待确认块（打款引擎轮询它们做确认追踪与孤块比对）。
	PendingBlocks(ctx context.Context, coin string) ([]core.FoundBlock, error)

	// ConfirmBlock 块成熟且仍在主链：按 PPLNS/SOLO 分账（扣费、抵 debts、写审计流水）。
	ConfirmBlock(ctx context.Context, b core.FoundBlock, feePercent float64) error
	// OrphanBlock 块被主链甩掉：作废奖励；若已预打款垫付则生成 debts。
	OrphanBlock(ctx context.Context, b core.FoundBlock) error

	// PayableBalances 达起付额、可打款的地址→金额（打款引擎组批用）。
	// perAddrThreshold 覆盖默认起付额（矿工 -p mp= 设置）。
	PayableBalances(ctx context.Context, coin string, defaultThreshold float64, perAddr map[string]float64) (map[string]string, error)
	// DeductForPayout 事务内先扣余额（打款状态机第一步：先扣后发，防双花 C4）。
	DeductForPayout(ctx context.Context, coin string, outputs map[string]string, batchID int64) error
	// RefundPayout 打款失败退回余额（仅当确证未广播）。
	RefundPayout(ctx context.Context, coin string, outputs map[string]string, batchID int64) error

	// Reconcile 守恒对账，delta 非 0 由调用方冻结打款并告警。
	Reconcile(ctx context.Context, coin string) (delta string, err error)

	// Snapshot 供 API/日志读的只读快照。
	Snapshot(ctx context.Context, coin string) (Stats, error)
}

// Stats 池会计快照。
type Stats struct {
	Balances     map[string]string
	TotalPaid    string
	TotalFees    string
	BlocksFound  int
	Confirmed    int
	Orphaned     int
	DebtsNet     string
	WindowShares int
}

// HashrateSampler 算力时序（独立于 Ledger：60s 打点 → 10min 桶 → 24h 曲线，R6）。
type HashrateSampler interface {
	Sample(ctx context.Context, coin string, at time.Time) error
	Prune(ctx context.Context, retain time.Duration) error
}
