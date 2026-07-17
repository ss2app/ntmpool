先给结论：我建议采用“共享代码、按币隔离运行；热路径模块化单体、资金链路服务化”的混合架构。

最重要的不是把十几个币塞进一个程序，而是把下面三条链彻底分开：

1. `Work Plane`：接矿工、发 Job、验 Share、提交区块，追求低延迟。
2. `Accounting Plane`：保存 Share、计算 PPLNS/PPS、生成不可变账务，追求可重放、可解释。
3. `Money Plane`：构造、签名、广播、确认打款，追求幂等、隔离、可对账。

一条硬规则：矿工收到“Share accepted”之前，这个 Share 必须已经进入可靠持久化介质；但发现区块时，提交区块不能等待数据库或消息队列。

---

## 1. 整体架构与进程模型

### 1.1 推荐的文字版架构图

> 矿工 / NiceHash / Proxy / ASIC  
> ↓  
> L4 接入层：TCP、TLS、限流、PROXY Protocol、连接保护  
> ↓  
> 每币 `Pool Worker` 集群  
> ├─ Stratum 协议适配  
> ├─ Connection / Channel / Worker 会话管理  
> ├─ Job 缓存与 Vardiff  
> ├─ Share 语法检查、去重、PoW 验证  
> ├─ Block Candidate 快速提交  
> └─ Share 可靠日志写入  
> ↓　　　　　　　　　　　　　↘  
> Share Journal　　　　　　　节点集群  
> ↓　　　　　　　　　　　　　├─ Template 节点  
> Share Aggregator　　　　　　├─ 独立观察节点  
> ↓　　　　　　　　　　　　　└─ 多节点 Block Submit  
> Block Observer / Reorg Tracker  
> ↓  
> Reward Engine：PPLNS、PPS、SOLO、费用与舍入  
> ↓  
> Immutable Double-entry Ledger  
> ↓  
> Payout Planner  
> ↓  
> 隔离的 Signer  
> ↓  
> Broadcaster / Reconciler  
> ↓  
> 链上确认  
>
> 旁路：  
> Control Plane：币配置、版本、发布、密钥引用、节点健康、审计  
> Read Plane：API、统计、报表、Dashboard，不参与结算真值

### 1.2 每层的职责边界

#### A. L4 接入层

只处理：

- TCP/TLS。
- 连接数、包大小、行长度、空闲连接限制。
- 可信上游的 PROXY Protocol。
- 基于端口或明确路由将连接送到对应 `pool_id`。
- 粗粒度 DDoS 防护。

不要让它解析复杂的币种 Job，也不要把它做成所有币共享的“智能 Stratum 中转核心”。否则它会变成全池单点和协议兼容黑洞。

#### B. Pool Worker：每币热路径

一个生产 `Pool Worker` 只加载一个 `work domain`。通常就是一个币网络，但 AuxPoW、Merged Mining 是例外。

它应在同一进程内完成：

- Stratum 解码。
- 会话状态。
- Job 查找。
- Share 去重。
- PoW 验证。
- Block Candidate 重构。
- 持久化 Share 事件。

不要把“收到一个 Share → 远程 RPC 调验证服务 → 远程 RPC 查 Job → 再远程写 Share”拆成一串微服务。每个 Share 多几个网络跳转，很快会变成延迟和故障放大器。

#### C. Coin Coordinator

每币一个逻辑协调器，负责：

- 监听新块。
- 获取和验证 Template。
- 生成基础 Job。
- 发布 Job 序列。
- 管理 Job Epoch。
- 分配不重复的 `extranonce prefix` 或实例号。
- 协调算法、Hard Fork 激活。

初期可以嵌入 Pool Worker；规模扩大后再独立，但接口要从第一天存在。

#### D. 节点层

每币至少准备两个独立节点，但不要简单做 RPC Round-robin。

推荐：

- 一个健康主节点生成 Template。
- 其他节点核对 Tip、Height、Chain Work 和网络身份。
- 新块通知优先使用节点原生推送能力，轮询作为兜底。
- Block Candidate 同时或快速并行提交到多个健康节点。
- 区块最终状态由独立观察器确认，不以某次 `submitblock` 返回值作为最终事实。

