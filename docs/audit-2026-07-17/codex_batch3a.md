你是资深 Go 工程师，为生产多币矿池 NTMPool 做**复式账本批次 3a（阶段0）+ 批次2 遗留两小修**。仓库根：`D:\lj2ls\通用锄头+矿池\pool-core`（Go module `github.com/scashcc/ntmpool`）。

## 背景（必读，按序）
- `docs/11-复式账本立项设计.md` —— 本次任务 = §3-阶段0 + §6「批次2 遗留两发现」。整份设计先通读。
- `docs/10-底层审计-差距清单与修复方案.md` §1 已达标项 = **禁改核心逻辑，只许叠加**。
- 现有代码：`internal/accounting/pgledger.go`（重点 addBalanceTx / Reconcile）、
  `internal/accounting/memledger.go`、`internal/accounting/ledger_conformance_test.go`、
  `internal/payout/engine.go`（NewEngine 里 chainAudit 接线处）、`internal/payout/chainaudit.go`、
  `internal/adapter/bitcoinrpc/bitcoinrpc.go`（recentWalletHistory / ListRecentOutbound / ListRecentCoinbase）、
  `internal/adapter/bitcoinrpc/chainaudit_test.go`（假节点测试基建，扩展它）、
  `internal/coininstance/`（config→engine 的接线）、`internal/config/config.go`。

## 事实前提（已实测，不要怀疑也不要重做）
2026-07-17 已对 dragonx/btc09 两生产库只读核对：逐地址 Σbalance_changes == balances.amount
双向零失配。即 J4 不变量今天在生产成立，本次只是把它固化成在线检查。

## 任务一：J4 不变量固化（docs/11 §2.4 J4 + §3-阶段0）

### PG 侧（pgledger.go）
1. `Reconcile` 内追加 J4 检查（纯 SQL，两个方向）：
   - 逐地址 `balances.amount` vs `Σ balance_changes.amount`（LEFT JOIN，失配计数 + 绝对差之和）；
   - 反方向：balance_changes 聚合后 `Σ≠0` 但 balances 无行的孤儿流计数。
2. 任一失配：打 P0 结构化日志（前 3 个失配样本：address / balance / stream_sum / diff）+
   **Reconcile 返回非零 delta**（守恒 delta 非零时优先返回守恒 delta；守恒平但 J4 破时返回
   J4 绝对差之和，保证引擎现有「delta≠0→冻结打款」路径自然生效，引擎代码不用改）。
3. reconciliations 表**不加列**（避免 schema churn）；J4 结果只走日志 + delta。
4. 性能：用现有 `idx_balance_changes_pool_addr` 索引的聚合查询即可；不要引入新表。

### Mem 侧（memledger.go）
1. MemLedger 补内存流水：每个余额变动点（与 pgledger addBalanceTx 调用点一一对应——
   ConfirmBlock 入账、直付 credit/paid、孤块 payment_refund/orphan_reversal、
   DeductForPayout、RefundPayout）追加一条内存流水记录 `{addr, delta, usage, tag}`，
   usage/tag 字符串与 PG 侧完全一致（对称性是本仓库双实现互相钉死语义的机制）。
2. Mem `Reconcile` 同样追加 J4：Σ流水 per addr == balances map。破则同样非零 delta+日志。
3. 流水 slice 保持私有，不加公共 API（它是未来 journal 的前身，不是产品功能）。

### Conformance（ledger_conformance_test.go）
1. 每个现有子测场景结束后追加断言：J4 在 Mem/PG 双实现都成立（建议做成公共 helper，
   在现有 runConformance 骨架的每场景收尾处调用；怎么接入你看现有测试结构定，但必须
   覆盖全部现有场景，包括孤块/直付/带费孤块/退款）。
2. 新增负向子测「绕过流水改余额必被 J4 抓住」：
   - PG：测试内直接 `UPDATE balances SET amount=amount+1`（用测试持有的 *sql.DB）→
     Reconcile 返回非零 delta；
   - Mem：直接改 balances map（同包白盒）→ 同样非零。
   断言日志路径不 panic、delta 非零、修回后 Reconcile 恢复 0。

