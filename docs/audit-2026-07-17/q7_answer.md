先给总原则：真正的 7×24，不是“永不崩溃”，而是任何崩溃点都能靠 3 类真相恢复：

- Postgres ledger 是“欠谁多少钱”的真相。
- 链和 mempool 是“钱或块是否已经发出去”的真相。
- 不可变的 config revision 是“当时按什么规则运行”的真相。

任何外部动作都必须可以重复核对；遇到歧义时默认 fail-closed，尤其不能因为 RPC timeout 就再次创建一笔新付款。

## 1. 重启恢复的固定顺序

### 推荐顺序

1. **进入安全启动模式**

   - `payouts=false`、`fee_sweep=false`、新 round 结算暂停。
   - Stratum 暂不发新 job；已连接矿工可保持 TCP，但先不接收需要入账的新 share。
   - 所有后台 worker 只读，不自动把超时意图标成失败。

2. **检查基础条件**

   - 时间同步正常，时钟偏差建议小于 `500 ms`。
   - Postgres 可写、schema version 正确、没有未完成 migration。
   - 磁盘使用率低于 `85%`，WAL、replication slot、备份归档无异常。
   - 加载 Postgres 中最后一个 active config revision；磁盘快照只作缓存。

3. **核对链身份和节点状态**

   - 校验 `chain_id/genesis_hash/network`，防止连到 testnet、错误 fork 或错误币节点。
   - 建议每币至少核对 2 个独立节点。
   - 节点必须结束 IBD，tip 高度、tip hash 和时间基本一致。
   - 从上次稳定 checkpoint 往前回扫 reorg window：大链建议 `20～100` 个块；小链按历史最大 reorg 加安全量，通常要更大。

4. **取得 leader 资格和新的 fencing token**

   - 在取得 token 前不能改变全局会计、付款、fee sweep 状态。
   - 恢复过程中的每次写入也带 token，避免旧 leader 复活后继续工作。

5. **先恢复提交中的块**

   块提交最有时效性，优先于付款扫描。

6. **再恢复“可能已经向外转账”的批次**

   固定按：

   `unknown → sent → 已签名 prepared → 未签名 prepared → created 无 rawtx`

   先处理最可能已经动钱的状态，完成前禁止创建新批次。

7. **恢复 ledger、round 和 maturity 状态**

   - 检查总资产、矿工负债、pool fee、已预留付款、舍入项是否守恒。
   - 完成 reorg 回滚和重新成熟。
   - 不能直接修改余额“修平”，必须用补偿 ledger entry。

8. **启动监控，再逐级开放外部动作**

   推荐顺序：

   `链监控 → Stratum/job → share 会计 → round 结算 → payout planner → signer → broadcaster → fee sweep`

### `submitting` 块怎么归位

意图里必须已有：

- 完整 raw block；
- block hash、height、parent hash；
- coin、job ID、template ID、发现 share；
- 发现时间和提交目标。

恢复时：

- 如果 block hash 在 active chain：归为 `accepted/confirming`。
- 如果已知但不在 active chain：归为 `orphan`，保留完整证据。
- 如果所有节点均不知道：
  - 先验证 stored raw block 与 hash 一致；
  - parent 仍有效时，向多个自有节点重复提交**相同 raw block**；
  - `duplicate/already known` 可视为提交成功；
  - RPC timeout 仍保持 `submitting`，不能标 `rejected`；
  - parent 已离开 active chain 且高度已过，归为 `stale/orphan`。
- 两个同步节点给出一致、确定性的 consensus-invalid，才进入 `invalid`；否则进 `unknown` 人工核查。

### 付款状态归位

