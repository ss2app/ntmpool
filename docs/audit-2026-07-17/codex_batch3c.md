你是资深 Go 工程师，为生产多币矿池 NTMPool 实现**复式账本批次 3c（B2 结构化追回/平账 + admin 人工调账强制配平）**。仓库根：`D:\lj2ls\通用锄头+矿池\pool-core`（Go module `github.com/scashcc/ntmpool`）。

## 背景（必读，按序）
- `docs/11-复式账本立项设计.md` §2.2 科目表、§2.3-8/9（B2 事件与人工调账）、§6（B2 并入本批次的决策）。
- `docs/10-底层审计-差距清单与修复方案.md` §2 B2 原始需求：债务结构化、追回/平账、
  「错误收款进 incident_loss/recipient_receivable，**绝不悄悄把矿工余额改负**」、Q5 铁律
  「任何人工余额调整都必须有对手科目，禁止裸的余额加 10」。
- 批次 3a/3b 刚合入的：J4 不变量、journal 双写（journalBuilder/writeJournalShadow/
  appendJournal/JournalShadowAudit）——3c 全部复用这套基建，不要另起炉灶。
- 现有代码：`internal/accounting/{journal.go,journal_audit.go,pgledger.go,memledger.go,accounting.go}`、
  `internal/admin/admin.go`（现有 admin 端点风格：Bearer token/config_audit/结构化响应，
  照着抄）、`internal/coininstance/coininstance.go`（admin 动作→ledger 的穿透方式，
  参考 FeeCollect 的接线）。

## 影子期铁律（继承 3b，不变）
journal 侧校验失败只 P0 + 跳过，绝不阻断业务；但**本批次的三类操作本身就是业务**
（它们改投影），所以：投影变更 + balance_changes/debts 更新 + journal 必须同一事务，
journal 部分照旧走 writeJournalShadow（失败不回滚投影）。

## 任务一：Ledger 三个新方法（PG + Mem 双实现，语义逐项对齐）

### 1. WriteOffDebt —— 坏账核销（B2 核心：debts 永远追不回时的出口）
`WriteOffDebt(ctx, coin, address string, amount string, reason string) error`
- 校验：amount > 0 且 ≤ 该地址 debts 当前 remaining（超出报错拒绝，不做部分静默）。
- 投影：debts.remaining -= amount（PG 按 id 升序逐条冲减；Mem 直接减 map）。
- journal：`DR orphan:loss ｜ CR debt:receivable(address)`，kind=debt_writeoff，
  business_key=`debtwriteoff:<address>:<config_audit_id>`（见任务二，由调用方传入唯一 id）。
- balance_changes：**不写**（余额没变，J4 不涉及）；J3 的 debt:receivable 恒等式因
  投影与 journal 同步减而保持成立——conformance 必须钉死这一点。

### 2. ManualAdjust —— 人工调余额（替代生产上手工 SQL 的唯一合法通道）
`ManualAdjust(ctx, coin, address string, amount string, credit bool, reason string) error`
- amount > 0；credit=true 加余额、false 减（减时余额不足报错拒绝，**绝不把余额改负**）。
- 投影：balances ± amount，且**必须写 balance_changes**（usage=manual_credit/manual_debit，
  tag=`admin:<config_audit_id>`）——这样 J4（Σ流水==余额）自然保持成立。
- journal：credit → `DR manual:adjustment ｜ CR miner:payable(address)`；
  debit → 反向。kind=manual，business_key=`manual:<config_audit_id>`。
  `manual:adjustment` 是非投影科目（J3 不查它），miner:payable 恒等式因投影同步动而保持。
- PG 复用 addBalanceTx（余额+流水同事务）+ writeJournalShadow；Mem 复用
  addBalanceChange + appendJournal。

### 3. RecordIncident / ResolveIncident —— 错误打款事故登记与了结（纯 journal 侧）
`RecordIncident(ctx, coin, id, kind, recipient, amount, memo string) error`
  - kind ∈ {overpay, wrong_address}；amount > 0；id 由调用方保证唯一（进 business_key）。
  - journal：`DR recipient:receivable(recipient) ｜ CR incident:outflow`，
    kind=incident_record，business_key=`incident:<id>`。
