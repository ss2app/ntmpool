# NTMPool 分期路线图

> 原则：每期结束都有「能真跑」的里程碑，用工厂现有的币做真机验收（先 regtest/dev 链，再 live）。
> 语言 Go；仓库 `scashcc/ntmpool`（私有）；编译走 GitHub CI（规避中文路径铁律）。

## M0 — 立项 + 骨架（本期）

- [x] 需求规格（docs/01）
- [x] 调研：本地 4 池 + miningcore 魔改盘点；市面开源池 + stratum 方言调研
- [x] 架构设计（docs/02）
- [x] Go 工程骨架：核心接口（NodeAdapter/Hasher/Dialect）、config、日志、vardiff、Postgres schema
- [x] 私有仓库建立 + 推送 + CI 骨架

## M1 — 单币竖切（第一个能挖的版本）

目标：**一个 bitcoin-rpc 系币在 regtest 上端到端**：锄头连上 → share 池端重算 → 爆块 submitblock → 确认追踪 → PPLNS 入账 → sendmany 打款 → miningcore 形状 API。

已完成（本地 Go 1.26.4 编译+测试，CI 全绿）：
- [x] `internal/btcwork`：难度/目标换算、coinbase 构造、merkle、header、字节序编码 —— 创世块 + 主网块 #100000 真实金锚验证
- [x] `internal/stratum` V1 方言：subscribe/authorize/configure/notify/submit + 去重 + vardiff 驱动 + 看门狗 + nonce 窗分段 + 三件套响应；内存管道全握手测试
- [x] `internal/vardiff` 三件套（一步 grace + 限频 + lowdiff/badpow 拆分）
- [x] `internal/adapter/bitcoinrpc`：NodeAdapter + WalletAdapter + RawTxWallet + PayoutScript
- [x] `internal/jobmanager` 粘合核心：GBT 解析 → 组 job → 池端重算校验 share → 命中组块提交；**全链路自洽测试（组块 header 哈希 == 池重算逐字节）**
- [x] `internal/hasher` sha256d + 金锚门禁

已完成（续）：
- [x] 会计：内存 Ledger（PPLNS 按份额分账 + 守恒对账 + 孤块 debts 追缴 + 起付额 + 事务性扣款，整数聪不过浮点）—— Postgres 实现推迟到 M4 多实例（内存版单实例够用且 e2e 已验证全链路）
- [x] 打款引擎：孤块分类器（确认数 + 主链 hash 逐字节比对）+ RawTxWallet 拆步打款（广播前落库）+ 崩溃恢复重播 + 守恒冻结 + FeeSweep + payouts 开关
- [x] CoinInstance 拼装 + main.go 拉起各币 + 优雅关停
- [x] **纯 Go 端到端测试**（假 bitcoind + 真 CoinInstance + 真 stratum 矿工 btcwork 挖矿）：全链路[锄头→share→爆块→submitblock→确认→PPLNS→拆步打款] + 孤块路径[主链 hash 逐字节比对判 ORPHANED 不误打款] 双双通过，进 CI
- [x] testminer CLI（可复用的 stratum 测试锄头）

- [x] **真 bitcoind regtest 字节级验收**（2026-07-06，服务器 103.80.18.140 起 Bitcoin Core 28.0 官方二进制 regtest，验完即删）：**真 bitcoind 通过 submitblock 收下我们组的块（高度 101→102），块 102 hash 与池日志逐字节一致，coinbase 含 `/NTMPool/` 池标记，真钱包广播真打款 txid（47.5=50−5%费）**——最终铁证到手，M1 核心竖切 DONE。

M1 收尾：
- [x] **miningcore 形状公共 API**（2026-07-06）：`internal/api` + `internal/hashrate`。
  - 端点：`/api/pools`、`/api/pools/{id}`、`/blocks`、`/payments`、`/miners`、`/miners/{addr}`（完整地址自查）、`/performance`（24h 曲线）。形状对齐 miningcore（前端零改），超越点以追加字段体现：打款状态机 status 对外透明、孤块追缴 debt 透明。
  - 隐私（R14.6）：列表一律脱敏「前6…后4 + HMAC-SHA256 匿名 ID」（`maskSecret` 配置，空则随机）；矿工完整地址自查密链，per-IP 60/min 限速防枚举。
  - 算力采样器 `internal/hashrate`：滚动 10min 精确窗口（即时算力）+ 10min 桶×24h（曲线），与 PPLNS 窗口彻底分开（pitfall C6）；全网算力走 `getnetworkhashps` 真值 30s 缓存（pitfall C7），API 层零 RPC；金额十进制字符串直通 JSON number 不过 float（金额铁律）。
  - 顺手修正：爆块 share 此前不计入 PPLNS 窗口/算力 → 已补（miningcore 同款语义）；新增同高度双爆块测试（一 confirm 一 orphan、绝不双份入账、守恒 delta=0、重跑幂等）。
  - 测试：api httptest 全端点形状/脱敏/分页/限速 + hashrate 数学 + e2e 里对真 CoinInstance API 冒烟（编译期断言 Instance 实现 api.Pool）。
