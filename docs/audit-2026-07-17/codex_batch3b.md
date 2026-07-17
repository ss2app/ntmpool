你是资深 Go 工程师，为生产多币矿池 NTMPool 实现**复式账本批次 3b（阶段1：journal 影子双写）**。仓库根：`D:\lj2ls\通用锄头+矿池\pool-core`（Go module `github.com/scashcc/ntmpool`）。

## 背景（必读，按序）
- `docs/11-复式账本立项设计.md` **全文**——本次任务 = §2 目标模型 + §3-阶段1 + §5 起账。
  科目表（§2.2）、分录映射（§2.3）、不变量（§2.4）、business_key 约定照抄，不要自创。
- `docs/10-底层审计-差距清单与修复方案.md` §1 = 禁改核心逻辑。
- 批次 3a 刚合入的：MemLedger 内存流水、Reconcile J4 检查——3b 在其上叠加，别推倒。
- 现有代码重点：`internal/accounting/pgledger.go`（ConfirmBlock/confirmDirectTx/OrphanBlock/
  DeductForPayout/RefundPayout/addBalanceTx/Reconcile）、`memledger.go` 对应方法、
  `db/schema.sql`、`internal/coininstance/`（PGLedger 构造+Migrate 接线处）、
  `internal/payout/engine.go`（RunOnce 的 Reconcile 调用处）、`internal/notify/`（Hub 事件风格）。

## 总原则（影子期铁律）
1. **balance_changes、balances、blocks、debts、block_credits 的现有写入代码一行不动**；
   journal 是纯追加面。
2. **journal 绝不影响业务结果**：journal 写入失败/借贷不平 → 打 P0 日志 + 本笔跳过 journal，
   业务事务照常提交（影子期 journal 无权威；J3 影子审计会暴露漂移）。
   实现上：journal 腿收集后先在内存断言 Σ=0，不平则整笔不写；写库失败同样只日志。
   ⚠ 但 journal 写入必须与业务变更**同一个 SQL 事务**（成功路径下原子）；「失败不阻塞」
   指 journal 侧校验不通过时主动跳过，不是把 journal 拆到独立事务。做法：业务逻辑全部
   完成后、tx.Commit 前组装并写 journal；写入报错时记日志并继续 Commit 业务部分——
   注意 Postgres 里同事务内语句失败会污染整个事务（abort state），所以 journal 写入前
   用 SAVEPOINT，失败 ROLLBACK TO SAVEPOINT 再 Commit。
3. 阶段1 **不启用** payout:in_transit、不给费归集入账（§2.3-4/7 已定），不做权威翻转。

## 任务一：schema（db/schema.sql，幂等追加）
按 docs/11 §2.1 加 `journal_tx` / `journal_entry` 两表 + 索引（CREATE TABLE IF NOT EXISTS）。
注意：journal_entry.amount NUMERIC，写入走现有 formatAmount（聪→十进制字符串）路径。

## 任务二：journalBuilder（internal/accounting 新文件 journal.go）
- builder：`newJournalTx(kind, businessKey, policyVersion, memo)` + `leg(account, address string, deltaSat int64)`；
  `writeTx(ctx, tx)` 前断言 Σlegs==0（int64 聪域），不平→返回哨兵错误（调用方按总原则2 处理）。
- 金额为 0 的腿不写行（例如 fee=0 的块没有 fee 腿）。
- policy_version 本阶段统一 `pre-registry`；memo 记关键参数（confirm: feePercent/solo；
  deduct/refund: batchID；orphan: takeΣ/remainΣ）。
- business_key 约定（§2.3）：`confirm:block:<hash>` / `orphan:block:<hash>` /
  `deduct:batch:<n>` / `refund:batch:<n>` / `opening:<ISO日期>`。
  UNIQUE(poolid,business_key) 冲突 = 编码 bug 的响亮信号：SAVEPOINT 回滚 + P0 日志（总原则2）。

## 任务三：五类事件双写（PG 侧，严格按 docs/11 §2.3 的腿）
1. **ConfirmBlock 普通/SOLO**：DR block:revenue R ｜ CR miner:payable(a) net_a、
   CR pool:fee_accrued F、CR debt:receivable(a) repaid_a。
   repaid_a = offsetDebtsTx 返回的抵债量（amt−credit），需要把该值带出来给 builder。
2. **ConfirmBlock 直付**（confirmDirectTx）：入账腿同上（fee=E−Σcredit）+ 直付腿
   DR miner:payable(a) paid_a ｜ CR payout:settled。
3. **OrphanBlock**（status==confirmed 分支）：DR miner:payable(a) take_a、
   DR debt:receivable(a) remain_a、DR pool:fee_accrued F ｜ CR block:revenue R。
   F 从 blocks.feeamount 读（孤块翻转前该行还是 confirmed 时写的值）。
   直付块另有退款腿：DR payout:settled ｜ CR miner:payable(a) paid_a。
   ⚠先在注释里写清配平推导（Σtake+Σremain=Σamt=R−F），代码里 builder 断言兜底。