`ResolveIncident(ctx, coin, id, outcome, amount, memo string) error`
  - outcome ∈ {recovered, writeoff}；
  - recovered（对方退回了）：`DR incident:outflow ｜ CR recipient:receivable(recipient)`；
  - writeoff（认赔）：`DR incident:loss ｜ CR recipient:receivable(recipient)`；
  - business_key=`incident:<id>:<outcome>`；resolve 前该 incident 必须存在（PG 查
    journal_tx business_key，Mem 查内存 journal），金额不得超过未了结余额（登记额减去
    已了结额），超出报错。recipient 从登记分录反查，不要求调用方重复传。
  - 这三个科目（recipient:receivable/incident:outflow/incident:loss）全部**非投影**，
    J3 不查、J4 不涉及——事故记录靠 journal 本身审计（这正是「绝不悄悄改余额」的实现：
    事故在账上显式可见，矿工余额一分不动）。
- Mem 侧同语义（内存 journal 查询）。

## 任务二：admin 端点（internal/admin + coininstance 穿透）
照现有 admin 风格（Bearer token、JSON body、config_audit 落盘、结构化响应）加四个端点：
- `POST …/debt/writeoff`  body: {address, amount, reason}
- `POST …/balance/adjust` body: {address, amount, credit, reason}
- `POST …/incident/record` body: {id, kind, recipient, amount, memo}
- `POST …/incident/resolve` body: {id, outcome, amount, memo}
硬性要求：
1. **每次调用先写 config_audit**（actor/coin/field/newvalue=参数摘要），拿到 audit 行 id
   后把它作为 `<config_audit_id>` 传给 Ledger 方法（business_key 可追溯到操作审计）。
   config_audit 现有写入机制若拿不到自增 id，就在 admin 层生成一个单调唯一 id（时间戳+
   随机后缀）并同时写进 config_audit 的 newvalue——说明你的选择。
2. reason/memo 必填非空（400 拒绝）；金额走字符串十进制（复用现有 parse，禁浮点）。
3. 全部操作打结构化日志（actor 来自 admin token 名）。
4. mem 币照常可用（Mem 实现同语义）；PG 币重启后 journal/审计可溯。

## 任务三：conformance + 单测
1. conformance 新增子测（Mem/PG 双实现）：
   - 「坏账核销后 J3/J4 仍零失配」：造 debts（孤块欠款场景）→ WriteOffDebt 部分核销 →
     JournalShadowAudit 零失配、Reconcile delta=0（注意：核销减少 debts_net，守恒式
     `confirmed = paid + balances + fees − debts_net`——核实现有守恒式对核销的反应：
     核销让 debt 消失，等式左边不变右边少了 debt 项 → **守恒式会不平**！这是本批次
     必须想清楚的点：守恒式的 debts_net 是「已多付待收回」的抵账项，核销=承认多付的钱
     收不回=池损失，守恒式需要把「累计核销额」计入（如 delta 公式加 writeoff 累计项，
     PG 从 journal 的 orphan:loss 科目取，Mem 记一个 totalWrittenOff）。**修守恒式时
     两实现+reconciliations 落库口径同步改，并加回归测试**。）
   - 「人工调账后 J3/J4 仍零失配」：credit 与 debit 各一笔 → 全绿；debit 超余额被拒。
   - 「事故登记/了结不touching投影」：RecordIncident+ResolveIncident 后 J3/J4 零失配、
     balances/debts 完全没动；重复 id 被拒；resolve 超额被拒。
2. admin 端点单测：token 校验、参数校验、config_audit 落盘、端到端调用 Ledger。

## 硬性要求（继承前批）
1. 复用 3a/3b 基建；打款状态机、分账算法、金额换算、现有五类事件分录一行不动。
2. 金额 int64 聪 + 字符串，禁浮点；日志风格对齐现有。
3. schema 若需改动（如无）保持幂等；**分号只许出现在语句尾**（Migrate 拆语句约定）。
4. 工具链 `C:\ntmbuild\go\bin\go`。跑
   `go build ./... ; go vet ./... ; go test ./internal/accounting/... ./internal/payout/... ./internal/admin/... ./internal/coininstance/...`
   贴真实输出；PG 门控本机 skip 如实说明。
5. 输出总结：文件清单+每处改动、守恒式修改的精确说明、新端点契约、无法自测部分。

现在开始：先读 docs/11 与上述代码（特别是守恒式现状），再动手。注释与总结用中文。