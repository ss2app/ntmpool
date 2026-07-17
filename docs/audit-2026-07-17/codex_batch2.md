你是资深 Go 工程师，为生产多币矿池 NTMPool 做**打款审计闭环**加固。仓库根：`D:\lj2ls\通用锄头+矿池\pool-core`（module `github.com/scashcc/ntmpool`）。

## 背景（必读）
先读理解需求：
- `docs/10-底层审计-差距清单与修复方案.md`（§2 B档 = 本次任务；§1 已达标项=禁改；§4 十条不变量#8#9）
先读现有代码摸清结构（**禁改核心打款写路径逻辑，只加只读审计层 + 冻结**）：
- `internal/payout/engine.go`（打款引擎：拆步/sendmany/守恒 Reconcile 冻结/追踪；重点看 Frozen/Unfreeze/emit/reconcile 段）
- `internal/accounting/accounting.go`（Ledger 接口 + Reconcile 守恒）
- `internal/adapter/adapter.go`（NodeAdapter/WalletAdapter/RawTxWallet 接口）
- `internal/adapter/bitcoinrpc/`（bitcoin 钱包实现，listtransactions 等）
- `internal/payout/pgstore.go`（batch 持久化，含 plannedtxid/txid）

## 本次要实现（B1 为主，B3 顺带；B2 本次不做只留 TODO）

### B1. chain-to-book 反向对账（抓"未知出账"和"上链了没记账"）——本次核心
目标：内部守恒 Reconcile 只证明"账自洽"，不证明"币真在链上"。新增一个**只读**对账器，
双向核对链上真相与账本，命中异常**冻结该币打款 + P0 告警**（fail-closed，安全方向）。

1. 新增可选适配器接口（放 `internal/adapter/adapter.go`）：
   ```go
   // ChainAuditor 可选：能列出钱包近期出账，供 chain-to-book 反向对账。
   // 适配器实现它才参与审计；不实现的币跳过并记 "audit unavailable"（诚实，绝不假装已审）。
   type ChainAuditor interface {
       // ListRecentOutbound 返回钱包近期"转出"交易（含 txid、各输出地址+金额、确认数）。
       ListRecentOutbound(ctx context.Context, sinceHeight uint64) ([]OutboundTx, error)
   }
   ```
   为 `bitcoinrpc` 实现它（listtransactions/listsinceblock 取 category=send）。其余适配器**暂不实现**（接口就位即可）。

2. 新增 `internal/payout/chainaudit.go`（或合适位置）`ChainToBookReconciler`，每打款周期跑（挂在现有 tick 上）：
   - **方向 A（未知出账 = 私钥泄漏/绕过/bug 的唯一早期信号）**：列钱包近期出账，每笔按 txid 反查
     `payout.Store` 是否有对应 batch（PlannedTxID/TxID 匹配）。发现"有转出但无对应 batch"的出账
     → **立即冻结该币打款**（复用 engine 现有 frozen 机制）+ emit P0 事件 `chain_audit_unknown_outbound`。
     （要排除已知的非打款出账：fee_collect/fee_sweep/consolidate 批次、找零回自身地址——按 batch.Kind 与自有地址集合过滤。）
   - **方向 B（上链了没记账）**：若适配器能列 coinbase 收入（可选，能力不足就跳过并记日志），
     核对"钱包收到挖矿收入但 blocks 表无 round 记录"→ emit 告警 `chain_audit_missing_round`
     （本次只告警，补建 round 留 TODO；docs/05 场景 B 已设计）。
   - 对**不实现 ChainAuditor 的币**：记一条 info 日志 "chain_audit_unavailable coin=X"，不冻结、不报错（诚实缺口）。
   - 冻结判定必须**只读+保守**：查链失败/能力不足绝不冻结（避免误冻结停打款）；只有"确证的未知出账"才冻结。

3. 与现有守恒 Reconcile 关系：二者并存互补。守恒查"账自洽"，chain-audit 查"账 vs 链"。
   两者任一冻结都走同一个 frozen 开关（人工 Unfreeze）。

### B3. 结算/打款版本化补齐（顺带，低风险）
- 核对 shares/blocks/rewards/payout batch 是否都 stamp 了 policy 版本（settlement/fee/rounding）。
- **缺哪个补哪个**（schema 加列 + 结算/打款时固化当时版本），已有的别重复。
- 目的：改费率/确认数/结算规则一律新 version，历史事件绝不被当前配置重新解释。
- 若改动面过大/牵动 schema migration，**先只加"打款批次记录当时 fee/confirmations 版本"这一最小闭环**，其余留 TODO 列清。

### 顺带小修（批次1审查发现）
- `internal/stratum/server.go` 的 `m.rejectLog` map 只增不删（每个曾触发 reject 日志的 IP 留一条 window），
  长期有内存增长。加一个简单上限/惰性清理（如超过 N 条时清理过期 window，或用带 TTL 的结构）。

## 硬性要求
1. **禁改** docs/10 §1 已达标项核心逻辑（拆步打款/sendmany 超时不重发/守恒冻结/孤块比对/DeductForPayout 等）。
   本次只**新增只读审计层 + 冻结触发**，不动打款写路径的任何一行判断。
2. chain-audit 必须 **fail-closed 的反面**：查链失败/能力不足→**不冻结**（继续跑，记日志）；只有确证未知出账→冻结。
3. 所有新逻辑**带单元测试**：注入"钱包有笔无 batch 的出账"→ 冻结 + emit；注入"出账都能匹配 batch"→ 不冻结；
   注入"适配器不实现 ChainAuditor"→ 跳过不报错；rejectLog 清理测试。
4. 用假 WalletAdapter/Store 测，别连真节点。金额用最小单位整数，禁浮点。
5. 若本机有 Go 工具链就跑 `go build ./... && go vet ./... && go test ./internal/payout/... ./internal/adapter/... ./internal/stratum/...` 贴结果；没有就说明，不臆造。
   （提示：Go 1.25 可能已解压在 `C:\Users\WU\AppData\Local\Temp\ntmpool-go1.25.0-complete2\go`，设 GOROOT 用它。）
6. 改完输出：改了哪些文件、每个干了什么、新增接口/事件名、哪些币能审哪些跳过、B3 补了哪些版本字段/哪些留 TODO、无法自测的部分。

现在开始：先读上述文件，再动手。中文注释和总结。
