// Package payout 打款引擎：批量打款状态机 + 手续费归集/转移。
//
// 状态机（全行业开源池只做到「记 txid」，确认追踪是 NTMPool 的核心超越点）：
//
//	达起付额 → 事务内先扣余额 + 建 batch/payments(created,txid=NULL)   ← 防双花第一步
//	→ WalletAdapter.SendMany 单笔多输出                                ← 绝不逐地址串行(C2)
//	→ 回填 txid(sent) → 周期追踪 TxConfirmations(confirming)
//	→ ≥N 确认且仍在主链 = confirmed ／ 掉链 = failed（人工介入，绝不自动重发,C4）
//
// 每币一把打款锁：正常打款、手续费归集、手续费一键转移（fee sweep）、
// 钱包整备（note/UTXO 合并，adapter.WalletMaintainer）四者串行互斥，
// 与其他币互不影响（R9）。payouts 总开关第一天就有（铁律）。
//
// 钱包整备（隐私链必开，dragonx 实战）：打款周期开始时若碎片数超阈值，
// 先做一批合并（每批尊重单笔输入上限 ~45，多轮推进不长占锁）再打款；
// 整备 tx 同样意图落库（payment_batches kind='consolidate'）并追踪确认。
package payout

import (
	"context"
)

// Engine 每币一个实例。M1/M2 落地，先定接口。
type Engine interface {
	// RunOnce 执行一轮打款周期：分类块 → 入账 → 组批 → 发送 → 追踪确认。
	RunOnce(ctx context.Context) error

	// SetEnabled 热开关（管理后台）。false = accrue-only，只记账不打款。
	SetEnabled(enabled bool)

	// FeeSweep 一键转移手续费地址余额到指定冷地址：
	// 暂停本币打款队列 → 等在途 tx 确认 → 转移 → 恢复。状态机化，可中断可恢复。
	FeeSweep(ctx context.Context, coldAddress string) (txid string, err error)
}