| DB 状态 | 链上检查和处理 |
|---|---|
| `sent` | 按 txid 查 wallet、mempool、active chain；已确认则 `confirmed`，在 mempool 则保持 `sent`。查不到时继续检查 inputs 或 account nonce。inputs 未花费时重广播**原始 signed rawtx**；被其他交易花费时找出 spender，进入 `conflicted/superseded`。 |
| 已签名 `prepared` | 和 `sent` 同样核对。链上没有、inputs 未花费时广播同一 rawtx；绝不重新生成另一笔付款。 |
| 未签名 `prepared` | 向 signer 按 intent ID 查询。若 signer 已签，先把 signed rawtx 持久化再继续；若确实未签，可在相同 batch ID 和相同 payload hash 下重试。 |
| `created` 无 rawtx | 理论上没有外部转账。但仍要查 signer 请求记录、wallet 历史和预留 inputs。确认完全未签、未花费后，将旧批次记为 `cancelled`，用补偿记录释放预留，再创建新批次；不要删除旧记录。 |
| `unknown` | 冻结相关余额、UTXO 或 nonce；用 txid、inputs、outputs、金额、change、nonce、intent marker 逐项匹配。自动恢复只允许“唯一匹配”；多解或找不到时要求双人复核。 |

对 UTXO 链，核心是查 inputs 的 spender；对 account/nonce 链，还要查 sender nonce。若 nonce 已被消耗，必须先找出占用该 nonce 的交易，不能直接用新 nonce 再付一次。