- [ ] 可选：真 ZMQ 新块通知（替代轮询）

## M2 — 热管理 + 管理后台 API

- [x] **热参数**（2026-07-06）：费率/确认数/起付额（`payout.Engine.SetParams`，新 round 生效不追溯）；端口增删启停（`stratum.Manager` 真监听器热启停，e2e 实测存量端口无扰）；币开关 newConnectionsEnabled（只拒新连存量不断）。
- [x] **管理后台 API `internal/admin`**：独立端口 + Bearer token（constant-time；无 token = 整面拒绝服务）。端点：status、coins/{id}（节点密码 redacted）、payout PATCH（部分更新）、payout/run、reconcile、unfreeze（守恒冻结人工解冻）、feesweep、feecollect、ports 增删启停、newconns、bans 增删查、miners/{addr}/settings 查/重置。**每次变更写 config_audit.jsonl + 落盘 config.state.json（只存热参数子集，密钥绝不落盘）；重启 overlay 热状态（以最后热状态为准）**。
- [x] **ban 管理 `internal/banlist`**：IP/CIDR、TTL 过期、Strikes 退避计数、JSON 持久化；stratum accept 最外圈拒连（全池共享一份）。自动 ban（invalidPercent 阈值）留 M3 连接治理一起做。
- [x] **矿工设置 `internal/minersettings`**（R5）：密码字段 `d=`（固定难度，连接级，vardiff.SetFixed 夹 Min/Max）/`mp=`/`pl=` 解析；首个带密码连接绑定设置密码（HMAC+盐文件跨重启稳定），之后改设置需同密码；起付额下限=池默认、上限可配；打款引擎走 PayableBalances perAddr 覆盖；管理后台可查/可 bypass 重置。
- [x] 手续费转移：FeeSweep（费地址→冷address）+ FeeCollect（池钱包→费地址归集），共用「意图落库→广播前落库」批次流程，冻结时拒绝。双地址分离校验 M0 起已有。
- [x] **通知 `internal/notify`**（2026-07-06 收官）：Hub 异步有界队列（Publish 非阻塞，队列满丢弃计数——通知是旁路，绝不拖累挖矿/打款）+ webhook（通用 JSON POST）+ Telegram bot 双 sink（单 sink 失败互不影响）。事件：block_found/confirmed/orphaned、payout_sent/failed、reconcile_frozen（翻转才发一次）、node_down/up（连续 5 次失败翻转，恢复翻转，不刷屏）、fee_collected。去重责任在事件源（状态翻转），孤块连发这类刷屏恰恰必须看到、不节流。
- [x] **手续费自动归集**（同日）：打款周期尾部（持每币锁天然与打款串行）算未归集额 = Ledger 计提总费 − Σ fee_collect 批次（含在途，保守防重复），达 `feeCollect.minAmount` 自动池钱包→费地址；热参数可改；防重复归集有测试。

**M2 = 全部 DONE（2026-07-06）**。

## M3 — 多币 + 多方言（本期）

