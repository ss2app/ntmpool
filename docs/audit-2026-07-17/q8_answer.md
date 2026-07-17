先给总判断：A–L 都是真正的架构问题。其中有 4 个容易被误修：

- B：换成 `sendmany` 只是降低冲突面，不等于解决重复打款。
- C：把写库和 `submitblock` 顺序倒过来，仍然存在另一侧的不一致窗口。
- H：同高度生成新 job 不一定错，错误在于把 refresh 当作 invalidation。
- J：生成 debts 也不等于钱能追回；没有授信、留存或准备金，债务记录可能毫无回收价值。

### A. SNAT 导致全体矿工被连坐

① 根因：  
网络层把多个真实主体压缩成一个 IP，而安全策略又把这个失真的 IP 当成唯一身份和处罚主体。根因不是单纯的 `MASQUERADE`，而是“身份丢失 + 单维度风控 + 无爆炸半径限制”。

② 架构级根治：  

- 优先使用 `TPROXY`、DSR、保留源地址路由，或受信任内网中的 `PROXY protocol v2` 传递真实 IP。
- `PROXY protocol` 只能接受来自受信任 edge 的连接，edge 到后端应有 mTLS 或网络隔离，绝不能相信公网客户端自己填的 IP header。
- 网络攻击在 edge 按真实 IP 限流；业务异常在 pool 层按 account、worker、session、credential 等身份处理。
- ban 系统增加 blast-radius guard：基础设施 IP、proxy IP 不得进入普通 autoban；一次规则不得同时影响超过设定比例的在线算力；大范围处罚必须自动降级为告警或限速。
- 任何 ban 都应有 TTL、原因码、证据和一键回滚。

③ 同类隐患：  
CGNAT、校园网、矿场 NAT 本来就可能让几百台设备共用公网 IP；IPv6 privacy address 又可能频繁变化。反过来，botnet 可以分散到大量 IP。只按 IP 风控，会同时产生误杀和漏杀。还要防伪造 `X-Forwarded-For`、proxy 重连换 IP、账号在多个 IP 共享等情况。

④ 通用铁律：  
**任何经过聚合、NAT 或代理而失真的字段，都不能作为高爆炸半径处罚的唯一主体；处罚必须落在最小可识别故障域。**

---

### B. 串行 `sendtoaddress`、UTXO 冲突和超时重付

① 根因：  
这里其实有两个独立问题：

- 没有 wallet single-writer 和 UTXO reservation，导致并发 coin selection、未确认 change 链和冲突交易。
- 没有业务幂等；RPC timeout 被误判成“没有发生”，但 timeout 的真实含义是“结果未知”。

相互冲突的两笔交易通常不能都确认；真正导致重复付款的常见路径是第一次交易已经广播，重试又选了另一批 UTXO，于是两笔都确认。

② 架构级根治：  