## 任务二：mem 币默认禁用 chain-audit（批次2 遗留①）

现状：`payout/engine.go` NewEngine 无条件构造 ChainToBookReconciler。mem 账本币（无
postgresDsn，如 noctari）重启后账簿清零，若回看窗（1000 块）内钱包有过真打款 →
FindByTxID 全查无 → 误判未知出账 → P0 误冻结。修法（docs/11 §6 决策已定）：
1. `payout` 的 Config 加 `ChainAuditEnabled *bool`（三态）：nil=auto（由接线方决定）、
   显式 true/false 覆盖。
2. `coininstance` 接线：有 postgresDsn（持久 store）→ auto=true；无 → auto=false。
   config.json 的 coin.payout 增可选字段 `chainAudit`（bool，省略=auto），穿透到上面。
3. Engine：禁用时不构造 reconciler，启动打一行日志说明原因
   （`chain_audit_disabled reason=volatile-store` 之类），避免静默。
4. 单测：mem store + auto → 无审计；mem + 显式 true → 有；PG 路径 auto → 有。
   （PG 路径若现有测试基建不便，允许用「带 FindByTxID 的假持久 store + 显式 true」等价覆盖，
   写清楚即可。）

## 任务三：PIVX 系 blockheight 缺失的游标修复（批次2 遗留②）

现状：PIVX v5（noctarid）listsinceblock 条目无 `blockheight` 字段 → walletHistoryEntry.
BlockHeight 恒 0 → chainaudit 游标 advanceAuditCursor 永不前进 → 每轮全量重扫回看窗。
真实数据：noctari 钱包 generate 条目 conf=7753、blockheight 缺失。修法：
1. `bitcoinrpc` 侧：当条目 `BlockHeight==0 && Confirmations>0` 时，用
   `height = tip − Confirmations + 1` 反推（tip 用 getblockcount，一次审计调用内只取一次、
   可复用 sinceHeight==0 分支已取的值；derived ≤0 时放弃反推保持 0）。
   `Confirmations<=0`（mempool/冲突）不反推，维持 0。
2. 反推只影响返回的 Height 字段（游标推进用），不改变其它语义。
3. 扩展 `chainaudit_test.go` 假节点：新增「PIVX 风格无 blockheight」测试用例——条目只有
   confirmations → 断言返回的 Height 被正确反推、且连续两轮 ListRecentOutbound 的游标
   （通过 payout.ChainToBookReconciler 跑两轮 Run 观察 since 前进也行，或直接断言返回值）
   前进不重扫。

## 硬性要求（继承批次1/2）
1. **禁改** docs/10 §1 已达标项核心逻辑；本次只碰 accounting（叠加 J4/流水）、payout 接线、
   bitcoinrpc 高度反推、config。**打款状态机、分账算法、金额换算一行不动。**
2. 所有新逻辑带单元测试（table-driven 优先）；conformance 是重点交付。
3. 配置字段走 `internal/config/config.go` 加字段+JSON tag+默认值；老 config 不写新字段时
   行为 = auto（向后兼容，不回退）。
4. 日志风格对齐现有 `log.Printf`（中文前缀 `[payout %s]` / `[会计 %s]` 等，grep 现有格式）。
5. 金额只走现有 int64 聪/字符串路径，禁浮点。
6. 工具链：`C:\ntmbuild\go\bin\go`（Go 1.26）。跑
   `go build ./... ; go vet ./... ; go test ./internal/accounting/... ./internal/payout/... ./internal/adapter/bitcoinrpc/... ./internal/coininstance/... ./internal/config/...`
   并贴真实输出；PG 门控测试本机无 DSN 会 skip，如实说明（Claude 会在 5850U 真 PG 复核）。
7. 输出总结：改了哪些文件、每处干了什么、新 config 字段与默认值、你无法自测的部分。

现在开始：先读 docs/11 与上述代码，再动手。注释与总结用中文。