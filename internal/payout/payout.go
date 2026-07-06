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

// 具体实现见 engine.go（*Engine）、memstore.go（内存 BatchStore）。
// 打款状态机、每币锁、崩溃恢复、fee sweep 的设计说明见本文件顶部注释与 docs/05。