- [x] **选型化**（2026-07-07）：coininstance 拆链家族构建器（`family_bitcoin.go`/`family_blob.go`），按 `cfg.Adapter` 绑定 节点适配器×算法×作业管理器×方言；公共面 = statusSource/jobPipe/HashPSSource（可选，无真值口径的链 API 直接省略全网算力，绝不反推）。
- [x] **CryptoNote 方言 `stratum/dialect_cn.go`**：login/job/submit/keepalived/getjob；worker 识别 rigid>pass>+worker、固定难度 `.N`/`+N`/`d=`、algo 能力协商（不含本币算法明确拒）、标准错误 message 集合、vardiff 一步 grace 经 Judge 闭包下沉、难度变更随新 job 下发（CN 无 set_difficulty）。链差异（blob 布局/target 编码/hash 字节序/组块载荷）全部下沉 CNShareHandler。
- [x] **blob 作业管理器 `internal/cnjob` + 数学库 `internal/cnwork`**：nonce 字段统一模型（低 SearchLen 字节矿工滚 + 高位连接 tag）盖住两类实战布局——zoka/CRB（尾部 8 字节、4+4、target 64hex-BE、hash 大端）与 monero 系（偏移 39、4 字节、nicehash 分片 3+1、target compact-LE、hash 小端，免 merkle 重建，代价=每 job ≤256 连接）；池端重算 badpow tripwire（矿工 result 逐字节比对）+ 连接 tag 防伪造（忽略矿工带回高位，池侧重建）；blob 链块 id ≠ PoW hash → 先交后记拿节点权威块 hash（TODO M4 Postgres 意图先落库）。
- [x] **适配器三件**：`customhttp`（zoka mining-patch REST 形状：/chain/height、/mining/template、/mining/submit(template_id+nonce)、钱包 REST）+ `cnrpc`（门罗 daemon /json_rpc：get_info/get_block_template(两 blob)/submit_block/块头查询；reserve_size=1 + nicehash 分片）+ `cnwallet`（wallet-rpc get_balance/transfer 单笔多 destinations/get_transfer_by_txid）。金额原子↔十进制纯整数换算（adapter/amount.go）。
- [x] **RandomX hasher FFI**（`internal/hasher/randomx`，build tag `randomx`）：cgo 链 **vendored 同源库** `third_party/randomx`（与 NTMminer 同一份源码，BSD，含 init_cache_salted 扩展）；**stock 配置（-DRANDOMX_STOCK）= rx/0**（⚠ vendored 默认是 dragonx 常量，链错金锚拦）；light 模式 + seed LRU（保留 2 epoch）+ 每 seed VM 锁；SelfTest = 官方 4 向量（与 NTMminer rx_kat.c STOCK 段同组）。KeyedHasher 注册表 + SelfTestAll 双注册表启动门禁。CI 增 randomx job（cmake 构建缓存 + `-tags randomx` 全量测试）；本地 Windows 默认不带 tag（中文路径 mingw cgo 铁律①）。**rx/dragonx 变体 = 第二份库 + zkrx_ 式符号前缀隔离，M3-6 迁移 dragonx 时加。**
- [x] **多币单实例共存 e2e**：TestMultiCoinCoexistence（btc 系 + CN 系同进程双管线双爆块）+ CN 全链路 e2e（假 REST 节点【节点端真验块】+ 真 CN 协议矿工：login→job→爆块→submit→确认→PPLNS→sendmany）+ CN 孤块路径。
- [x] **连接治理**：协议层自动 ban（badpow/malformed/dup 占比 ≥50%（≥10 样本）→ 指数退避 ban，stale/lowdiff 良性绝不计入防误 ban）+ PROXY protocol v1 解包（off/optional/required，真实 IP 二次过 ban 名单）+ 端口 MaxConns / 每 IP MaxConnsPerIP 双上限。
- [x] **zoka live 真机冒烟（2026-07-07，103.80 服务器，冒烟完即 pkill）**：CI artifact（ubuntu-22.04 cgo 二进制）→ /data/ntmpool-smoke，custom-http 对接**真 zoka 主网节点**（127.0.0.1:7100，height 2257+），NTMminer v1.11.1 **发行版**锄头 `-a zoka` 连池。三轮迭代定版：**180s found=20 accepted=20 rejected=0、badpow=0**（池端 rx/0 cgo 重算与真锄头逐 share 字节吻合）、vardiff 2000→8.2K 收敛、job 节流 ~14s/换。冒烟抓出并已修 3 个真 bug：①同高度模板 2s churn（zoka 模板每拉必变 timestamp）→ cnjob 15s 节流；②hashrate 用 2^32 口径把 1.2KH/s 显成 964GH/s → blob 系 multiplier=1；③vardiff 调档重推同 blob → 矿工重置 nonce 重找同解 = Duplicate share → 改 pendingDifficulty 语义（随下一个新模板生效）。未爆块（主网难度 vs 4T CPU，符合预期，与 BTX live 冒烟同口径）。
- [ ] M3 尾巴（后续会话）：**rx/dragonx 前缀库**（第二份 librandomx + zkrx_ 式符号隔离，dragonx 迁移时做）；**XMRig 官方客户端对拍**（走 cnrpc/monero 系路径，等有 offset-39 blob 的币/节点可测——zoka blob 布局 XMRig 本来就挖不了）；customhttp 补固定 reward 适配器选项（zoka 模板无 reward 字段，打款前要）。
- [ ] 可选：真 ZMQ 新块通知（替代轮询，从 M1 顺延）。

## M4 — 横向扩展 + 前端 API 定稿（主体 DONE，2026-07-07，本地+真 PG16+CI 三绿）