- 为每个 chain/wallet 建立带 fencing token 的唯一 payout coordinator，任何时刻只有一个有权花钱的 writer。
- 先在账本生成唯一 `payout_batch_id`，将 payout items 原子冻结，保证同一 item 只能进入一个交易族。
- 先构造交易、锁定 inputs、签名，然后把 raw transaction、txid、input 集合、output manifest hash 持久化，再广播。
- 广播失败或超时时，状态进入 `UNKNOWN`，先查 wallet、mempool 和 chain；只能重播同一份 raw transaction，不能重新调用非幂等的“创建并付款”RPC。
- RBF、CPFP 或重建交易必须记录为同一 replacement family，而不是新付款。
- `sendmany` 可作为批次构造方式，因为它确实为多个地址只生成一笔交易，但它并不解决跨批次并发和 timeout 幂等问题。[Bitcoin Core `sendmany` 文档](https://bitcoincore.org/en/doc/31.0.0/rpc/wallet/sendmany/)

③ 同类隐患：  

- HA 主从 split-brain、两个 wallet 实例持有同一组私钥。
- JSON object 中同一地址出现多次被覆盖，必须先按地址聚合。
- `subtractfeefrom` 导致实付金额与账本不一致。
- 使用未确认 change，形成过长 ancestor chain。
- RPC timeout 后节点重启、mempool eviction、RBF 后 txid 改变。
- 自动重试 `z_sendmany`、Monero `transfer` 等同样有歧义。
- 金额用浮点数导致舍入差异。

④ 通用铁律：  
**所有外部资金操作都按 at-least-once 和结果可能未知来设计；“恰好一次”只能由持久化意图、同一交易重播、幂等键和事后对账共同实现。**

---

### C. `submitblock` 成功后才写 round

① 根因：  
这是典型的跨系统 dual-write：区块链节点和数据库之间不存在共同事务。简单地把顺序改成“先写 DB、再 submit”只会把问题变成 DB 有幽灵块、链上没有块。

更深层的问题是 round 居然在爆块后才创建。round、job 和 share 的因果关系本应在挖矿开始前就存在。

② 架构级根治：  

- 在 job 发给矿工前，先持久化 `ROUND_OPENED`、prevhash、template ID、job ID 和结算规则版本。
- share 被回复 accepted 前，先进入可恢复的 replicated WAL/event log，并绑定 job/round。
- 爆块时，将完整 block blob、block hash、parent hash、coinbase txid、round ID、share cutoff 先写入低延迟 durable WAL，再并行提交多个本地验证节点。
- 状态机至少为：`FOUND → SUBMITTED → NODE_ACCEPTED → ACTIVE_CHAIN → MATURED`，分支为 `REJECTED/ORPHANED`。
- 重启后扫描所有非终态 candidate，并按 block hash 与节点、钱包重新对账。
- 再加一条链上反查兜底：扫描池 coinbase output；链允许时在 coinbase 中嵌入可识别的 round commitment，自动补建遗漏记录。

`submitblock` 返回成功只表示该节点接受了块，不表示最终不会 reorg。[Bitcoin Core `submitblock` 文档](https://bitcoincore.org/en/doc/31.0.0/rpc/mining/submitblock/)

③ 同类隐患：  
RPC response 丢失、提交到一个节点但传播失败、多个 frontend 同时发现候选块、DB replica lag、进程在 share cutoff 前后崩溃、round close 与 late share 竞争、块先 accepted 后 reorg。

④ 通用铁律：  
**不可逆或外部可见动作之前必须有 durable intent；跨系统一致性靠状态机和 reconciliation，不能靠两行代码的调用顺序。**

---

### D. 用高度判断孤块

① 根因：  
把“位置”当成“身份”。高度在 fork 中并不唯一，同一高度可以存在多个合法块；canonicality 还会随 reorg 改变。

② 架构级根治：  

- 候选块必须存完整 32-byte block hash、parent hash、height、coinbase txid。
- 使用强类型 `Hash256` 比较规范化字节，禁止在业务层混用 display hex、大小端和字符串。
- 以当前节点 fork-choice 为准：检查候选 hash 是否处于 active chain，而不仅是高度存在。
- 支付前再次核对 `getblockhash(height) == candidate_hash`、active-chain 状态、确认数和 coinbase 可花费状态。
- canonical、mature、paid、orphaned 必须是可反转的状态机；深度 reorg 用补偿账目处理。

Bitcoin Core 的 `getblockheader` 会对不在主链的块返回 `confirmations = -1`，这正是高度检查无法表达的信息。[Bitcoin Core `getblockheader` 文档](https://bitcoincore.org/en/doc/31.0.0/rpc/blockchain/getblockheader/)

③ 同类隐患：  
hash 大小端错误、连到错误 network、两个节点处在不同 fork、链按 cumulative work 而不是单纯高度选链、成熟后深度 reorg、同高度 explorer 数据与本地节点不同。

④ 通用铁律：  
**序号、时间和高度只能描述位置，不能证明对象身份；任何金融结算必须同时验证精确 ID、祖先关系和当前 canonical 状态。**

---

### E. 用 PPLNS 裁剪数组计算 hashrate

① 根因：  
把货币结算 projection 当成 telemetry 数据源。PPLNS 窗口按“最后 N 份工作”裁剪，覆盖的真实时间会随池算力变化，因此不适合固定时间 hashrate。

② 架构级根治：  

- 建立不可变 share event stream，至少记录 account、worker、job、assigned difficulty、accepted work、分类和接收时间。
- PPLNS、hashrate、reject rate、审计分别建立独立 projection。
- hashrate 按固定时间窗计算：`Σ accepted_work / elapsed_time`，vardiff 下必须按 share difficulty 加权，不能数 share 个数。
- UI 可展示 EWMA，但审计保留固定 bucket 和原始计量依据。
- PPLNS 本身也应按 work/difficulty 归一；如果 vardiff 环境下真按“share 条数”裁剪，会直接造成分账不公平。

③ 同类隐患：  
数组重启丢失、裁剪边界抖动、低算力矿工的 Poisson 方差被误认为掉线、stale share 是否计入混乱、不同算法的 diff1 work 常数混用、客户端伪造时间戳。

④ 通用铁律：  
**同一不可变事件可以产生多个视图，但结算、风控和监控不得共享具有不同保留语义的可变数据结构。**

---

### F. vardiff 对在途 share 立即生效

① 根因：  
验证时读取了连接的“当前 difficulty”，而不是该 share 对应 job 创建时的 difficulty。也就是配置没有版本化、没有绑定因果上下文。

② 架构级根治：  

- 每个 job 保存不可变的 `share_target/difficulty_epoch`。
- 调难度时先创建新的 target epoch，再配套发送新 job；新 target 只适用于新 job。
- 按提交中的 `job_id` 查原始 target，旧 job 在同一 prevhash 下保留一个有界 grace period，以覆盖 GPU/proxy batching 和网络 RTT。
- 旧 job 按旧 difficulty 记账；不能用新 difficulty 多记或少记。
- grace period 到期即拒绝，避免矿工长期利用旧低难度 job 刷 share。
- 新 prevhash 到来时旧 job 应标记 stale，而不是错误地报 `low difficulty`。

Stratum V2 明确规定，`SetTarget` 不作用于已经激活的旧 job，它只作用于未来 job 或尚未激活的 future job。[Stratum V2 Mining Protocol](https://github.com/stratum-mining/sv2-spec/blob/main/05-Mining-Protocol.md)

③ 同类隐患：  
job ID 重用、frontend failover 后丢失 difficulty epoch、proxy 聚合多个下游难度、vardiff 来回振荡、降低难度后把旧高难度 share 按新低难度少记账。

④ 通用铁律：  
**任何已经签发、在途或异步处理的请求，都必须按签发时绑定的不可变规则版本验证，不能读取处理时的 mutable current state。**

---

### G. 新块只靠一个短轮询通道

① 根因：  
把单个可能阻塞、丢事件的通知源同时当作 trigger 和 truth，没有 gap detection、健康度和独立 reconciliation。

② 架构级根治：  

- 至少组合 daemon push/ZMQ、GBT longpoll 或 `waitfornewblock`、以及低频 safety polling。
- 使用多个独立验证节点，监控 network、tip hash、fork-choice work、peer 状态和 tip age。
- 通知只作为“可能变化”的 trigger；收到后重新查询 authoritative tip。
- 使用 sequence number 检测丢事件；检测到 gap 时从最后已知 hash 重建链段。
- tip watcher 发布单调递增的 `chain_epoch`；各 Stratum frontend ACK 当前 epoch，落后者自动摘流。
- 消息总线应 coalesce 到“最新 tip”，不能排队慢慢重放已经过时的 20 个块。
- tip 源超过最大静默时间时进入 failover 或停止发旧工作，不能无限继续挖。

Bitcoin Core 明确说明 ZMQ 通知可能丢失，并提供 sequence number 用于检测；`sequence` topic 还区分 block connect/disconnect。[Bitcoin Core ZMQ 文档](https://github.com/bitcoin/bitcoin/blob/master/doc/zmq.md)；GBT 也原生支持 longpoll。[BIP 22](https://github.com/bitcoin/bips/blob/master/bip-0022.mediawiki)

③ 同类隐患：  
ZMQ subscriber 启动时错过事件、longpoll 卡死、节点被 eclipse、frontend fanout 部分失败、消息队列 backlog、reorg 只有 tip 通知但中间 disconnect 没处理、template build 太慢。

④ 通用铁律：  
**时间关键的状态失效必须同时具备 push、独立 pull reconciliation、gap detection 和 freshness SLO；通知不是事实来源。**

---

### H. 时间戳变化触发同高度强制换工

① 根因：  
对模板做 byte/JSON equality，而没有做 semantic diff；同时没有区分 refresh 和 invalidation。

需要校正一点：同高度的新 transaction set、coinbase 或 merkle root 可以提供更高 fee 或新搜索空间，并非都“无意义”；真正有害的是仅 `curtime` 变化也强制切换，并把旧 job 作废。

② 架构级根治：  

把变化分为 3 层：

- `chain invalidation`：prevhash、consensus rules、bits 等变化；必须立即 clean。
- `same-parent refresh`：transaction set、coinbase、merkle root 变化；可以发新 job，但旧 job 仍可提交，`clean_jobs=false`。
- `rolling field update`：仅 nTime 或允许范围变化；优先允许 miner roll nTime，不必重建 job。

另外：

- 用规范化语义 fingerprint，而不是整个 RPC response hash。
- 对 mempool churn 做 debounce/coalesce，只在 fee 增益、hash-space 耗尽或策略阈值达到时刷新。
- job cache 必须保留旧模板，以便验证在途 share。
- `clean_jobs=true` 只用于旧工作确实不再可能产生有效块的场景。

nonce 重置本身不必然损失算力，因为新 merkle root 是新搜索空间；主要损失来自高频切换、硬件切换开销，以及旧 in-flight share 被错误作废。

③ 同类隐患：  
交易排序不稳定、coinbase extranonce 自动变化、merged-mining aux root 抖动、job ID wrap/reuse、只因 RPC 字段顺序变化就换工、旧 job 模板提前被缓存淘汰。

④ 通用铁律：  
**缓存失效和工作失效必须由语义有效性决定，不能由原始配置“看起来不一样”决定；refresh 不等于 invalidate。**

---

### I. 日志写满磁盘

① 根因：  
把 observability 当成无限资源，并与 chain DB、wallet、业务数据库共享故障域。单文件 rotation 不是完整根治，因为多个文件仍可共同写满磁盘。

② 架构级根治：  

- 日志使用独立 filesystem/volume，并设置 OS 或 project quota。
- 同时设置单文件大小、文件数量、保留天数和整个 logging namespace 的总量上限。
- stdout/container logging driver 也必须有 quota。
- 高频重复日志做 rate limit、sampling 和“已抑制 N 条”聚合。
- production 默认关闭 debug；临时开启必须自动 TTL 到期。
- 异步日志队列必须有上限；过载时丢 debug/info，不能阻塞 share validation。
- 监控 free bytes、inode、写入速率和预计写满时间，保留 emergency reserve。
- 财务 audit event 不应混在 debug log 中，应进入独立、不可变、可校验的审计存储。

③ 同类隐患：  
删除文件但进程仍持有 fd、inode 耗尽、压缩任务抢 CPU/I/O、同步日志卡住 event loop、stack trace 风暴、高 cardinality metrics、日志泄露 RPC password、seed 或 payout 数据。

④ 通用铁律：  
**Observability 本身是不可信工作负载，必须限制 CPU、I/O、内存和存储，并与核心状态隔离。**

---

### J. 低确认垫付没有 orphan debt

① 根因：  
把概率性 receivable 当成已经实现的 revenue，同时没有明确“尾部风险究竟由矿工还是矿池承担”。

② 架构级根治：  

- block revenue 使用状态化资产：`EXPECTED → CANONICAL → CONFIRMED → MATURED`，或转为 `ORPHANED`。
- 低确认付款应记为 advance/credit exposure，而不是最终收入。
- orphan 时生成幂等补偿分录，键为 `block_hash + allocation_id`，防止 reorg event 重复扣款。
- 对 PPLNS 等“仅分实际块收入”的产品，可转成矿工负余额并从未来收益抵扣。
- 对 PPS/FPPS，orphan 和 variance 本来就是矿池承担的产品风险，不能事后偷偷向矿工追债；应由 pool reserve/insurance account 吸收。
- 每个矿工设授信额度、holdback、可垫付比例和总 exposure；全池设置 global cap 和熔断。
- 匿名矿工一旦提币离开，数据库中的 debt 不会自动变成可回收资产，因此必须有准备金或尚未释放的未来收益覆盖。

③ 同类隐患：  
矿工换账号/地址逃债、同一个 orphan 重复生成 debt、深度 reorg、orphan 后块又重新进入 active chain、币价变化造成 reserve 不足、产品条款与账务处理不一致。

④ 通用铁律：  
**任何把暂定收入变成不可逆付款的系统，都必须事先明确风险承担方，并同时具备 reversal ledger、可回收信用敞口和准备金。**

---

### K. 隐私链 note/output 碎片导致打款卡死

① 根因：  
把 wallet balance 当成一个标量，却没有管理 spendable inventory 的拓扑。余额足够不代表能在交易大小、input/note 数、proof 时间、fee policy 和隐私策略范围内花出去。

术语上需要区分：`z address/note` 是 Zcash 类模型；Monero 使用 one-time outputs 和 key images，不叫 z note，但碎片化问题属于同一类别。

② 架构级根治：  

- 建立 treasury inventory service，持续跟踪每个 UTXO/note/output 的金额、成熟度、锁定状态、隐私池、预计 spend weight 和 witness/anchor。
- 在低负载期主动 consolidation/sweep，但要由 privacy policy 控制；合并可能暴露关联，最好使用专用 treasury wallet。
- payout planner 在构造前准确估算 input、output、action、size/weight、proof memory/time 和 fee。
- 超过阈值就确定性拆批，而不是先构造一笔巨型交易再等超时。
- 每批使用独立幂等状态机，持久化 selected notes、operation ID、txid 和锁定状态。
- prover/wallet worker 独立资源池，避免一笔大交易卡死整个打款队列。
- 钱包超时后先查询 async operation 和 note lock，不能直接重试。

Zcash 官方提供 `z_mergetoaddress`，明确包含 note 数限制、交易大小限制、异步 operation ID 和 input lock。[Zcash `z_mergetoaddress`](https://zcash.github.io/rpc/z_mergetoaddress.html)；Monero 则提供会自动拆交易的 `transfer_split` 和 sweep 系列 RPC。[Monero Wallet RPC](https://web.getmonero.org/resources/developer-guides/wallet-rpc.html)

③ 同类隐患：  
wallet crash 后 notes 永久显示 locked、reorg 使 witness/anchor 失效、wallet scan 落后、privacy policy 拒绝跨池花费、memo 增大交易、fee/action 规则升级、consolidation 自己又制造大量小 change。

④ 通用铁律：  
**资金可用性不是 balance，而是满足当前共识、policy、隐私和资源限制的可执行 spend plan。**

---

### L. 自行实现 RandomX，与节点共识实现不一致

① 根因：  
把 spec 当成唯一权威并复制 consensus-critical 实现。即使核心 RandomX 算法正确，coin-specific wrapper 仍可能在 seed height、key block、blob serialization、fork version、nonce offset、endianness 或 activation height 上不同。

② 架构级根治：  

- share validator 必须链接节点实际使用的同一份 PoW 代码，或将节点代码抽成 native sidecar/FFI service。
- pin 精确 source commit、build flags、network 参数和 hard-fork schedule，pool 与 node 作为一个兼容性发布单元。
- 独立实现只能用于 differential testing，不能作为生产 acceptance authority。
- 使用真实历史块和 activation boundary 构造 golden vectors，覆盖 reorg、epoch 前后、各 CPU/JIT/interpreter 路径。
- 启动时运行 self-test；生产中抽样双算，发现不一致立即 quarantine 当前 job，share 标记 pending，而不是继续判好坏。
- 所有达到 network target 的 candidate 必须再由本地 canonical daemon 做完整 block validation，成功后才能确认为 found block。
- 升级前进行新旧实现 shadow validation，并按高度切换，不能滚动发布到一半时混用两个共识版本。

RandomX 项目本身提供 reference implementation、spec 和测试，但具体币的共识仍由该币节点实际集成方式决定。[RandomX reference implementation](https://github.com/tevador/RandomX)；例如 Monero 在自己的节点源码中集成并按 hard-fork 规则启用 RandomX。[Monero 源码](https://github.com/monero-project/monero)

③ 同类隐患：  
seed epoch off-by-one、短 reorg 后继续使用旧 seed、JIT 与 interpreter 差异、CPU feature 检测、大小端、target 比较方向、diff1 常数错误、不同币共用一个名为 `randomx` 的错误配置。

④ 通用铁律：  
**生产系统不得自行复制 consensus-critical cryptography 作为裁决权威；权威实现只能有一个，其他实现只能做交叉验证。**

---

## 你们清单之外，优先排查的经典坑

### M. share 已回复 accepted，但尚未持久化

① 根因：内存 queue、Redis 或异步 DB 被当成最终账本。  
② 根治：share 在返回成功前进入 replicated WAL，消费者按幂等 share ID 重放；所有 PPLNS/统计视图可从日志重建。  
③ 隐患：Kafka `acks=1`、关闭 fsync、schema migration 丢事件、主机断电、队列满后静默 drop。  
④ 铁律：**任何已经对外确认的财务贡献，都必须已经跨越约定的故障边界并可恢复。**

### N. extranonce/job namespace 冲突与 share replay

① 根因：多个 frontend 各自从 0 分配 extranonce、failover 后 counter 重置、job ID 只在本机唯一。  
② 根治：使用带 deployment epoch 和 frontend ID 的全局 work namespace；保证每台矿机搜索空间不重叠；对完整 share tuple 建唯一约束并全局去重。  
③ 隐患：proxy 重连复用 extranonce、job ID wrap、相同 share 跨账号重复提交、缓存淘汰后 replay。  
④ 铁律：**工作空间必须按构造全局不相交；所有网络提交都假定可能重复到达。**

### O. 金额、difficulty 或 reward 使用浮点数

① 根因：把近似数值用于守恒账务。  
② 根治：金额全部使用 atomic unit 的 checked integer/BigInt；比例使用有理数；统一确定性舍入，并把 remainder 记入 dust account。  
③ 隐患：JSON float、不同币 decimals 不同、整数 overflow、负数 underflow、长期累计舍入超发。  
④ 铁律：**钱只用整数记账，任何分配都必须满足逐币种守恒不变量。**

### P. 只有 mutable `balance` 字段，没有双式账本

① 根因：把结果缓存当成事实来源，人工修余额后无法解释资金从哪里来。  
② 根治：append-only double-entry ledger；余额只是 projection；修正使用补偿分录；每天与 wallet、未成熟 coinbase、debt、reserve 做总账对账。  
③ 隐患：并发 lost update、重复消费事件、管理员直接改余额、fee 和 orphan 无法追溯。  
④ 铁律：**任何余额都必须能由不可变、唯一来源的分录完整重放出来。**

### Q. 在线矿池服务拥有无限 hot-wallet 权限

① 根因：一个 Stratum/RPC 漏洞即可转走全池资金。  
② 根治：挖矿服务与 signer 网络隔离；在线仅 watch-only 和 payout intent；HSM/cold signer 校验 output manifest、单笔/日限额、地址策略和审批等级；hot wallet 只放有限 float。  
③ 隐患：wallet RPC 泄露、依赖供应链、内部人员、备份 seed 泄露、恶意 payout address change。  
④ 铁律：**任何在线组件被完全攻破时，其最大可损失金额也必须是预先有界的。**

### R. 节点“RPC 能响应”就被认为健康

① 根因：把 liveness 当 correctness。  
② 根治：发 job 前验证 genesis/network、IBD 状态、tip age、fork-choice work、peer 多样性、共识版本和多节点一致性；不健康节点自动摘除。  
③ 隐患：连接 testnet、节点卡在旧高度、eclipse、系统时钟漂移、升级后共识参数不匹配。  
④ 铁律：**依赖服务的健康检查必须验证业务正确性，而不只是端口存活。**

### S. Stratum 与 payout 控制面没有强认证

① 根因：明文 Stratum、弱 worker password、挖矿凭证同时能改提现地址。  
② 根治：使用 TLS 或 Stratum V2 Noise；认证 pool endpoint；mining credential、portal credential、payout authority 完全分权；地址变更要求强认证、冷却期和异渠道通知。  
③ 隐患：DNS 劫持、proxy MITM、矿机固件篡改、API key 泄露、客服社工改地址。  
④ 铁律：**数据面凭证永远不得拥有资金控制权，work source 和 payout destination 都必须经过认证。**

### T. PPS/FPPS 没有资本与风险模型

① 根因：把长期期望收益当成短期确定现金流。  
② 根治：按实际 hash exposure、difficulty、fee、orphan、luck drought 和 block-withholding 压力计算 reserve；设置全局承保上限、动态 fee 和自动暂停接入。  
③ 隐患：短期算力暴增、租赁算力攻击、fee market 突变、连续坏 luck、深度 reorg。  
④ 铁律：**PPS/FPPS 是承保业务，不只是一个 payout formula；没有资本约束就迟早出现偿付危机。**

### U. RandomX 验证和连接队列可被 DoS

① 根因：未认证客户端可以用很低成本触发昂贵 hash、日志和数据库操作。  
② 根治：先做 frame length、schema、auth、job existence、duplicate、nonce/ntime range 等廉价检查，再进入有界 PoW worker pool；按 assigned share rate 限流，过载时优先丢弃低信任流量。  
③ 隐患：slowloris、JSON bomb、重连风暴、低难度 share flood、高 cardinality metric/log。  
④ 铁律：**所有外部输入的尺寸、频率、并发和最坏计算成本都必须有硬上限。**

### V. Block withholding

① 根因：矿工能独自识别“这是普通 share 还是有效块”，但是否上报完全由矿工决定。  
② 根治：普通 Stratum 架构下没有完整软件根治，只能做长期统计检测、匿名大算力限额、PPS 风险准备金、信誉或抵押管理；真正消除此能力需要 oblivious-share 一类共识/PoW 协议变更。  
③ 隐患：小样本误杀、攻击者拆分身份、竞争池渗透、FAW、算力租赁。  
④ 铁律：**如果参与者能在上报前独占观察结果价值，就必须假设其会选择性披露；应用层统计不能替代协议约束。** 这是经典矿池结构性风险，早期矿池奖励研究已经专门分析过。[Rosenfeld 矿池奖励系统论文](https://arxiv.org/abs/1112.4980)

### W. merged mining 把多条链当成一个生命周期

① 根因：一个 parent share 可能同时产生多个 aux-chain candidate，但提交、canonicality、成熟和收益被错误耦合。  
② 根治：每条链建立独立 candidate 状态机和账本，使用 `(parent_share_id, chain_id, aux_block_hash)` 唯一标识；各自提交、重试、判孤和结算。  
③ 隐患：aux template 过期、chain ID 冲突、AuxPoW commitment 顺序错误、一条链成功掩盖另一条链提交失败。  
④ 铁律：**一个物理事件产生多个经济结果时，每个结果必须拥有独立身份、状态机和 finality 判断。**

贯穿所有事故的核心其实只有 5 条：身份不可丢、规则必须版本化、财务事件先持久化、链上对象按 hash 与 ancestry 认定、任何资源与损失都必须有硬上限。