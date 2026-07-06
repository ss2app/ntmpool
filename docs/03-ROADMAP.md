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

## M3 — 多币 + 多方言（下一期）

- CryptoNote 方言（XMRig 系 login/job/submit）——协议细节已调研齐，见 **docs/04 §2**（login params/algo 协商/seed_hash 64hex/worker 识别顺序 rigid>pass>+worker/keepalived）。验证客户端：XMRig 官方（标准参考实现）+ NTMminer rx 系。
- RandomX hasher FFI：Go cgo 链 stock librandomx（`-DARCH=default` 重编，C 链 C++ 要 `-lstdc++`——工厂坑见 `_knowledge/pitfalls/randomx-vendor-into-c-miner.md`）；金锚自检 = rx/0 官方 test vector + 与 NTMminer `rx_dragonx.c` 跨实现逐字节对拍。
- custom-http 适配器（zoka 先例：节点 REST /mining/template+submit；CRB miningcore 魔改先例）+ cryptonote-rpc 适配器（门罗系 daemon+wallet 分离）。
- 验证素材：**dragonx**（rx/dragonx，103.80 有 live 节点+miningcore 池可对拍口径）、**zoka**（rx/0 标准，live）、taron 冷归档 `coins/taron`（rx/tar miningcore CN family 配置参考；币已放弃只作代码参考）。
- 多币单实例共存（每币独立开关/独立日志/独立地址对）——架构已支持（CoinInstance 独立生命周期），补多币 e2e。
- 顺带：协议层自动 ban（invalidPercent 阈值+指数退避，banlist.Strikes 已备好）+ PROXY protocol 解包（藏转发器后拿真实 IP，R12）。

## M4 — 横向扩展 + 前端 API 定稿

- 多实例共享 Postgres：实例注册表 + 心跳 + stratum 实例无状态化验证
- API 网关聚合多实例（前端「大全网站」的数据底座）
- API 隐私脱敏定稿 + 速率限制
- Prometheus /metrics + Grafana 看板模板

## M5 — 生产迁移

- 现有 live 币逐个迁移：先影子运行（新旧池并行、只对账不打款）→ 切流
- 迁移顺序建议：新币直接上 NTMPool → zoka/dragonx 等成熟币最后迁
- 压测：share flood、连接风暴、断线重连风暴

## 二期候选（不排期）

PPS 结算、Stratum V2、合并挖矿、PoS 质押池模块、自动兑换、前端大全网站（等 M4 API 定稿后启动）
