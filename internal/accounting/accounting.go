// Package accounting 会计层：share 计权、round/PPLNS 分账、孤块判定与追缴、守恒对账。
//
// 实现蓝本 = bitcoin09 pool/payout.go（孤块作废+debts）× btx pool/payout.rs+db.rs（Postgres 状态机）。
// M1 落地；此处先定死接口与不变量，防止实现期走样。
//
// 不变量（守恒对账器每周期校验，破则冻结打款+告警）：
//
//	Σ已确认块奖励 = Σ已付 + Σ矿工余额 + Σ手续费 + Σ在途打款 + Σ债务净额
//
// 铁律：
//   - 成熟入账前：按高度取主链块 hash 与我们记录的 hash 逐字节比对（不是比高度）。
//   - 算力统计窗口与 PPLNS 窗口彻底分开（pitfall C6）。
//   - PPLNS 窗口不满时按实际份额归一化付满 100%（Snipa22 #349 冷启动 bug）。
//   - 孤块 round 的 share 并入下一 round（NOMP 派，对矿工最友好）。
package accounting

import (
	"context"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// Ledger 会计接口。实现：postgres（生产）/ memory（测试）。
type Ledger interface {
	// RecordShare 记一条已接受 share（计权难度 = vardiff.Judge 返回的 creditDiff）。
	RecordShare(ctx context.Context, s core.Share) error

	// RecordBlock 记一个池找到的块（status=pending）。
	RecordBlock(ctx context.Context, b core.FoundBlock) error

	// ClassifyBlocks 推进块状态机：确认追踪 + 主链 hash 逐字节比对。
	// 返回本轮转为 confirmed / orphaned 的块。
	ClassifyBlocks(ctx context.Context, coin string) (confirmed, orphaned []core.FoundBlock, err error)

	// CreditRound 对 confirmed 块做 PPLNS/SOLO 分账（扣手续费、抵扣 debts、写审计流水）。
	CreditRound(ctx context.Context, b core.FoundBlock, feePercent float64) error

	// OrphanRound 孤块处理：share 并入当前窗口；若已入账（预打款垫付）则生成 debts。
	OrphanRound(ctx context.Context, b core.FoundBlock) error

	// Reconcile 守恒对账，delta 非 0 时由调用方冻结打款并告警。
	Reconcile(ctx context.Context, coin string) (delta string, err error)
}

// HashrateSampler 算力时序（独立于 Ledger：60s 打点 → 10min 桶 → 24h 曲线，R6）。
type HashrateSampler interface {
	Sample(ctx context.Context, coin string, at time.Time) error
	Prune(ctx context.Context, retain time.Duration) error
}