4. **DeductForPayout**：DR miner:payable(a) amt_a ｜ CR payout:settled。
5. **RefundPayout**：DR payout:settled ｜ CR miner:payable(a)。已退款幂等路径（现有
   balance_changes tag 查重命中提前 return）不写 journal。
未确认块的 OrphanBlock（status 非 confirmed，无入账可回滚）不写 journal。

## 任务四：opening 起账（PG）
- `PGLedger.EnsureJournalOpening(ctx)`：`SELECT EXISTS(... kind='opening')` 守卫，只跑一次。
  按 docs/11 §5：miner:payable=balances 逐地址、debt:receivable=debts 逐地址、
  pool:fee_accrued=Σfeeamount(confirmed)、block:revenue=Σreward(confirmed)、
  payout:settled=现行 paid 推导值（−Σ balance_changes usage∈(payment,payment_refund)），
  对手腿 opening:equity 轧平。policy_version='opening-snapshot'。
- 接线：coininstance 在 Migrate 之后、实例启动时调用（每币一次，失败只告警不阻启动——
  影子期原则）。
- 全空库（新币）opening 也要写（全 0 腿省略即空 journal_tx 也行，写一笔空 opening 便于
  「已起账」判定）。

## 任务五：Mem 侧对称（memledger.go）
- 内存 journal：`[]memJournalTx{key, kind, legs []memJournalLeg}`，五类事件同样的腿与
  business_key；Σ=0 断言不平则跳过并记日志（与 PG 同语义）。
- Mem 无 opening（内存起点即空账，J3 天然全量成立）。

## 任务六：J3 影子审计
- Ledger 接口新增：
  `JournalShadowAudit(ctx, coin) (mismatches []JournalMismatch, checked bool, err error)`
  `JournalMismatch{Account, Address, JournalAmount, ProjectionAmount string}`（金额十进制串）。
- PG 实现（全 SQL 聚合）：docs/11 §2.4 J3 五条恒等式逐一比对（miner:payable per addr、
  debt:receivable per addr、pool:fee_accrued、block:revenue、payout:settled），返回全部失配
  （每类限量前 N=20 条防爆）。查询失败 → checked=false（不告警不误报）。
- Mem 实现：对内存 journal 同五条。
- 接线（payout/engine.go RunOnce）：Reconcile 之后调用，失配 → Hub 事件
  `journal_shadow_mismatch`（去重：同一 (account,address) 只在翻转时报一次，风格参考
  runChainAudit 的 auditUnknown map）+ 结构化日志。**只告警，绝不冻结**（阶段2 才翻权威）。

## 任务七：conformance 扩展（ledger_conformance_test.go）
1. 公共 helper：每个现有场景收尾断言（继 3a 的 J4 之后追加）：
   - J1：每笔 journal_tx 借贷 Σ=0（PG 用 SQL SUM GROUP BY txref；Mem 遍历）；
   - J3：JournalShadowAudit 返回零失配、checked=true；
   - J2：journal_tx business_key 无重复。
2. 新增子测：
   - 「孤块回滚 journal 配平」：确认→孤块全流程后 J3 仍零失配（含带费、含 debts 场景）；
   - 「直付块 journal」：直付确认+直付孤块回滚后 J3 零失配；
   - 「重复 business_key 被拒」：同一块二次 confirm（幂等提前返回）不产生第二笔 journal；
   - 「opening 幂等」：EnsureJournalOpening 跑两遍只有一笔 opening（PG）；
   - 「带存量数据起账」：先造余额/债务/已确认块，再 EnsureJournalOpening → J3 立即零失配（PG）。
3. 影子不阻塞验证：测试里人为造一笔不平 builder 调用（可导出一个 test hook 或直接单测
   builder），断言返回哨兵错误、不 panic。

## 硬性要求（继承批次1/2/3a）
1. 现有投影/流水写入逻辑一行不动；打款状态机、分账算法、金额换算不碰。
2. 金额全程 int64 聪 + formatAmount 字符串，禁浮点。
3. 日志风格对齐现有格式；新表不加任何触发器/存储过程（纯应用层）。
4. 工具链 `C:\ntmbuild\go\bin\go`。跑
   `go build ./... ; go vet ./... ; go test ./internal/accounting/... ./internal/payout/... ./internal/coininstance/...`
   贴真实输出；PG 门控本机 skip 如实说明（Claude 上 5850U 真 PG 复核）。
5. 输出总结：文件清单+每处改动、接口变更点（JournalShadowAudit 影响的实现/假件）、
   你无法自测的部分。

现在开始：先读 docs/11 全文与上述代码，再动手。注释与总结用中文。