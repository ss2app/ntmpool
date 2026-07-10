# BUG：NTMPool zoka 爆块漏判（真矿工挖到的块没被识别/提交）

**状态：✅已定位并修复（2026-07-08）。根因 = 空块奖励让爆块落库崩，与难度/target 换算无关。代码已修 + 回归测试 + 真 PG 验证；待重新部署冒烟复验。**

---

## 一句话根因

zoka 节点的 `/mining/template` **不下发 reward 字段** → `customhttp.GetTemplate` 置 `CoinbaseValue=""` →
`FoundBlock.Reward=""` → `PGLedger.RecordBlock` 把空串 `""` 塞进 `blocks.reward`（**numeric 列**）→
Postgres 报 `invalid input syntax for type numeric: "" (SQLSTATE 22P02)` → `RecordBlock` 返回 err →
`blockSink`（family_bitcoin.go:72-75）「意图先落库」失败即 `return err`，**永不执行 `submit()`** →
**块从不提交节点 = 真矿工白挖**，且没建 block 行 → API/DB 显示 `blocks=0`。

**MeetsTarget、难度公式、hash 字节序全部正确，且与 miningcore 逐行一致。爆块检测本身没坏——坏在检测到之后的记账落库。**

## 铁证（服务器日志 + 真 PG）

- `/data/ntmpool-zoka/logs/pool.log`（GBK 编码）解码后满屏：
  `[zoka] ★爆块意图落库失败 height=2439: ERROR: invalid input syntax for type numeric: "" (SQLSTATE 22P02)`
- **24 次落库失败，13 个不同真块高度**被丢：2439/2442/2443/2444/2447/2451/2452/2459/2461/2467/2468/2470/2473。
- 时间窗 `17:27:54 → 19:09:45`（~102 分钟），**0 次成功提交**（grep `submitted|pending|panic|fatal` = 0）。
- 真 PG（`ntmpool-pg` 容器）验证：`blocks.reward` 类型 = `numeric`；
  `SELECT ''::numeric` → 复现同款 22P02；`SELECT '0'::numeric` → `0` 成功。

## ★为什么之前的「8× 难度换算」理论是错的（避免重蹈）

旧排查的核心线索「shares 表 maxd=429万 << 网络难度 2^25=3355万，约 8× gap」是**误读**：

- `shares.Difficulty` 存的是 `vardiff.Judge` 的 **creditDiff**（= `s.cur`/`s.prev`，即 **vardiff 档位**），
  **不是**该 share 实际 hash 难度（见 `internal/vardiff/vardiff.go:101-112`、`internal/cnjob/cnjob.go` 的
  `credit, _ := sub.Judge(shareDiff)` → `Difficulty: credit`）。
- zoka 端口配置 `vardiff.maxDiff = 5000000（500万）`，所以 `Difficulty` 列**物理上永远 ≤ 500万**，
  maxd=429万 只是 vardiff 爬到的最高档，撞在 500万 上限附近——**跟 hash 运气/爆块毫无关系**。
  哪怕挖到满足 2^25 的真块，落库的 `Difficulty` 也只会记 ≤500万（Judge 返回 s.cur）。
- 「8× ≈ 2^3」纯属巧合（500万 与 2^25 恰好差 ~6.7×，且 429万 更近 2^22）。
- 教训：**别用一个只记 vardiff 档位的列去反推「爆块是否被漏判」**。要看爆块，去看 blocks 表 / 日志的
  爆块路径，不是 shares.maxd。

## 修复

`internal/accounting/pgledger.go` `RecordBlock`：空奖励归一为 `"0"` 再入库（`parseAmount("")` 返回 (0,nil)
不报错，所以 Go 侧校验放行、坑全在 PG numeric 列）。一条块**绝不能因金额格式化问题而丢**（丢块 = 白挖，
比崩溃更隐蔽危险）——这是记账层最后一道防线。

回归测试：`internal/accounting/ledger_conformance_test.go` 新增子用例「空奖励块可落库不丢块」，
跑 MemLedger + PGLedger 两实现（PG 由 `NTMPOOL_PG_DSN` 门控）。`go build ./...` + Mem 测试全绿；
真 PG SQL 层已复现坑并验证修复。

## 待办（重新部署复验）

1. ✅ **已做（2026-07-08）**：修复推 CI（commit 75553a0），CI 双 job 全绿——`build` job 带真 postgres:16
   跑双实现 conformance（含新回归「空奖励块可落库」在**真 PG** 上通过）+ `randomx` job 出 linux 二进制。
   artifact `ntmpool-linux-x64-randomx` 已 SFTP 部署到 `/data/ntmpool-zoka/ntmpool`（sha256
   `8ffdd0c6…`，旧二进制备份 `ntmpool.bak.20260708`=`9e38a124…`）。启动冒烟通过：干净 boot、连 PG、
   randomx 加载、stratum 17011 监听、优雅停。
2. ✅ **poolAddress 占位符已排除**：curl 真节点 `/mining/template?address=X` 用两个不同 address 返回
   **同一个** `pool_address: zpriv:d67d3e99…` + 同一 blob_prefix → zoka-node 忽略传入 address、用自己
   硬编码的 pool2 地址。占位符 `zoka-shadow-pool` 无害，提交的块落真 pool2 地址。
3. **⏳ 待你在场执行 = 重新引流真矿工复验爆块闭环**：209 systemd `zoka-stratum-tunnel.service` 落地端口
   `7011→17011` + daemon-reload+restart（permitopen 已放行 17011）。**盯 `pool.log` 出现
   「★爆块 height=… hash=…」而非落库失败**，确认 RecordBlock 成功→SubmitBlob 成功→节点返回权威 hash。
   引流前建议先 TRUNCATE 影子 PG 测试数据（干净口径）。
4. （非阻塞）打款币若用 custom-http 且模板无 reward，应由适配器从节点 `/blocks/{height}` 的
   `reward_atoms` 回填真奖励；zoka 是 accrue-only 不打款，记 0 无影响。

## 排查证据/环境保留

- 服务器 103.80.18.140：`ntmpool-pg` 容器（127.0.0.1:15432，user/db=ntmpool，pw=ntmpool_shadow_pw）保留，
  `shares` 表 2680 条真矿工 share 在库；`pool.log` 24 条爆块落库失败为铁证。
- `/data/ntmpool-zoka/`：config.json、二进制 ntmpool、logs 保留。
- 重启排查：`cd /data/ntmpool-zoka && ./ntmpool -config config.json -payouts=false`（连真节点 7100、17011）。