- [x] **会计/打款落 Postgres**：`PGLedger` 实现 Ledger 全接口（与 MemLedger 语义分毫不差：
  int64 聪计算、NUMERIC 字符串出入库、ConfirmBlock/OrphanBlock 单事务+行锁幂等、
  `block_credits` 分账快照做孤块回滚、`balance_changes` 审计流水、Reconcile 守恒 SQL 化）；
  `PGBatchStore`（payment_batches+payments 双表，Unfinished 崩溃恢复走真持久化）。
  share 缓冲批写（1s flush，爆块/confirm 前强制同步 flush）。有 `postgresDsn`→PG、无→内存。
- [x] **双实现 conformance 套件**（NTMPOOL_PG_DSN 门控）：9 组会计断言同时跑 Mem/PG，
  等价性铁证（M5 迁移前提）。顺手修 MemLedger 带费孤块守恒 bug（confirm(fee>0)→orphan
  计提费未作废→delta=-fee 误冻结）。
- [x] **意图先落库定稿**：cnjob「先交后记」改「意图先落库」（PoW hash 占位 submitting →
  SubmitBlob 拿权威块 id → BlockSink 补 hash 转 pending，闭合提交-落账崩溃窗口）；
  BlockSink 加 submit 闭包，bitcoin 侧顺带补上此前缺失的 MarkBlockPending。
- [x] **多实例共享 Postgres**：`InstanceRegistry` 心跳 upsert（text[] 币列表）+ 判活
  （make_interval）+ 无状态化验证（多实例集成测试：A 记 share、B confirm 分账正确）。
- [x] **API 聚合多实例**：公共 API `/api/instances` 列在线实例（前端「大全网站」数据底座）。
- [x] **Prometheus /metrics**：零依赖手写文本格式，shares_total{coin,outcome}（badpow 与
  lowdiff 分开，zoka 冒烟教训）+ blocks_submitted + payouts。
- [x] **CI**：postgres:16 service 容器跑全部 PG 集成测试；go 1.25 对齐 pgx。
- [ ] 尾巴（可选）：API 隐私脱敏本已在 M1 定稿；Grafana 看板模板（有 /metrics 后随时可做）；
  PPLNS share 窗口内存池通病（重启窗口清空首块分账残缺）已有 shares 表可从库重建，接一下即可。

## M5 — 生产迁移

- 现有 live 币逐个迁移：先影子运行（新旧池并行、只对账不打款）→ 切流
- 迁移顺序建议：新币直接上 NTMPool → zoka/dragonx 等成熟币最后迁
- 压测：share flood、连接风暴、断线重连风暴

### ✅ M5-DragonX 全线收官（2026-07-10）— NTMPool 首次真实生产验证

代码：`22af0df`（rx/dragonx 双段哈希器 + drgrx_ 前缀第二份 libRandomX + 三层金锚）→
`84157df`（作业管线/dragonxrpc 适配器/family 拼装/e2e）→ `9af48b4`（stale 修复）。**唯一设计事实源 = docs/06。**
- **新增家族 `dragonx-rpc`**：bitcoin-RPC 节点 × blob 作业管线 × CN 方言 × rx/dragonx 双段 PoW ×
  z_* 隐私钱包（异步 opid 内吞成同步 txid + WalletMaintainer shield 回补金库）。
- **10 确认垫付打款**（用户拍板，金库垫付、coinbase 100 确认成熟后自动 shield 回补），Confirmations 配 10。
- **真实生产矿工验证（103.80，退役 miningcore→切 NTMPool，中转机 iptables DNAT 引流 11 真实矿工，
  验完回滚）**：**badpow=0**（池端 rx/dragonx 重算与官方 drg-xmrig 逐字节一致）+ 真爆块 3131180 上主链 +
  **同高度双爆块 1confirmed/1orphaned 不双份入账** + 10 确认 z_sendmany 真打款给矿工 zs（txid 2b4aea…）。
- **实战抓修的坑**：stale 洪峰（curtime 抖动导致每 15s 无谓换 job，49%→0%，见 pitfall
  `blob链-job按JobKey去重-别让无关字节换工.md`）。
- 已知小 bug（M5.x）：`blocks_submitted_total` metric 埋点遗漏（爆块未计数，PG/打款正常，不影响功能）；
  z_sendmany opid 崩溃恢复（>45 收款人 fail-fast，分页待做）；opid 落库闭合 crash 窗口。
- **剩**：正式切流决策由用户定（这次验完已回滚旧机）；btc09/midstate 迁移（各自补 hasher）。

## 二期候选（不排期）

PPS 结算、Stratum V2、合并挖矿、PoS 质押池模块、自动兑换、前端大全网站（M4 API 已定稿，可启动）