“节点查不到 txid”不是交易从未广播的证明。以 Bitcoin Core 为例，`getrawtransaction` 默认只保证查 mempool；查历史交易需要 wallet、`txindex` 或已知 block hash，因此恢复程序必须组合多种查询，而不能依赖单一 RPC。[Bitcoin Core `getrawtransaction` 文档](https://bitcoincore.org/en/doc/25.0.0/rpc/rawtransactions/getrawtransaction/)

### 重启恢复清单

- [ ] payouts、sweep、round close 默认关闭
- [ ] DB、schema、磁盘、时钟正常
- [ ] 加载最后 active config revision
- [ ] 每币校验 genesis、network、tip、sync 状态
- [ ] 从 checkpoint 回扫 reorg window
- [ ] 取得新 leader token
- [ ] 扫描所有 `submitting` block
- [ ] 扫描所有 `unknown/sent/prepared` 付款和 sweep
- [ ] 扫描 `created` 无 rawtx 批次
- [ ] 检查 ledger 守恒、唯一约束、UTXO/nonce 预留
- [ ] 检查 notification outbox
- [ ] 开启链监控和 Stratum
- [ ] 观察 `5～10 min`
- [ ] 最后开启 payouts 和 fee sweep

## 2. “意图先落库，动作后执行”

标准模式是：

1. 一个 DB transaction 写入 intent、payload hash、业务唯一键、资金/UTXO/nonce 预留。
2. transaction commit。
3. worker 领取 intent，并带 fencing token 执行外部动作。
4. 记录 RPC 请求和结果。
5. 如果结果不确定，进入 reconciliation，而不是重新创造动作。

必须先有 intent 的动作包括：

- 提交 found block；
- 请求 signer 签名；
- 广播 payout；
- RBF、CPFP、fee bump；
- fee sweep、归集、冷热钱包调拨；
- UTXO consolidate；
- account nonce 分配；
- 钱包 unlock 或策略临时放宽；
- 任何会改变会计口径的 config；
- 对外通知建议走 transactional outbox。

付款推荐状态机：

`created → outputs_reserved → unsigned_prepared → signing → signed_persisted → broadcasting → sent → confirmed`

关键边界是：

> signed rawtx 必须先持久化，才能调用广播 RPC。

不要直接依赖 `sendmany` 一类“创建、签名、广播一次完成”的 RPC，因为进程可能在钱包广播成功、应用尚未记录 txid 的瞬间崩溃。应拆为构造、签名、保存、广播。

推荐默认值：

- 每个 intent 有 UUID 和业务唯一键。
- 所有金额用最小单位整数，不用 float。
- payload 使用 canonical serialization 后计算 hash。
- intent worker lease `30 s`，每 `10 s` 续租。
- RPC timeout 后标 `unknown` 或保持原状态，不标 `failed`。
- 重试必须使用相同 intent ID 和相同 payload。
- signer 对同一 intent ID 只允许一个 payload hash。

## 3. 新增币和零停机升级

### 新增币分 3 档

| 类型 | 是否停机 | 做法 |
|---|---:|---|
| 已有 adapter、已有算法，仅新增配置 | 不需要 | 创建 disabled coin → 校验节点/genesis → 启动生命周期 → 预热 job/template → 观察 `5 min` → 开端口 → 开 mining；payouts 最后单独开启。 |
| 新算法或新 native hash 库 | 不建议进程内热加 | 先放到独立 verifier 进程做 canary；稳定后滚动升级 Stratum。native ABI、全局状态和 SIGSEGV 风险太高。 |
| 必须换二进制 | 不停整体服务 | 快速重启、FD 继承或多实例蓝绿。DB migration 必须先做 backward-compatible 的 expand。 |

### 3 种升级方式

1. **快速重启**

   - supervisor 拉起；
   - readiness 通过后接流量；
   - 目标进程不可服务窗口 `<3 s`，最好 `<1 s`。
   - 适合小池或已有备用实例，但不算严格零停机。

2. **listener FD 继承**

   - 新进程继承 listening socket，开始接新连接；
   - 老进程继续服务已有 TCP 连接；
   - 老进程停止接新连接并 drain。
   - 注意：FD 继承只继承 listener，不会把现有 Stratum session 搬给新进程。
   - 推荐 drain `30 min`，剩余连接分批、带 jitter 关闭。

3. **多实例蓝绿**

   - L4 load balancer 保持 TCP session 粘性；
   - 新连接进 green，旧连接留在 blue；
   - green 先只跑 Stratum/验证，不取得会计和付款 leader；
   - shares 用全局唯一业务键去重；
   - 切 leader 时 payouts 暂停 `1～2 min`，旧 token 失效后新 leader 才开始。

为接受滚动升级期间的旧 share，旧 job 建议保留 `60～120 s`；但只能接受仍满足链和 stale policy 的 share。

### 矿机断线切备用池窗口

这里没有可靠的统一数值，**不确定**。厂商手册通常只保证有 3 个 pool 和 priority/failover，并没有跨固件稳定的切换秒数；Bitmain 的公开资料也是如此。[Bitmain pool priority 说明](https://docs.bitmain.com/en/antminer/CONTENTS.html)

保守运维假设：

- `<3 s`：大部分矿机只表现为一次快速重连。
- `5～10 s`：已经可能有部分固件切备用池。
- `30 s`：应假设相当一部分设备已经 failover。
- 切到备用池后何时回主池也不统一，可能需要几分钟，**不确定**。

因此不要把“矿工尚未切备用池”当成升级保证。推荐 SLO 是：

- LB/FD 继承：无 accept gap；
- 快速重启：p99 `<2 s`，硬上限 `<5 s`；
- 上线前用实际 Antminer、WhatsMiner、Avalon 和主要第三方固件逐版本实测。

## 4A. 热参数及生效语义

| 参数 | 能否热改 | 推荐生效规则 |
|---|---:|---|
| pool fee | 能 | 新 config revision；PPLNS/PROP 默认从下一个 round 生效，PPS 从下一 share/accounting epoch 生效。已结算 round 和已有余额不追溯。 |
| 确认数 | 能，但敏感 | 增加确认数可立即作用于所有尚未成熟的块；降低只作用于新块，存量块默认保持旧要求。绝不能低于该链 consensus maturity。 |
| 起付额 | 能 | 下一次 payout selection 使用新值；已创建或 prepared 批次不变。 |
| 新增端口 | 能 | 先 bind、health check，再发布；仅影响新连接。 |
| 停用端口 | 能 | 先停止 accept，现有连接 drain；事故时才硬断。 |
| coin mining 开关 | 能 | 关闭后不再发新 job，现有连接迁移或 drain；但 block/reorg/maturity 监控必须继续。 |
| payouts 开关 | 能 | 建议做 `enabled/drain/frozen` 三态。`frozen` 不创建、不签、不广播、不重广播，只监控已知 tx。 |
| fee sweep 开关 | 能 | 与 payouts 分开，默认关闭，人工启用。 |
| vardiff、share policy | 能 | 新连接立即使用；现有连接在下一 vardiff window 或下一 job 生效。 |

确认数默认值不能跨币统一，**不确定且必须逐币配置**。生产建议：

- 永不低于 consensus coinbase maturity；
- payout eligibility 再加 reorg margin；
- 大链可加 `2～6` blocks；
- 小链、低算力链可能需要 `20～100+` blocks，应根据历史 reorg 和攻击成本定。

起付额建议用公式，而不是拍脑袋：

`min_payout >= max(10 × dust, 100 × 单个收款 output 的预计边际手续费)`

这样通常能把付款手续费占比控制在约 `1%` 以下。

### 落库、快照和重启恢复

Postgres transaction 和本地配置文件不能真正原子提交。推荐做法：

1. 一个 transaction 写入：
   - immutable `config_revision`；
   - old/new diff；
   - effective scope/time；
   - audit event；
   - transactional outbox；
   - active revision pointer。
2. 各实例收到通知后，完整验证 revision。
3. 写本地 canonical snapshot，临时文件 `fsync` 后原子 rename。
4. 实例回写 `applied_revision` 和 apply status。

Postgres 是权威源。重启时：

- DB 可用：加载 active revision，发现本地快照旧了就重建。
- DB 不可用：可用最后签名快照启动 Stratum；payouts、sweep 和 leader 功能保持关闭。
- 不允许让旧静态配置覆盖最后一次热修改。

审计记录至少包含：

- 操作者 identity、MFA/审批人；
- 变更时间、请求来源、ticket；
- old value、new value；
- 生效币、端口、实例、round；
- config revision、binary version；
- apply 成功/失败实例；
- 回滚也创建一个新 revision，不能删除原记录。

## 4B. 多实例、leader 和双打款

### 安全的 leader 选举

推荐把 Postgres 作为 authority：

- lease row：`service_name、holder_id、token、lease_until`；
- 使用 DB server time，不用应用机本地时间；
- 取得 lease 的 transaction 锁住该 row；
- 每次新任 leader 都令 token 单调递增；
- heartbeat 默认每 `2 s`；
- lease 默认 `10 s`；
- leader 在连续 `4 s` 无法确认续租时主动停止所有外部动作；
- 新 leader 只能在旧 lease 到期后取得更高 token。

Postgres advisory lock 可以作为额外互斥，但它只是应用自行遵守的锁；session lock 通常持续到连接结束，不能代替外部动作的 fencing。[PostgreSQL advisory lock 文档](https://www.postgresql.org/docs/17/explicit-locking.html)

每次会计、签名和广播请求都带 fencing token。下游必须实际检查：

- token 等于当前 token；
- lease 尚未过期；
- intent 尚未由其他 token 完成；
- batch ID、payload hash 一致。

仅仅“拿到一个 token 但下游不检查”没有意义。

### 为什么 Redis TTL 锁不够

典型场景：

1. leader A 取得 Redis 锁。
2. A 因 GC、CPU stall 或网络分区停顿超过 TTL。
3. Redis 把锁给 leader B。
4. A 恢复后并不知道锁已失效，继续广播付款。
5. A、B 同时具有实际执行能力。

TTL 只能说明锁记录过期，不能撤回旧进程已经持有的钱包权限。fencing token 的价值在于下游拒绝旧世代请求。

### 会不会双打款

网络层面无法保证严格 exactly-once，但可以做到 effectively-once：

- `batch_id` 唯一；
- recipient/output 集合计算 payload hash；
- 同一 batch 只能持久化一个 signed rawtx；
- signer 对 intent ID 幂等；
- UTXO 链对 inputs 做唯一 active reservation；
- account 链对 sender nonce 做唯一约束；
- 广播重试只广播相同 rawtx；
- signer/broadcaster 检查 fencing token；
- ledger debit 通过唯一 journal key 只入账一次。

重复广播同一个 txid 没关系；危险的是重新构造第二笔不同交易。

### 前端 API 聚合

不要让 API 实时 fan-out 到所有 Stratum 实例，也不建议把 Prometheus 当业务查询库。

每个实例每 `5 s` 上报一份 ephemeral snapshot：

- `instance_id/boot_id`
- coin
- active connections
- accepted shares delta
- local hashrate windows
- last_seen

存入 Redis，TTL 推荐 `20 s`；没有 Redis 时，可用 Postgres UNLOGGED 表或普通 heartbeat 表，但注意写放大。

API：

- 实时连接数、算力：只汇总 `last_seen <20 s` 的实例。
- 历史算力、收益、付款：读 Postgres 的分钟/小时 rollup。
- Redis 不可用时退化到最近 DB snapshot，并明确返回 `stale_at`。
- worker ID、address 不要成为 Prometheus label；去重 distinct worker 可用 DB 查询或 HyperLogLog。

## 5. 单进程内的每币生命周期隔离

每币 goroutine tree 至少要有：

- 独立 root context 和 cancel；
- 独立、有上限的 channel；
- 独立 RPC concurrency semaphore；
- deadline、retry budget、circuit breaker；
- 独立模板、share 验证、block monitor、maturity worker；
- 每个 goroutine 顶层 `recover` 并交给 supervisor；
- 单币 restart backoff，建议 `1 s → 2 s → 5 s → 30 s`；
- 文件描述符、RPC 连接和队列容量配额。

故障影响：

- **节点卡死**：只要所有 RPC 有 deadline、连接池和队列隔离，通常只影响该币。
- **普通 panic**：只有在 goroutine 顶层成功 recover 才能隔离；未 recover 的 panic 会结束整个 Go 进程。
- **native hash SIGSEGV/runtime fatal**：无法可靠 recover，通常杀掉整个进程。
- **OOM**：Go heap 是进程共享的，某币泄漏可能让整个进程被 OOM killer 杀掉。goroutine 没有硬内存边界。
- **cgo 死锁或内存破坏**：也可能污染全进程。

必须拆进程的情况：

- 新或不可信的 native hash/CUDA/OpenCL/cgo；
- 大型 DAG 或内存占用不可预测；
- 历史上出现过 native crash、泄漏或无法取消的调用；
- 单币使用超过全进程 `25%～30%` 内存；
- 高价值币需要独立 SLO、权限或升级周期；
- 需要真正的 cgroup memory/CPU 限制。

Linux cgroup/container 的 memory limit 是进程级隔离；触发 OOM 时通常停止相应容器，而不是只停止某个 goroutine。[Kubernetes 资源限制说明](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)

## 6. 监控、指标和告警去重

### Prometheus 指标建议

Stratum：

- `pool_stratum_connections{coin,instance,state}`
- `pool_shares_total{coin,instance,result,reason}`
- `pool_share_validation_duration_seconds`
- `pool_jobs_total{coin,result}`
- `pool_job_age_seconds`
- `pool_effective_hashrate{coin,instance,window}`

链节点：

- `pool_node_rpc_up{coin,node}`
- `pool_node_rpc_duration_seconds`
- `pool_node_height`
- `pool_node_tip_age_seconds`
- `pool_node_peer_count`
- `pool_node_sync_lag_blocks`

块：

- `pool_blocks_found_total{coin}`
- `pool_block_submissions_total{coin,result}`
- `pool_blocks_by_state{coin,state}`
- `pool_last_block_found_timestamp_seconds`

付款和守恒：

- `pool_payout_batches_total{coin,result}`
- `pool_payout_pending_batches{coin,state}`
- `pool_payout_oldest_pending_age_seconds`
- `pool_payout_last_success_timestamp_seconds`
- `pool_unresolved_intents{coin,type,state}`
- `pool_ledger_conservation_ok{coin}`
- `pool_signer_requests_total{coin,result}`

集群：

- `pool_leader_present{service}`
- `pool_leader_changes_total{service}`
- `pool_leader_lease_margin_seconds`
- DB pool、transaction latency、deadlocks、WAL、replication lag
- process RSS、GC、goroutine、FD、CPU、disk free

标签只能使用低基数值：`coin、instance、state、reason enum`。不要把 worker、address、txid、block hash、job ID 放进 label。

精确金额可能超过 Prometheus float64 的安全整数范围。因此守恒告警用 DB 中的精确整数检查，Prometheus 主要暴露 `0/1` 结果和差额的近似展示。

### 推荐默认告警

立即或 P1：

- ledger 守恒不平：完成一个 accounting transaction 后仍非零，持续 `0～1 min`。
- payout 状态 `ambiguous/conflicted`：立即。
- 所有该币节点失联：`15～30 s`。
- Postgres 无法写：`15～30 s`。
- disk `>90%` 或 free `<10 GB`。
- leader 不存在超过 `30 s`。
- signer 接收到 stale token 或 payload hash 冲突：立即。

Warning：

- 单节点失联 `2 min`。
- node lag `>2 blocks` 持续 `2 min`，具体按币调整。
- share reject rate `>3%` 持续 `5 min`，且样本数至少 `1000`。
- stale rate `>2%` 持续 `5 min`；`>5%` 为 critical。
- connections `5 min` 内下降 `30%`；下降 `50%` 为 critical。
- payout 连续失败 `3` 次，或逾期超过正常周期 `2` 倍。
- disk `>80%`、预计不足 `7 d` 填满。
- 最近成功备份超过 `26 h`。
- DB connection pool 使用率 `>80%` 持续 `5 min`。

事件通知但不一定 page：

- found block；
- block 成熟；
- payout confirmed；
- orphan block；
- config revision 生效或回滚。

Effort 是随机变量，不要把高 effort 直接当故障。默认：

- `300%`：信息告警；
- `500%`：人工检查网络难度、模板和 share target；
- 只有伴随模板停止更新、算力不匹配等信号才升 P1。

### 不刷屏

Prometheus 用 `for` 过滤瞬时波动，Alertmanager 负责 grouping、dedup、silence 和 inhibition。[Alertmanager 官方说明](https://prometheus.io/docs/alerting/latest/alertmanager/)

推荐：

- fingerprint：`cluster + alertname + coin`；
- `group_wait=30 s`；
- `group_interval=10 min`；
- `repeat_interval=4 h`，P1 可设 `1 h`；
- DB down 时 inhibit 所有依赖 DB 的子告警；
- fire 和 resolve 使用不同阈值，例如 `>85%` 触发、`<80%` 才恢复；
- 业务事件使用 DB outbox，唯一键为 `entity_id + new_state + channel`，只在状态翻转时发送。

## 7. 100G 磁盘和日志治理

如果区块链节点数据也在这 100G 盘上，很多币根本不可持续。推荐节点数据、Postgres、日志至少分 volume；若只能共盘，必须做硬配额。

### 建议预算

- 保留安全空闲：`20 GB`
- Postgres 数据：最多 `45～50 GB`
- WAL：上限预算 `5～10 GB`
- 应用和 journald 日志：合计 `8～10 GB`
- Prometheus 本地 TSDB：`10 GB`
- OS、临时文件及升级空间：其余

日志默认：

- journald：`SystemMaxUse=5 GB`
- 应用日志总配额：`5 GB`
- 单文件：`100 MB`
- 保留：`7 d` 或 `10` 个压缩轮转，先到者为准
- 正常 share 不逐条写 INFO
- reject 日志按 reason 聚合；详细样本限速，例如每 reason 每分钟 `10` 条
- rawtx、私钥、token、完整付款地址不得进日志

DB retention 默认：

- raw share 明细：`72 h`；流量较小时可升到 `7 d`
- 已结算贡献的分钟聚合：`90 d`
- 小时算力聚合：`2 y`
- round、block、ledger、payout：永久
- audit：永久保存，但 `1 y` 后可移至加密、不可变的 off-host archive
- 每日 partition；过期用 `DROP PARTITION`，不要批量 `DELETE`

raw share 还应受容量配额限制：建议不超过 DB volume 的 `20%`。若 72 小时仍超配额，就必须在 ingest 时聚合，而不是继续缩短到无法排障的程度。

特别监控：

- `pg_wal` 增长；
- 卡死的 replication slot；
- 失败的 archive；
- table/index bloat；
- 临时查询文件；
- 日志轮转失败；
- 被删除但进程仍持有的日志文件。

### 磁盘保护模式

- `70%`：warning 和容量预测。
- `80%`：立即轮转、删除已过期 partition、停止 debug、缩短非关键 metrics retention。
- `85%`：禁止新 payout/sweep，停止 raw share 详细日志，保留聚合和关键 ledger。
- `90%`：停止发新 mining job、停止接收无法可靠持久化的 share；保留链监控和恢复服务，并 P1 page。
- `<80%` 且人工确认后才解除保护，避免抖动。

宁可短暂停矿，也不能继续接受却无法记账的 share。

## 8. signer、钱包和节点安全域

推荐至少拆成 4 个安全区：

1. **Edge/Stratum/API**

   - 公开端口只有 Stratum 和 HTTPS。
   - 无私钥、无 wallet RPC。
   - DB 使用最小权限角色；API 尽量读 replica。
   - egress allowlist，不能访问 signer。

2. **Accounting/Payout coordinator**

   - 私网，无公网 inbound。
   - 选择付款对象、构造 unsigned transaction/PSBT。
   - 自身仍不持私钥。
   - 只有 leader 能创建 signing intent。

3. **Signer/HSM**

   - 独立主机或强隔离 VM，最好独立管理域。
   - 无公网 IP、无默认互联网路由。
   - 只接受特定 coordinator 的 mTLS 请求。
   - 校验 coin/network、outputs、change allowlist、fee cap、daily limit、batch ID、payload hash 和 fencing token。
   - 同一 intent ID 只签一个 digest。
   - 大额付款默认双人审批。

4. **Broadcaster/relay node**

   - 不持私钥。
   - 接收已签 rawtx，向专用 relay node 广播。
   - 与挖矿模板节点分离。

挖矿 full node：

- wallet 功能关闭；
- RPC 仅私网开放；
- RPC credential 与 payout wallet 完全不同；
- 挖矿节点失陷不能触达 signer。

热钱包只保留 `1～3` 个 payout cycle 的金额；其余放冷钱包。付款地址、change 地址必须 allowlist，单笔和单日都设置上限。

容器 namespace 不是足够的钱包安全边界。signer 最好不与 Stratum 共宿主机、共 ServiceAccount、共 hostPath 或共管理员凭据。

## 9. 灰度和回滚

### 影子运行

1. 先做 backward-compatible DB expand，旧 binary 能继续运行。
2. green 以 shadow mode 启动：
   - 复制 job、share、block、config 事件；
   - 运行新验证和新 accounting；
   - 写独立 shadow schema；
   - signer ACL 在基础设施层拒绝 shadow 请求；
   - payout planner 只生成 unsigned 对比结果。
3. 对比：
   - share accept/reject 决策；
   - round 边界；
   - 每矿工贡献和 fee；
   - ledger 守恒；
   - payout recipients、amount、change、fee；
   - p95/p99 latency 和资源使用。
4. ledger 和 payout 对比要求精确相等；不能以“差一点”通过。

### 切流建议

- `1% → 5% → 25% → 50% → 100%`
- 每档至少观察 `15～30 min` 和一个完整 vardiff/job 周期。
- accounting 改动至少影子跑一个完整 round。
- payout 改动至少跑一个完整 payout cycle，必要时更久。
- 旧 TCP 连接留给 blue drain；新连接进 green。
- 最终 leader 切换时先冻结 payouts，废除旧 token，再让 green 获得新 token。

### 快速回滚

- LB 将新连接切回 blue；
- green 停止 accept 并 drain；
- config 回滚创建新 revision；
- 不回滚 Postgres 到旧备份；
- 不删除 green 已写的 intent；
- 已广播 tx 不能“回滚”，必须继续链上跟踪；
- prepared/sent 批次继续由恢复状态机接管；
- contract migration 等旧版本完全退场、回滚窗口结束后才执行。

推荐回滚目标：

- 流量回切 `<5 min`
- payout leader 恢复前至少完成一次 unresolved intent 扫描
- 旧 binary 和兼容 schema 保留至少 `24～72 h`

## 10. 最容易被忽略的事故源

行业里反复出现的通常不是“不会写 Stratum”，而是这些边界问题：

1. **时钟漂移**

   会破坏 lease、证书、审计时间和 share timestamp。至少 2 个 NTP source；偏差 `>1 s` 告警。

2. **错误链或节点分叉**

   升级后连错 network、某节点停在旧 fork。固定校验 genesis，并用两个不同 failure domain 的节点交叉核对。

3. **一次 RPC timeout 被当成失败**

   这是双付款最经典的来源。timeout 必须归 `unknown`。

4. **钱包 RPC 一步完成签名和广播**

   应用来不及记录 txid 就崩溃。必须拆成 signed rawtx 先持久化、后广播。

5. **UTXO 碎片和 account nonce gap**

   小额付款长期产生大量 UTXO；nonce 链则可能因一笔低 fee 交易堵住后续。应监控 UTXO 数、最大 ancestor、nonce gap，并在低峰规划 consolidation。

6. **WAL 或 replication slot 撑满磁盘**

   日志往往不是最大凶手，卡死的 slot 和 archive 才是。宁愿让 replica 重建，也不能让 primary 满盘。

7. **“有备份”但从未 restore**

   每月自动恢复演练；每季度做完整灾难恢复并记录 RTO/RPO。备份必须 off-host。

8. **Prometheus label 基数爆炸**

   把 worker、txid、address 放进 label，会让监控系统先于主服务 OOM。

9. **重启后的连接风暴**

   大量矿机同时重连会耗尽 accept backlog、FD、DB pool。提高 backlog/FD，认证缓存，重连和强制断开加 jitter。

10. **证书、域名、Webhook、MFA 到期**

    TLS 证书、域名、DNS、告警渠道、冷钱包审批账号都要在到期前 `30/14/7 d` 告警。

11. **配置只改了内存或某一台机器**

    必须以 immutable revision 和实例 apply ack 消除 config drift。

12. **reorg 后只改 block 状态，不反转 ledger**

    reorg 必须生成完整补偿 entry，并重新计算 maturity 和 payout liability。

13. **浮点、舍入和整数溢出**

    全部使用最小单位整数；舍入余数进入明确的 rounding account，每次结算做守恒检查。

14. **Kubernetes liveness 配错**

    节点 RPC 变慢不应立即杀整个池进程。readiness、liveness 和 per-coin health 必须分开，避免 restart loop。

15. **矿工改收款地址后的账号接管**

    地址变更应触发通知和 `24～48 h` payout hold；大额账户要求 MFA 或签名证明。

16. **恢复扫描没有分页、上限或 checkpoint**

    真正事故时可能积累数十万条 intent。扫描必须可分页、可中断、可继续，并按风险和时间排序。

最重要的默认策略可以浓缩成一句话：

> 接受 share 可以追求高可用；动 ledger 和动钱必须追求可证明、可幂等、可 fencing；任何无法证明“没有执行过”的动作，都先冻结核对，绝不重新创造。