Bitcoin 的 `getblocktemplate` 基础来自 [BIP 22](https://github.com/bitcoin/bips/blob/master/bip-0022.mediawiki)，但大量分叉币只做了部分兼容，甚至同名 RPC 的字段、错误码和提交返回值都不同。

#### E. Accounting Plane

从 Share Journal 消费，负责：

- 给 Share 排定确定性顺序。
- 生成按矿工、时间、Round、Work 的聚合。
- 计算 PPLNS/PPS。
- 生成账务事务。
- 处理重复事件，但绝不重复入账。

它不接私钥，也不调用 Stratum。

#### F. Money Plane

至少拆为：

- `Payout Planner`：决定付给谁、多少、手续费与批次。
- `Signer`：只签符合策略的交易，不直接访问公网。
- `Broadcaster`：广播并记录所有尝试。
- `Reconciler`：判断交易到底有没有进入 Mempool、是否确认、是否被替换或冲突。

私钥不能出现在 Pool Worker、API 或 Share 服务中。

---

### 1.3 三种进程模型比较

| 模型 | 优点 | 致命问题 | 结论 |
|---|---|---|---|
| 单进程多币 | 部署简单、共享缓存、开发快 | 一个币的节点卡死、RandomX OOM、Native Hash 崩溃或升级，会影响所有币；无法按币独立扩容和发布 | 只适合开发、测试、小型私池 |
| 每币一个进程 | 故障隔离、资源隔离、独立发布、容易定位问题 | 运维对象增多；若代码被复制成十几个分支，会迅速腐烂 | 推荐作为生产故障域 |
| 细粒度微服务 | 安全域和扩容边界清楚 | 分布式一致性、网络延迟、消息重复、排障成本很高 | 只拆安全、扩容和故障边界，不要按类或接口拆 |

### 1.4 我的具体推荐

- 一个代码库、一个主要发行物。
- 每个 `pool_id` 或 `work domain` 启动独立 Pool Worker 进程组。
- 小币可以共用物理服务器，但不要共用进程。
- RandomX、Equihash 等内存或 Native 风险高的币必须单独进程。
- Settlement 可以多币共享部署，但内部按 `pool_id` 严格分区。
- Payout Executor、钱包和 Signer 至少按币或钱包隔离。
- 每币至少两个 Pool Worker 实例时，必须保证 Job ID 和 Extranonce 全局不碰撞。
- Payout Worker 使用带 Fencing Token 的单 Leader；不能只靠“Redis 锁过期”。

这与“每币复制一份项目”完全不同：运行时隔离，代码和领域模型仍然共享。

---

## 2. “多币”应该怎样建模

### 2.1 不要把 Coin 当成一条记录

至少拆成这些概念：

1. `Asset`

   经济资产，例如 BTC、XMR。Ticker 只能展示，不能做主键。

2. `Network`

   一条具体链网络，由 Genesis Hash、Network Magic、Chain ID、Mainnet/Testnet 等识别。

3. `Work Profile`

   某一高度范围内的 PoW 算法、参数、Target 规则、Header 编码、Fork 激活规则。

4. `Pool Product`

   对外提供的池产品：端口、结算方式、费率、Vardiff、Stale 政策、最低打款额。

5. `Protocol Endpoint`

   Bitcoin SV1、CryptoNote Stratum、Equihash 方言、未来 SV2 等。

6. `Reward Stream`

   一个 Share 最终可能产生哪些资产。普通池通常一对一，但 Merged Mining 可以一个 Work Stream 产生多个币的奖励。

7. `Payout Rail`

   UTXO、Account/Nonce、CryptoNote Wallet RPC、Shielded Transfer、Custom RPC 等支付方式。

因此不能写死：

> 一个端口 = 一个币 = 一个资产 = 一个钱包 = 一个结算账户

这在普通小池看似成立，一旦加入 Merged Mining、SOLO/PPLNS 双端口、自动换币或同币多网络，就会返工。

### 2.2 外部身份也要拆开

不要让 TCP Connection 等于矿工。

正确关系是：

> Transport Connection  
> → Mining Session  
> → Mining Channel  
> → Authorized Identity  
> → Worker Label  
> → Payout Destination

原因：

- SV1 一个连接可能授权多个 Worker。
- XMRig Proxy、矿场 Proxy 会聚合大量下游矿机。
- SV2 一个 Connection 可以包含多个 Channel。
- 矿工账户和打款地址未必永远是一回事。

SV2 官方规范明确把 Connection、Channel、Proxy、Pool Service、Job Declarator 等角色分开，因此不要把 SV1 的“一个 Socket 一个 Worker”写进核心模型。[SV2 Protocol Overview](https://stratumprotocol.org/specification/03-protocol-overview/)、[SV2 Mining Protocol](https://stratumprotocol.org/specification/05-mining-protocol/)

即使第一版采用“钱包地址作为用户名”，内部也应生成独立 `miner_account_id`，并把地址保存成不可变的 `payout_destination_version`。

### 2.3 必须抽象成接口的差异

| 接口边界 | 必须负责的内容 |
|---|---|
| `ConsensusNodeAdapter` | 网络身份、同步状态、Tip、Template、提交区块、查询区块、节点能力探测 |
| `TemplateInterpreter` | 解析节点 Template，验证强制输出、交易、奖励字段和 Fork 参数 |
| `JobBuilder` | 构造 Job、Coinbase、Merkle Root、Reserved Offset、Header 或 Blob |
| `ShareReconstructor` | 根据 Job 和矿工提交重构实际 Header、Blob、Solution |
| `PowVerifier` | 算法、Seed、Personalization、Hash/Solution 验证 |
| `TargetMath` | Diff1、Target、Difficulty、Work 的精确转换和字节序 |
| `StratumFrontend` | Wire Protocol、方法名、响应形式、扩展协商、错误码 |
| `AddressCodec` | 地址类型、网络、Checksum、Integrated Address、Payment ID、Memo |
| `RewardInterpreter` | 实际归池奖励、手续费、Treasury、Masternode、Founder/Dev 输出 |
| `MaturityPolicy` | Canonical、Confirmed、Coinbase Mature、Wallet Spendable、Reorg Buffer |
| `WalletAdapter` | 构造、签名、广播、查交易、手续费、UTXO 或 Nonce 管理 |
| `ConsensusSchedule` | 按 Height、Timestamp、Version 等选择具体算法和规则版本 |

节点与钱包接口必须分开。很多链的挖矿节点 RPC 和钱包 RPC 根本不是同一个安全域或鉴权方式。

Miningcore 的 Monero 文档曾要求在不支持 Wallet RPC Digest Auth 时关闭 RPC 登录，再通过反向代理补认证。这正说明“RPC 都是 JSON-RPC”不等于安全能力相同。[Miningcore README](https://github.com/oliverw/miningcore)

### 2.4 新增一个币的理想改动面

已有币族和算法已支持时，新增币应只需要：

1. 新增不可变的 `Coin Manifest`：

   - Genesis Hash、Network Magic。
   - 地址规则。
   - Atomic Unit。
   - Template/Protocol/PoW Capability 选择。
   - Fork 激活计划。
   - Maturity 和 Reorg 策略。
   - 钱包能力要求。

2. 部署层配置：

   - 节点地址与 Secret 引用。
   - 钱包和 Signer。
   - Stratum 端口。
   - 费率、PPLNS 参数、最低付款额。
   - 告警阈值。

3. 测试资产：

   - 已知 Template → Job。
   - 已知 Share → Accept/Reject/Block Candidate。
   - 地址正反例。
   - 区块 Reward 和 Maturity。
   - Wallet 构造和查询交易。
   - 真实版本的 xmrig、SRBMiner、T-Rex、lolMiner、NiceHash 测试矩阵。

4. 自动创建数据分区和监控项。

理想情况下不应：

- 新建一套数据库表。
- 修改余额和付款核心。
- 在主流程加入 `if coin == ...`。
- 复制一个旧币目录后全局替换名称。

如果节点 RPC、Header、Reward 或 Wallet 确实魔改，则增加一个小而有类型的 Adapter，不应修改整个核心。

### 2.5 千万不要过度抽象的地方

#### 不要做“万能 BlockTemplate”

Bitcoin、CryptoNote、Equihash、PIVX 魔改币之间没有一个干净的万能 Template。最后通常会变成几十个 Nullable 字段和大量运行时分支。

应当是：

- 跨币共享少量语义，例如 Height、Previous Hash、Network Target。
- 币族内部使用强类型 Family Model。
- 原始 RPC 响应保留 Hash 或归档引用，便于追查。

#### 不要做“万能 sendMany”

UTXO、Account/Nonce、Monero Transaction Set、Shielded 钱包在重试、手续费、找零、隐私和交易查询上完全不同。

统一的是付款状态机，不是交易构造细节。

#### 不要把 Maturity 抽象成一个整数

`coinbase_maturity = 100` 不够。

至少要区分：

- 区块被某节点接受。
- 区块进入 Canonical Chain。
- 达到确认数。
- Coinbase Consensus Mature。
- 钱包实际 Spendable。
- 额外 Reorg Safety 满足。
- 运营策略允许付款。

#### 不要用通用 `Map<String, Any>` 包装 RPC

RPC 差异应该由 Adapter 消化。无类型的动态字段会把节点兼容问题拖到结算和付款阶段才爆炸。

#### 不要把所有魔改都做成配置项

字段偏移、Reward 输出、Fork 算法属于 Consensus-critical 逻辑，宁可写一个明确 Adapter 并配测试，也不要设计一门 JSONPath 配置语言。

原则是：抽象稳定动作，不强行统一所有数据结构。

---

## 3. 技术选型

### 3.1 实现语言

我的首选是：

- Go：核心服务、Stratum、节点适配、Settlement、Payout Orchestrator。
- Rust/C/C++：已有 PoW Native Library 或特别高性能的算法验证模块。
- TypeScript：只用于管理后台和非权威 API。

选择 Go 的理由：

- 网络和并发模型适合大量长连接。
- Goroutine 和 Channel 适合按连接、Channel、Pool 建立状态所有权。
- 单二进制部署，对十几个币的运维友好。
- RPC、PostgreSQL、Kafka、Metrics 生态成熟。
- 相比 C/C++，资金逻辑和并发代码更容易审计。
- Share 验证耗时通常主要在 PoW Kernel，不在语言本身。

如果团队已经有很强的 Rust 生产经验，全 Rust 的 Data Plane 会更好；但为了“技术先进”临时组建 Rust 团队，结果往往是加币速度和故障处理能力下降。这个选择高度依赖团队，我将其标为判断性建议。

不建议：

- 用 Node.js/PHP 承担权威账务和 Native PoW 热路径。
- 用 C/C++ 写余额、打款和状态机。
- 在金额或 Difficulty 中使用 JavaScript `Number`、Go `float64` 或 SQL 浮点数。

NOMP 证明 Node.js 异步 I/O 可以承载连接和多池，但它采用 Cluster、Redis Share/Payment 存储和 All-in-one 形态，官方 README 还明确警告配置与 Redis 数据结构可能变化并破坏生产部署。[NOMP README](https://github.com/zone117x/node-open-mining-portal)

### 3.2 Native PoW 模块

建议支持两种运行方式：

- 成熟稳定、调用频率高的算法：进程内薄 FFI。
- 来历不明、容易崩溃或内存巨大的算法：独立 `pow-worker` 进程，通过 Unix Domain Socket 批量调用。

不要使用运行时动态加载的任意插件去污染所有币。更稳妥的是编译进版本化发行物，或隔离成独立进程。

### 3.3 数据存储

推荐组合：

- PostgreSQL：账本、区块、Reward、Payment、Audit 的唯一真值。
- Kafka 或兼容的可靠日志：Accepted Share 的有序、可重放 Journal。
- Redis：只做可丢失缓存、短期去重、限流、Session 辅助。
- ClickHouse：Share/Worker 统计和长期分析，可选。
- Object Storage：压缩归档原始 Share、Job、Winning Template、审计快照。

不要把余额、未付款金额或唯一一份 Share 数据放在 Redis。

物理上最好至少拆成：

1. 高频 Share Store。
2. 低频但高价值的 Financial Ledger PostgreSQL。
3. Analytics/Read Model。

这样 Share 洪峰或分区维护不会拖死付款账本。

### 3.4 进程内消息机制

推荐：

- 强类型、有限容量的 Channel。
- Connection/Channel/Pool Coordinator 各自拥有状态。
- CPU 密集验证使用有界 Worker Pool。
- 不要每个 Share 启动一个无界 Goroutine。
- 不要使用全局 EventEmitter 式 Fire-and-forget。
- 新块和 Block Candidate 使用最高优先级通道。
- Share 持久化使用正常优先级。
- Stats、日志、Dashboard 事件可以采样或丢弃。

必须明确 Backpressure：

- Block Candidate 不能丢。
- Accepted Share 不能丢。
- 队列满时不能继续向矿工返回 Accepted。
- Stats 可以丢。
- 无效 Share 日志可以采样。

跨进程事件采用 At-least-once，再由消费者通过 `event_id` 和业务唯一键幂等。不要相信“Exactly-once”宣传；链上广播本身就无法和数据库形成原子事务。

---

## 4. 数据库核心设计

### 4.1 基础元数据

应有：

- `assets`
- `networks`
- `work_profiles`
- `consensus_schedule_versions`
- `pool_instances`
- `pool_config_versions`
- `protocol_endpoints`
- `node_capability_snapshots`
- `payout_destination_versions`

所有历史表引用具体版本，不能在结算时读取“当前币配置”重新解释旧 Share。

金额统一保存最小原子单位：

- 推荐 `NUMERIC(78,0)`，或经过严格上限证明的整数类型。
- `decimals` 只负责展示。
- 所有金额行都带 `asset_id`。
- 数据库约束禁止跨资产平账。
- 地址保留原始文本和规范化二进制值，不能通用地转小写。

### 4.2 Job 表

`mining_jobs` 至少包含：

- `job_id`
- `pool_id`
- `network_id`
- `job_epoch`
- `height`
- `previous_hash`
- `network_target`
- `work_profile_version`
- `config_version`
- `template_hash`
- `template_source_node`
- `created_at`
- `expires_at`
- `raw_template_pointer`
- `job_origin`：Pool、Proxy、未来 SV2 Declared Job

普通 Job 可短期保存；产出 Block Candidate 的 Job 和 Template 应永久保存。

Job ID 不能是重启后从 1 开始的小整数。建议包含 Pool、Epoch、实例和序列信息，至少要保证在所有活跃和过期 Job 生命周期内不重用。

### 4.3 Share 表

权威表只保存 Accepted 或具有结算意义的 Share：

`share_events`

关键字段：

- `event_id`
- `pool_id`
- `share_seq`
- `network_id`
- `job_id`
- `session_id`
- `channel_id`
- `miner_id`
- `worker_id`
- `gateway_instance_id`
- `received_at`
- `assigned_target`
- `credited_work`
- `network_target`
- `pow_hash`
- `submission_fingerprint`
- `work_profile_version`
- `credit_policy_version`
- `is_stale`
- `is_block_candidate`
- `validation_result`
- `credit_result`

特别注意：

- `validation_result` 和 `credit_result` 必须分开。
- Assigned Difficulty 必须取自那个 Job，而不是 Session 当前最新 Difficulty。
- 结算应使用精确 `credited_work`，而不是展示用浮点 Difficulty。
- 每个 Pool 需要确定性的 `share_seq` 或等价顺序。
- PPLNS 截止点使用 Candidate 对应的 Share 序列，不用模糊的数据库时间。

去重键由规范化提交计算，例如 Job、Extranonce、Nonce、NTime、Version、Solution 等。矿工重试或服务重启不能造成双重计分。

无效 Share 不要进入权威 Share 表：

- 以 Counter、时间桶聚合。
- 原始样本短期保存。
- 可按错误类型和矿工采样。
- Block Candidate 级别的异常永不采样丢弃。

### 4.4 Round 和 PPLNS

保留 `mining_rounds` 供报表使用，但不要让 PPLNS 依赖传统 Round。

PPLNS 的窗口可能：

- 跨越多个区块 Round。
- 在连续出块时重叠。
- 因 Vardiff 含有不同 Share Difficulty。

所以 `N` 应定义为累计 Work，而不是：

- 最近多少个 Share。
- 最近多少分钟。
- 当前 Round 的 Share 数。

否则高低 Difficulty 矿工会被不公平计权，甚至可被利用。

建议保存：

`reward_calculations`

- Scheme 和版本。
- Candidate Block。
- Window Start/End Share Sequence。
- 总 Work。
- 实际可分奖励。
- Pool Fee。
- 舍入策略。
- 输入快照 Hash。
- 计算器版本。

`reward_allocations`

- Calculation ID。
- Miner。
- Work。
- Gross、Fee、Net Atomic Amount。
- Destination Policy。
- 唯一业务键。

边界 Share 是否整体纳入、是否按比例截断，也必须成为版本化政策。

### 4.5 区块表

至少拆为：

- `block_candidates`
- `block_submit_attempts`
- `block_observations`
- `block_state_transitions`

状态不要只有 `pending/confirmed/orphan` 三种，应覆盖：

- Candidate。
- Submitted。
- Accepted by Node。
- Seen in Canonical Chain。
- Confirming。
- Consensus Mature。
- Wallet Spendable。
- Payout Eligible。
- Orphaned。
- Reorged。
- Invalid。
- Unknown/Ambiguous。

保存每个节点的原始提交响应。RPC 返回成功不等于已进入 Canonical Chain，超时也不等于提交失败。

唯一约束至少包括：

- `(network_id, block_hash)`
- Candidate Share 的业务唯一键。

### 4.6 账本和余额

余额不能是唯一真值的可修改数字。

采用 Double-entry Ledger：

- `ledger_accounts`
- `ledger_transactions`
- `ledger_entries`
- `balance_projections`

账户类型可以包括：

- Immature Reward。
- Mature Reward Clearing。
- Miner Payable。
- Pool Fee Revenue。
- PPS Reserve。
- Payout Reserved。
- Payout In-flight。
- Network Fee Expense。
- Hot Wallet Asset。
- Orphan/Reorg Reserve。

要求：

- Ledger Entry 不更新、不删除。
- 更正只能用反向或补偿事务。
- 每个 Ledger Transaction 在单一资产内平衡。
- `business_key` 唯一，防止同一 Block 或 Credit Batch 重复入账。
- `balance_projections` 只是缓存，必须能由 Entry 重建。

PPS 不建议每个 Share 写一笔 Ledger Transaction。可以按矿工、Pool、短时间窗口、Share 序列范围生成确定性 Credit Batch，但必须能还原所覆盖的 Share 和计算参数。

### 4.7 Payout 表

建议至少有：

- `payout_batches`
- `payout_items`
- `payout_reservations`
- `transaction_intents`
- `signed_transactions`
- `broadcast_attempts`
- `chain_transactions`
- `transaction_confirmations`
- `payout_reconciliations`
- `replacement_transactions`

状态示例：

> Draft → Approved → Reserved → Built → Signed → Broadcast Unknown → In Mempool → Confirmed

以及：

> Rejected / Terminal Failed / Replaced / Conflicted / Manual Review

保存：

- Destination Version。
- Atomic Amount。
- 手续费分配。
- UTXO Input Set 或 Account Nonce。
- Unsigned Payload Hash。
- Signed Transaction Hash。
- TxID。
- 每次广播结果。
- Replacement 关系。
- 对应 Ledger Reservation。

Monero 一批付款可能产生多个交易，因此不要把 `payout_batch` 和 `txid` 写成强制一对一。

### 4.8 审计表

`audit_events` 保存：

- Actor：人、服务、自动任务。
- Action。
- Request ID。
- 配置前后版本。
- 付款审批。
- 手工调整原因。
- 时间和来源。
- 相关业务 ID。
- 前一条 Audit Hash，可选。

仅做数据库 Hash Chain 不是真正防篡改；需要配合只追加权限、异地备份和 WORM 归档。

### 4.9 Share 写入量怎样控制

按优先级：

1. 用 Vardiff 控制正常矿工 Share 频率。
2. 昂贵 PoW 前先做长度、Job、Nonce、重复、速率检查。
3. Accepted Share 先进入 Kafka 可靠日志，批量落 PostgreSQL。
4. 按 `pool_id + time` 分区，自动创建分区。
5. 删除旧数据时 Drop Partition，不做逐行 Delete。
6. 热表只保留必要索引。
7. 时间查询可使用 BRIN；矿工查询走聚合表。
8. Stats 进入 ClickHouse 或时间桶，不扫描原始 Share。
9. 大型 Solution、原始 Job 放 Object Storage，只在数据库留 Hash 和引用。

Miningcore 的 Multipool 高负载建议把 Share 按 Pool 做 PostgreSQL List Partition，而且新增 Pool 还需要显式增加分区。这是很典型的教训：Share 表的物理布局必须从第一天按 Pool 分区，并且分区创建要自动化。[Miningcore README](https://github.com/oliverw/miningcore)

### 4.10 数据保留策略

逻辑上永久保存：

- Block Candidate、Winning Job、Template Hash。
- Block 状态历史。
- Reward Calculation 和输入快照。
- Reward Allocation。
- Ledger 全部记录。
- Payout、TxID、广播和确认历史。
- 配置版本。
- 手工操作和审计。
- 归档文件 Hash。
- 结算聚合数据。

可以裁剪：

- Invalid Share 原始数据。
- 普通 Connection/IP 日志。
- 非获块 Job。
- 普通 Accepted Share 热数据。
- 高频 Hashrate Tick。
- 完整 RPC Debug 日志。

Accepted Share 只有在满足以下条件后才能从热库裁剪：

> 最大 PPLNS 窗口已关闭  
> + 相关区块成熟和 Reorg 观察完成  
> + Reward 已确定  
> + Payout 已对账  
> + 申诉保留期结束  
> + 原始数据已压缩归档并校验

常见的在线保留可以是 30～90 天，但这只是容量规划示例，不是通用规则；具体取决于最大 PPLNS 窗口、出块频率和运营政策。

---

## 5. 从一个 Share 到钱包收款的完整数据流

### 第 0 步：生成 Job

Coin Coordinator 从健康节点取得 Template。

校验：

- Genesis/Network 身份正确。
- 节点已同步，不在 IBD。
- Height、Prev Hash、Target 与观察节点一致。
- Template 时间合法。
- Coinbase Value 合理。
- SegWit Commitment、Treasury、Masternode、Founder/Dev、Superblock 等强制输出正确。
- 对应 Height 使用正确 PoW 和参数。
- Template 来源节点版本已获支持。

生成 Job 时固定：

- Network Target。
- Work Profile Version。
- Config Version。
- Reward 规则版本。
- Job Epoch。

### 第 1 步：矿工连接和协商

Stratum Frontend 完成：

- 限制输入长度和方法速率。
- Subscribe/Login。
- Authorize。
- 解析钱包、账户、Worker、Password 参数。
- 算法和扩展协商。
- 分配 Channel、Extranonce/Search Space。
- 设置 Initial Difficulty/Vardiff。

地址校验不能只看前缀，要检查网络、Checksum、地址类型、Integrated Address、Payment ID 或 Memo。

XMRig 官方扩展包括算法协商、NiceHash、KeepAlive、Rig ID、Self-select 等，说明 CryptoNote Stratum 不是一份固定不变的协议。[XMRig Protocol Extensions](https://xmrig.com/docs/extensions)、[Algorithm Negotiation](https://xmrig.com/docs/extensions/algorithm-negotiation)

### 第 2 步：下发 Job

为 Session/Channel 绑定：

- `job_id`
- Assigned Target/Difficulty
- Extranonce 范围
- 算法版本
- 允许的 NTime/Version Rolling
- Clean Job/Stale 规则

Vardiff 改变后，旧 Job 仍使用旧 Job 对应的 Assigned Target。不能用 Session 当前 Difficulty 重新判断旧 Share。

### 第 3 步：收到 Share，先做廉价检查

依次检查：

- JSON/Binary Frame 合法。
- 方法和字段数量正确。
- Job 属于该 Pool 和 Session/Channel。
- Job 未未知或超出允许的 Stale 生命周期。
- Nonce、NTime、Version、Extranonce、Solution 长度和范围合法。
- 算法与 Job 匹配。
- Submission Fingerprint 未出现。
- 用户未超速或触发恶意低难度攻击规则。

这些检查必须在昂贵的 RandomX 或 Equihash 验证之前。

### 第 4 步：重构和验证 PoW

根据原始 Job 精确重构：

- Coinbase。
- Merkle Root。
- Block Header。
- CryptoNote Hashing Blob。
- Equihash Solution 输入。

然后：

1. 使用 Job 固定的 PoW Profile 计算 Hash/Solution。
2. 与 Assigned Share Target 比较。
3. 再与 Network Target 比较。
4. 得到 Accepted、Low Difficulty、Invalid 或 Block Candidate。

这里最常见的灾难性错误是：

- 大小端。
- Diff1 常量。
- Compact Target。
- Header 字段顺序。
- RandomX Seed。
- Equihash Personalization。
- Fork 高度边界。
- Version Rolling Mask。

### 第 5 步：Block Candidate 走快速通道

一旦达到 Network Target：

- 不等待 Share 批量落库。
- 立即重构完整区块。
- 本地再做一次结构校验。
- 并行提交到多个健康节点。
- 必要时通过本地 P2P 或专门 Block Submit 通道传播。
- 记录每个节点的响应和耗时。
- 同时写入不可丢失的 Candidate Event。

“某节点返回超时”必须进入 `Unknown`，不能立即当作失败重提另一个不同区块。

### 第 6 步：可靠持久化并回复矿工

对于普通 Accepted Share：

- 生成全局唯一 `event_id`。
- 写入复制后的可靠 Share Journal。
- Broker 确认达到持久化策略。
- 然后向矿工返回 Accepted。

如果 Journal 不可用：

- 短期可进入有容量上限的本地 Durable Spool。
- 到达安全上限后停止接受新 Share。
- 绝不能内存排队后继续返回 Accepted。

Candidate 提交和 Journal 写入可以并行，但区块传播优先。

### 第 7 步：Share 聚合

消费者：

- 按 `event_id` 去重。
- 给 Pool 内 Share 形成确定性顺序。
- 写入 `share_events`。
- 更新矿工和 Worker 时间桶。
- 累计 `credited_work`。
- 维护 PPLNS 窗口索引。

Stats 和财务聚合必须分开。Hashrate 显示允许近似，Reward 计算不允许。

### 第 8 步：观察区块生命周期

Block Observer 使用独立节点持续确认：

- Block Hash 是否出现在 Canonical Chain。
- Height 和 Parent 是否正确。
- 实际 Coinbase/Reward。
- Pool 真正拥有的输出。
- Confirmations。
- Coinbase 是否 Consensus Mature。
- 钱包是否实际 Spendable。
- 是否发生 Reorg。

不能只看 Wallet RPC 的一个 `confirmations` 字段，也不能只信提交节点。

### 第 9 步：计算 Reward

#### PPLNS

- 以 Candidate Share 为窗口终点。
- 向前累计准确 Work，直到达到政策定义的 N。
- 应用 Stale、Fee、Donation、Reward Recipient 规则。
- 使用实际归池 Reward。
- 按确定性舍入规则分配。
- 保存输入边界、总 Work、每矿工 Work 和 Calculation Hash。

PPLNS 不应在发现块后删除“当前 Round 全部 Share”；连续出块时窗口可能重叠。

#### PPS

在 Share Accepted 时，使用该 Job 当时的：

- Network Expected Work。
- 合格 Block Reward 定义。
- PPS Fee。
- Share Credited Work。

生成 PPS Credit。不能在几小时后使用当前 Network Difficulty 重新计算。

纯 PPS、PPS+、FPPS 对交易费和 Uncle/Auxiliary Reward 的处理不同，必须是不同版本的 Scheme，不要都叫一个 `PPS`。

### 第 10 步：入不可变账本

在一个 PostgreSQL 事务内：

- 插入 Reward Calculation。
- 插入 Reward Allocation。
- 生成 Ledger Transaction/Entries。
- 写 Outbox Event。

对于 PPLNS，可以先进入 Pending/Immature，再在成熟时转入 Miner Payable；孤块则做反向事务。

对于 PPS，运营方在 Share 接受时即形成负债，区块是否挖到与该笔矿工应收款解耦。

### 第 11 步：选择可付款余额

Payout Planner 检查：

- Available Balance ≥ Threshold。
- 没有 Frozen、Dispute、Negative Adjustment。
- Destination 版本有效。
- 金额大于 Dust 和预估手续费。
- 钱包余额、储备和负债对得上。
- 该 Miner 没有已 Reserved 的重复金额。

在同一数据库事务中：

- 创建 Payout Batch。
- 将 Miner Payable 转入 Payout Reserved。
- 生成唯一 Idempotency Key。
- 保存 Destination Snapshot。

### 第 12 步：构造交易

币种 Wallet Adapter 负责：

- UTXO 选择或 Account Nonce。
- 输出数量和交易大小限制。
- 找零。
- Fee Rate。
- Dust。
- Shielded/Transparent 约束。
- Payment ID、Memo。
- Monero 多交易拆分。

先保存 Transaction Intent 和 Unsigned Payload Hash，再进入签名。

### 第 13 步：隔离签名

Signer 只接受符合策略的 Intent：

- Network 正确。
- 总金额在批次限制内。
- Fee 不超上限。
- 输出与审批后的 Payout Items 一致。
- 不含额外输出。
- Nonce/UTXO 符合预留。
- 请求带有效 Fencing Token。

Signer 返回签名结果，不能直接把私钥交给 Wallet RPC 或 Pool Worker。

### 第 14 步：广播

广播前或广播同时，必须可靠保存：

- Signed Transaction。
- 可确定的 TxID。
- Batch 与输出映射。

如果 RPC 超时：

- 状态设为 `Broadcast Unknown`。
- 用 TxID、Input Set 或 Nonce 查询节点和 Mempool。
- 未查清前不能重新构造一笔新交易。

这是整个链路中最容易造成直接资金损失的一步。

### 第 15 步：确认与对账

Reconciler 持续检查：

- Tx 是否在 Mempool。
- 是否确认。
- 是否被替换。
- 是否冲突或被丢弃。
- 实际手续费。
- 链上输出是否和 Payout Item 一致。

达到政策确认数后：

- Payout In-flight 转为 Paid。
- 记录最终手续费。
- 更新 Read Model。
- 通知矿工。

失败时只有在证明原交易不会再确认，或已明确纳入 Replacement Chain 后，才可以释放或重建付款。

如果只问“哪一步最容易出错”：

1. 打款广播超时后的重试，最容易双付。
2. Job/Target/Header 构造错误，最容易损失整块。
3. PPLNS 窗口、单位和舍入错误，最容易形成长期系统性少付或多付。
4. Maturity/Reorg 判断错误，最容易用运营资金垫付孤块。

---

## 6. 多币池最容易返工的地方

下面按危险程度排序。

### 6.1 用 Ticker 当主键

后果：

- Mainnet/Testnet 混淆。
- 同名分叉币冲突。
- 钱包连错链。
- 历史数据在改名后失去身份。

必须使用不可变 `network_id`，并在启动时验证 Genesis Hash。

### 6.2 金额用浮点数

后果：

- 长期舍入漂移。
- 批量付款差一 Atomic Unit。
- 余额无法和链上对账。
- 跨不同 Decimals 的币互相污染。

金额必须是 Atomic Integer。Difficulty 和 Target 也不能偷懒使用同一种浮点类型。

### 6.3 PPLNS 按 Share 数量计算

Vardiff 下一个 Share 的 Work 不相等。按数量计算会直接改变矿工收益权重。

正确依据是精确 Work。

### 6.4 把所有 Bitcoin Fork 当成 Bitcoin

常见差异：

- SegWit Commitment。
- AuxPoW。
- Treasury/Masternode/Superblock。
- 特殊 Coinbase 输出顺序。
- Block Version。
- Time/Nonce 扩展。
- `submitblock` 返回格式。
- PoS/PoW 混合。
- Reward 不是简单的 Subsidy + Fee。

PIVX 系尤其不能只继承 Bitcoin Template 后改地址前缀。

### 6.5 把所有 Stratum V1 当成一个协议

Stratum V1 实际上是方言集合：

- Bitcoin `subscribe/authorize/notify/submit`。
- CryptoNote `login/job/submit`。
- Equihash 的 Header/Solution。
- NiceHash Extranonce 和最低 Difficulty。
- Version Rolling、AsicBoost。
- KeepAlive、Rig ID、Algo Negotiation。
- 不同矿工对 JSON ID、Error、字段类型的容忍差异。

Miningcore 社区曾公开报告普通 SHA256 能连，但 Braiins/AsicBoost 场景发生断连；这只是社区报告，不能视为官方根因，但足以说明“支持 SHA256”不等于“兼容 ASIC Stratum 扩展”。[相关讨论](https://github.com/oliverw/miningcore/discussions/1597)

### 6.6 把 Connection 当 Worker

Proxy、NiceHash 和未来 SV2 会打破这个假设。后续再改会波及：

- Session 表。
- Stats。
- Extranonce。
- Ban。
- Share 去重。
- API。
- 付款身份。

第一天就拆 Connection、Channel、Identity、Worker。

### 6.7 多实例 Extranonce 和 Job ID 碰撞

单实例运行多年没问题，一做双机或跨区域就出现：

- 重复 Coinbase。
- Share 被误判 Duplicate。
- 两个矿工搜索同一空间。
- 重启后旧 Share 对上新 Job。

必须使用实例租约、Epoch 和不重复 Prefix；Prefix 在所有关联 Job 过期前不能重用。

### 6.8 节点简单负载均衡

两个节点可能：

- Tip 不同。
- Mempool 不同。
- Fork 状态不同。
- 一个仍在同步。
- 一个运行旧 Consensus 版本。

不要从 A 获取 Template，却用 B 的状态解释它。主 Template 节点和观察节点职责要明确。

### 6.9 RPC 成功等于业务成功

典型错误：

- `submitblock` 返回空值就立即标记区块 Confirmed。
- Wallet RPC 返回 TxID 就立即减少余额并标记 Paid。
- RPC Timeout 立即重试创建新交易。

所有外部动作都要进入“意图 → 尝试 → 观察 → 最终确认”的状态机。

### 6.10 Redis 同时承担 Queue、Share、余额和付款

Redis 可以很快，但：

- Pub/Sub 无可靠重放。
- TTL/Eviction 和资金数据冲突。
- 数据结构升级困难。
- 审计和复杂约束弱。
- 故障恢复时容易出现“到底处理到哪里”。

NOMP 的官方设计把 Reward/Payment Share 放在 Redis，并明确提醒生产用户配置和 Redis 数据结构可能变化。它适合作为历史经验，而不是新资金系统的账本模板。[NOMP README](https://github.com/zone117x/node-open-mining-portal)

### 6.11 单进程加载所有 PoW

RandomX、Equihash 和各种 Forked Native Library 的资源特点完全不同：

- Native Crash 会杀掉整个进程。
- RandomX Dataset/VM 消耗大。
- Equihash 验证可能产生显著内存和 CPU 压力。
- 一个币被 Invalid Share 攻击会拖慢其他币。

Miningcore 自身对 RandomX VM 和 Equihash 并发有专门的资源警告，这正是按币或算法风险隔离的理由。[Miningcore README](https://github.com/oliverw/miningcore)

### 6.12 Pool ID、配置和数据库物理结构耦合

Miningcore 配置明确警告生产开始收 Share 后不要修改 Pool ID；其多池分区方案也按 Pool ID 建分区。[Miningcore Configuration](https://github.com/coinfoundry/miningcore/wiki/Configuration)

教训：

- `pool_id` 一旦投入生产就不可重用、不可改语义。
- Display Name 可以改。
- 停用后要创建新 Pool ID，不能把旧 ID 指向另一个币。
- 数据分区应自动创建，不靠人工执行 DDL。

### 6.13 把历史记录按当前配置解释

常见事故：

- Pool Fee 从 1% 改成 2%，重算旧块时用了 2%。
- Fork 后算法改变，旧 Share 被新算法解释。
- 修改 Maturity 后旧块状态跳变。
- 地址规则升级后历史地址无法展示。

因此每个 Job、Share、Reward、Payout 都要引用配置和算法版本。

### 6.14 把 Stats 当账务

Hashrate、Worker Online、近 10 分钟 Share 都是近似读模型。

它们可以丢、可以重算，不能拿来决定矿工余额。

### 6.15 Merged Mining 事后加入

如果数据库写死：

> 一个 Share 只能属于一个币  
> 一个 Block 只能产生一种资产  
> 一个 Pool 只能有一个 Reward Asset

后续加入 AuxPoW 会大改 Share、Block、Reward、Ledger。

即使第一版不实现 Merged Mining，也建议允许：

- 一个 Share 关联多个 Block Candidate。
- 一个 Reward Calculation 产生多个 Asset 的 Allocation。

### 6.16 把加币做成代码分叉

yiimp/YAAMP 类系统的优势是加算法、加币快，但长期 Fork 生态很容易积累大量币种 Flag、共享表字段和定时脚本依赖。关于 yiimp 的具体生产事故没有统一官方 Postmortem，因此这里标记为：**不确定，属于基于其架构形态的工程推断，不应当作已证实事故记录**。

更可靠的经验是：共享核心、Capability 组合、少量明确 Adapter；不要形成十几个长期漂移的币种分支。

### 6.17 只考虑 SV1 数据模型

SV2 将 Mining Protocol、Job Declaration、Template Distribution 分开，而且支持 Standard、Extended、Group Channel。未来接入 SV2 时，应增加独立 Frontend，复用 Job、Share、Target、Ledger 语义，不能把 SV2 硬翻译成内部的 SV1 JSON DTO。[SV2 官方协议概览](https://stratumprotocol.org/specification/03-protocol-overview/)

### 6.18 没有长期维护所有权

Oliver Weichhold 的原 Miningcore 仓库已于 2023 年归档。它仍是很有价值的参考，但这提醒自研池必须真正拥有核心协议、PoW、支付和节点适配能力，不能把“Fork 一个活跃项目”当成长期维护策略。[Miningcore 原仓库](https://github.com/oliverw/miningcore)

ckpool 则展示了另一个方向：针对 Bitcoin 把接入、Proxy、数据库、进程和 Unix Socket 明确拆开，甚至支持 Socket Handover。这是高性能专业化设计的好参考，但不应把其 Bitcoin 专用模型直接泛化成十几个币的万能核心。[ckpool README 镜像](https://github.com/ctubio/ckpool)

---

## 7. 第一天必须做对、后期很难改的决策

按重要性排列：

### 1. 金额和账本模型

- Atomic Integer。
- 每条金额带 Asset。
- Double-entry。
- Immutable Entry。
- 更正用补偿事务。
- Balance 只是 Projection。

这是最不能返工的部分。

### 2. Pool、Network、Asset 的不可变身份

- Ticker 不做 Key。
- Genesis Hash 验证。
- Pool ID 不改、不复用。
- Mainnet/Testnet 明确分开。
- 工作资产、奖励资产、付款资产不强制写死为同一个。

### 3. Share Accepted 的持久化语义

必须明确：

> 什么条件满足后，才能向矿工返回 Accepted？

推荐答案是：Share 已进入复制后的可靠 Journal。否则数据库故障时一定会发生“矿工看到 Accepted，但池没有账”。

### 4. PPLNS 的数学合同

第一天就写成正式规范：

- N 的单位。
- Window 终点。
- Difficulty 变化。
- Stale 是否计入。
- 连续出块窗口是否重叠。
- Reward 范围。
- Pool Fee。
- 舍入和余数。
- Orphan/Reorg。
- 配置变更如何生效。

不要只在代码中体现。

### 5. Payout 状态机和幂等键

必须接受一个事实：

> 数据库写入与链上广播永远无法形成一个原子事务。

因此第一天就需要 Intent、Signed、Broadcast Unknown、Observed、Confirmed、Replacement 等状态，不能从简单的 `paid = true/false` 开始。

### 6. Connection、Channel、Worker、Destination 的关系

这决定未来能不能接：

- Proxy。
- NiceHash。
- 矿场聚合。
- SV2。
- Account 模式。
- 多 Worker 授权。

### 7. Extranonce、Job ID 和多实例策略

在单实例阶段就设计：

- Instance Epoch。
- Prefix Lease。
- 重启不重用。
- Job 全局唯一。
- 多区域去重。
- Candidate 的确定性边界。

否则第一次做 HA 就会碰到共识级问题。

### 8. 历史版本化

Job、Share、Reward、Payout 至少引用：

- Config Version。
- Work Profile Version。
- Protocol/Adapter Version。
- Reward Scheme Version。
- Address Validation Version。

过去发生的业务不能依赖当前配置解释。

### 9. 节点、Pool 和钱包的安全域

- Stratum Worker 无私钥。
- API 无钱包写权限。
- Planner 无签名密钥。
- Signer 无任意公网访问。
- Broadcaster 无私钥。
- 节点 RPC 不暴露公网。
- 钱包与挖矿节点尽量分开。

后期再拆安全域，通常需要改所有 RPC 和部署结构。

### 10. 区块和交易采用状态历史，不是单字段

Block 和 Payout 都必须：

- 有显式状态机。
- 状态变化可审计。
- 保留原始外部响应。
- 能表达 Unknown/Ambiguous。
- 支持 Reorg 和 Replacement。

### 11. Adapter 边界和测试资产

每个币上线前必须拥有可重复的：

- Template。
- Share。
- Block Candidate。
- Fork Boundary。
- Address。
- Reward。
- Wallet。
- 真实 Miner 兼容测试。

小众链的官方文档通常不可靠，测试向量和真实 Daemon 行为才是最终合同。

### 12. 数据分区和保留策略

- Share 与 Ledger 物理隔离。
- Pool 和时间分区。
- 自动创建分区。
- Drop Partition 清理。
- Winning Job 永久保留。
- Financial Data 不随 Share 清理。
- 审计和异地备份从第一天启用。

---

最后可以把整个架构压缩成六条不可违反的 Invariant：

1. 没有可靠持久化，就不能回复 Share Accepted。
2. Block Candidate 不能经过普通异步结算队列后才提交节点。
3. 余额不能直接修改，只能由不可变 Ledger 推导。
4. RPC Timeout 不能直接解释成失败，更不能盲目重付。
5. 历史 Share 和 Reward 不能由当前配置重新解释。
6. 一个币的节点、PoW 或钱包故障，不能拖垮其他币。

只要这六条从第一天守住，后续增加币、协议、区域和 ASIC，主要是增加 Adapter 与部署容量；如果其中任何一条没守住，规模上来后基本都会演变成账务、双付或整池停机级返工。