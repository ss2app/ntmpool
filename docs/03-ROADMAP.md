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

M1 收尾（下个会话）：
- [ ] 真 bitcoind regtest 字节级验收（服务器起一次性容器）——唯一还没做的是「真节点接受我们组的块字节」；纯 Go e2e 用假节点验了逻辑，btcwork 自洽测试验了字节，但真 bitcoind 的 submitblock 接受是最终铁证
- [ ] miningcore 形状 API 填充（/blocks /payments /miners/{addr}，含地址脱敏）
- 验收币候选：任一标准比特币系 regtest（stock bitcoind 最标准）

## M2 — 热管理 + 管理后台 API

- 热参数：手续费/确认数/起付额/端口增删启停（监听器热启停）
- 管理后台 API（token 鉴权）：全部热操作 + ban 管理 + 对账 + 手续费转移（fee sweep 状态机）
- 双地址强制分离 + 手续费自动归集
- 矿工设置：`-p mp=21` 密码绑定 + miner_settings
- 通知：Telegram/webhook（爆块/打款/孤块/节点失联/对账不平）

## M3 — 多币 + 多方言

- CryptoNote 方言（XMRig 系登录/job/submit）+ cryptonote-rpc 适配器 → 用 taron 冷归档代码/dragonx 验证
- custom-http 适配器（zoka 先例）+ NTM 自有方言（自写链）
- RandomX hasher FFI（复用 dragonx/zoka 的 stock librandomx 经验，`-DARCH=default`）
- 多币单实例共存（每币独立开关/独立日志/独立地址对）

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
