先给结论：**打款引擎必须按“小型支付清算系统”设计，不能按“定时查询余额，然后调用 `sendmany`”设计。**

按“事故严重程度 × 发生概率”排序，我会这样定级：

| 优先级 | 风险 | 为什么危险 |
|---|---|---|
| P0-1 | RPC 超时后重新生成交易，造成重复打款 | 最常见的致命窗口；链上不可逆 |
| P0-2 | 并发、重启、任务重入，重复预留同一余额 | 高频工程错误，一次即可造成批量双付 |
| P0-3 | signer、热钱包或节点钱包被攻破 | 发生概率略低，但可能一次清空钱包 |
| P0-4 | 单位、精度、费率、币种或地址错误 | 常由多币适配、浮点、配置串币引发 |
| P0-5 | 链上与池内总账不一致却继续打款 | 会把局部故障扩大成资不抵债 |
| P1 | mempool 驱逐、手续费、UTXO/note/nonce 冲突 | 经常发生，但状态机正确就可恢复 |
| P1 | 低确认垫付、孤块和流动性风险 | 小链、攻击链上尤其危险 |
| P1 | Zcash/Monero 异步构造及钱包缓存问题 | 容易形成“到底发没发”的灰色状态 |
| P2 | 批量大小、手续费、隐私和效率优化 | 重要，但应在正确性之后做 |

---

## 1. 打款引擎的完整状态机

### 1.1 不要只建一个 `payment.status`

至少拆成四种实体：

1. `PayoutItem`：某个矿工的某笔应付义务。
2. `PayoutBatch`：为运营、审批和限额而形成的批次。
3. `TransactionIntent`：准备在某条链上产生的一笔交易意图。
4. `ChainTransaction`：实际签名、广播、确认、替换、重组的链上交易。

一个 batch 可能拆成多笔交易；一笔交易可能承载多个 item；Monero `transfer_split` 还可能让一个意图生成多个子交易。因此关系应当允许多对多，不能把 `batch.txid` 当成唯一真相。

### 1.2 文字版状态图

```text
矿工可用余额达到阈值（仅查询结果，不是账务状态）
    │
    ├─ 目标地址/风控失败
    │      └─ HELD：冻结该 item，余额仍属于矿工
    │
    └─ DB 原子预留
           └─ RESERVED
                  │
                  ├─ 审批拒绝且从未生成签名交易
                  │      └─ RELEASED：通过冲正恢复可用余额
                  │
                  └─ 分配到 TransactionIntent
                         └─ ASSIGNED
                                │
                                └─ 交易已签名
                                       └─ COMMITTED
                                              │
                                              ├─ 广播/链上状态不明
                                              │      └─ RECOVERY
                                              │
                                              ├─ mempool / 确认中
                                              │      └─ PENDING_CHAIN
                                              │
                                              └─ 达到结算确认数
                                                     └─ SETTLED
```

`TransactionIntent` 状态应更细：

```text
PREPARED
  → BUILT_UNSIGNED
  → SIGNING
  → SIGNED
  → BROADCASTING
       ├─ BROADCAST_ACCEPTED
       └─ BROADCAST_UNKNOWN
  → SEEN_MEMPOOL
  → CONFIRMING
  → CONFIRMED
  → FINALIZED
```

异常分支：

```text
PREPARED / BUILT_UNSIGNED
  → FAILED_PRE_BROADCAST
  → 可重建或释放预留

SIGNING
  → SIGNING_UNKNOWN
  → 只能按 intent_id 查询 signer，不能盲目再签

SIGNED / BROADCASTING
  → RECOVERY
  → 查询 txid、输入、nonce、opid，不允许生成独立新交易

SEEN_MEMPOOL
  → DROPPED
  → 原交易重播，或建立显式 replacement

CONFIRMING / CONFIRMED
  → REORGED
  → 返回 mempool/恢复处理中，不允许直接再次付款

旧交易
  → REPLACED_BY(new_txid)
  → 新旧交易属于同一个 payout intent
```

这里的 `FINALIZED` 只是业务上的最终性，不代表 PoW 链具有数学上的绝对最终性。

### 1.3 每一步什么时候落库

#### 第一步：筛选候选余额

只读查询：

- `available_balance >= threshold`
- 没有已有的 active payout
- 地址版本有效
- 没有人工冻结、风控冻结
- 币种、网络、付款策略正常

“达到起付额”只是候选条件，不能在这里修改余额。

#### 第二步：原子预留

在同一个 DB 事务中完成：

- 锁定 `miner_account + asset` 的余额行，或使用串行化事务。
- 重新计算可用余额，不能使用任务开始时的旧快照。
- 固定本次结算截止的 `ledger_sequence`。
- 固定金额、地址版本、费率版本、起付策略版本。
- 插入 `PayoutItem`、`PayoutBatch`。
- 总账执行 `AVAILABLE → RESERVED`。
- 写入 outbox 事件。
- 提交事务。

事务提交前崩溃：什么都没发生。  
事务提交后崩溃：余额已经安全预留，恢复程序可继续。

建议早期版本限制为：**同一矿工、同一资产同时最多一个 active payout**。新收益继续进入 `AVAILABLE`，但不再并发生成第二笔支付。它牺牲一点付款频率，能显著降低复杂度。

#### 第三步：验证与审批

落库：

- 地址解析结果和目标二进制形式。
- 网络、genesis hash、地址类型、memo/payment ID。
- 策略校验结果。
- 批次总额、单笔最大额、日累计额。
- 审批人和审批策略。
- payout item 列表的哈希。

高额批次需要双人审批；普通小额可由策略自动审批。

#### 第四步：选择输入和构造

先插入 `TransactionIntent`，再做外部调用：

- UTXO 链：预留具体 `txid:vout`，数据库中必须有唯一约束。
- Account/nonce 链：预留发送账户和 nonce。
- Zcash：预留 note/UTXO，记录可能的 opid。
- Monero：由单一 wallet writer 选 output，尽量使用 `do_not_relay`。

落库完整的：

- 输入集合。
- 收款输出及金额。
- change 地址及金额。
- network fee。
- unsigned transaction 或其加密存档。
- 构造结果哈希。

#### 第五步：签名

调用 signer 前先落库：

- `SIGNING`
- `sign_request_id`
- signer key id
- leader fencing token
- 策略版本
- unsigned payload hash

signer 必须以 `sign_request_id` 做幂等：重复请求只能返回第一次保存的同一结果，不能重新随机签一份新交易。

签名成功后，**必须在广播前**持久化：

- 完整 signed raw transaction。
- `txid`，需要时还有 `wtxid`。
- 输入、输出、fee 的最终复核结果。
- 签名策略结果。

这是整个系统最重要的落库点。

#### 第六步：广播

调用节点前：

- 写 `BROADCASTING`。
- 插入一条 `broadcast_attempt`，状态为 `STARTED`。
- 提交 DB。

然后调用 RPC。RPC 返回后再写：

- 节点身份。
- 返回值或错误。
- `BROADCAST_ACCEPTED` 或 `BROADCAST_UNKNOWN`。
- 时间、延迟、request correlation id。

RPC 超时只能进入 `BROADCAST_UNKNOWN`，不能进入 `FAILED`。

#### 第七步：独立观察链上状态

不要仅信广播 RPC 的返回。独立 watcher 应观察：

- 钱包历史。
- 自有多个全节点的 mempool。
- active chain。
- 输入是否已花费。
- nonce 是否已使用。
- 是否出现 replacement/conflict。
- 确认块高度和 block hash。

#### 第八步：结算

建议：

- `RESERVED` 时，矿工可用余额已经减少，但池的负债没有消失。
- 广播后只是 `IN_FLIGHT`，UI 显示“已发送、待确认”。
- 达到付款确认数后，才将矿工负债与热钱包资产核销。
- 发生 reorg 时使用冲正流水恢复 `IN_FLIGHT`，绝不覆盖旧记录。

一旦产生 `SIGNED` 交易，不能因为“暂时没在 mempool 看见”就释放预留。签名交易可能被其他节点重新广播。

---

## 2. 怎么防止打款出错

### 2.a 杜绝同一余额重复打款

核心不是 distributed lock，而是**账务预留 + 数据库唯一约束 + 链上幂等**。

必须同时做：

1. **先预留，后付款**

   不能“发完币再把余额清零”。正确方式是在 DB 事务里把余额从 `AVAILABLE` 转到 `RESERVED`，外部 RPC 只能处理已经预留的 item。

2. **固定余额截止点**

   记录本次包含到哪个 `ledger_sequence`。预留之后新产生的收益属于下一次付款，不能混入当前 item。

3. **数据库最后防线**

   至少建立：

   - 同一账户、资产、预留序号的唯一业务键。
   - active reservation 唯一约束。
   - UTXO `network + txid + vout` 的活跃租约唯一约束。
   - Account 链 `wallet + nonce` 唯一约束。
   - `chain + txid` 唯一约束。
   - ledger source/business key 唯一约束。

4. **状态转换使用 CAS**

   例如只允许 `SIGNED → BROADCASTING`，并校验版本号。两个 worker 同时操作，只有一个能更新成功。

5. **每个 wallet 只有一个写者**

   可以有多个 watcher，但构造、签名、广播必须由一个有 fencing token 的 leader 控制。旧 leader 即使“复活”，signer 也应拒绝旧 epoch。

6. **调度器可以重复，副作用必须幂等**

   分布式系统不存在可以轻信的“恰好一次”。正确目标是：

   - 任务和消息允许 at-least-once。
   - DB 写入由唯一键幂等。
   - 链上重试只重播同一 signed raw transaction。
   - 通过 reconciliation 修复遗漏。

7. **同一 raw transaction 可以重播，不能重新付款**

   同一 signed raw 的 `txid` 不变，重播不会形成第二笔付款。重新调用 `sendmany` 或重新选择另一组输入，则可能产生第二笔有效交易。

### 2.b 杜绝金额算错

金额错误主要来自单位、浮点、RPC 序列化和手续费规则。

具体要求：

- 所有账务金额使用最小原子单位整数，例如 satoshi、piconero、zatoshi。
- 禁止 `float`、`double`、JavaScript `Number` 进入账务链路。
- 数据库使用足够宽的整数或 `NUMERIC(..., 0)`。
- 百分比费率存成整数比例，例如 numerator/denominator、ppm 或 bps。
- RPC 要求小数金额时，由原子整数生成精确的十进制文本；不要先转浮点再 JSON 序列化。
- 每张表都带 `asset_id`，金额类型在逻辑上必须绑定资产，不能把 BTC atomic amount 传给 XMR adapter。
- 明确定义“池承担手续费”还是“矿工承担手续费”。

推荐普通矿池由池承担 network fee：

```text
矿工 payout item 金额 = 收款输出金额
network fee = 池的手续费支出
```

如果矿工承担手续费，则 item 必须同时记录：

```text
gross_amount
allocated_fee
net_output_amount
fee_policy_version
```

不能在钱包 RPC 内临时使用“从输出扣手续费”，否则实际输出可能和池内承诺不一致。

签名前必须检查：

- `Σ item.net = Σ recipient outputs`
- UTXO 链：`Σ inputs = Σ recipients + Σ change + fee`
- 每个 output 都能映射到 item 或受控 change。
- 没有负数、溢出、额外精度和 dust。
- 单笔、单批次、单日总额不超过限额。
- 与过去付款相比不存在 10 倍、1000 倍异常跳变。

signer 前还应有第二套策略校验器，直接解析最终交易，重新验证金额和目标。不要让 signer 只负责“给任意字节签名”。

舍入差额必须进入显式的 `ROUNDING` 账户，哪怕只有 1 个 atomic unit；不允许用“误差范围”把不平账吞掉。

### 2.c 杜绝打给错误地址

地址不是普通字符串，而是带网络语义的目标。

具体做法：

1. **地址版本不可变**

   修改地址不是 UPDATE 原字段，而是创建一个新 `destination_version`。每个 payout item 永久绑定当时的版本。

2. **保存三种形式**

   - 用户原始输入。
   - 规范化后的展示形式。
   - 实际目标的二进制形式，例如 `scriptPubKey`、receiver bytes、subaddress 信息。

   Bitcoin 系最终签名校验应针对 `scriptPubKey`，而不是再次解析一个可能变化的字符串。

3. **禁止通用小写化**

   - Bech32 混合大小写通常非法。
   - Base58、Monero 地址大小写敏感。
   - EVM 地址大小写还可能带 checksum。
   - 不要对所有币统一做 lowercase、Unicode normalize。

4. **入口清洗要严格**

   拒绝：

   - Unicode 隐形字符。
   - 非 ASCII 地址中的异常字符。
   - 前后之外的嵌入空格。
   - 矿工 worker 后缀误入地址。
   - 多余小数点、冒号、控制字符。

5. **三次验证**

   - 地址录入/Stratum authorize 时。
   - payout reservation 时。
   - signer 解析最终交易时。

6. **绑定网络身份**

   地址记录必须绑定：

   - `asset_id`
   - network
   - genesis hash 或 chain id
   - 地址类型
   - memo/payment ID
   - adapter/validation 版本

   很多 Bitcoin 分叉币共用地址前缀；“地址格式合法”不代表“币种正确”。

7. **地址修改增加冷静期**

   建议新地址：

   - 24–72 小时冷静期，具体时间按业务风险设定。
   - 原地址及新地址同时发送通知。
   - 高额账户要求 MFA 或人工复核。
   - 新地址首笔设置较低上限，矿工确认收到后再解除。

8. **不要在付款时动态解析 DNS/OpenAlias**

   如果支持 OpenAlias，应在地址登记时解析并让用户确认最终地址，保存快照。付款时重新解析会把 DNS 变更直接变成付款目标变更。

9. **无效地址只隔离一个 item**

   地址脏数据应进入 `HELD_INVALID_DESTINATION`，不能让它污染整个批次，也不能默默改写地址。

---

## 3. 打款失败的分类及重试策略

最重要的分类不是“成功/失败”，而是：

- 已证明没有外部副作用。
- 可以用同一幂等对象重复。
- 外部副作用未知。

| 情况 | 正确处理 | 能否自动重试 |
|---|---|---|
| 查询余额、区块、手续费 RPC 超时 | 指数退避重试只读请求 | 可以 |
| unsigned 构造失败，尚未签名 | 同一 intent 下重新构造 | 可以 |
| 手续费估算失败 | 等待估算恢复，或使用有上下限的批准策略 | 可以，但不能突破 fee cap |
| spendable balance 不足 | 保持 `RESERVED`，等待 treasury 补充；检查 immature/locked 资金 | 只能重试检查，不能重复创建付款 |
| signer 超时 | 按 `sign_request_id` 查询 signer 已保存结果 | 仅在 signer 幂等时可以 |
| 已知 raw/txid 的广播超时 | 查 txid、输入，然后重播同一 raw | 可以 |
| `sendmany` 超时且拿不到 raw/txid | 进入 `BROADCAST_UNKNOWN`，查钱包历史和输入 | 绝对不能重新调用付款 |
| 节点返回 already known/already in chain | 作为已观察到处理 | 不需新交易 |
| fee 太低 | 显式 RBF、CPFP 或同 nonce replacement | 可以，但必须关联原 intent |
| 地址、金额、共识格式非法 | 冻结该 intent，调查 adapter/配置 | 不能盲目重试 |
| mempool 中消失 | 查多个自有节点、输入、冲突交易 | 不能据此重新付款 |
| Zcash 已取得 opid | 轮询同一个 opid | 可以 |
| Zcash 调用超时且没拿到 opid | 枚举 operation、钱包历史并人工/自动匹配 | 绝对不能再提交相同操作 |

### RPC 超时是不是最危险的窗口

如果使用 `sendmany/sendtoaddress` 这种“构造 + 签名 + 广播”一体 RPC，并且响应丢失，答案是：**是，它通常是最危险的窗口。**

节点可能已经广播，只是 HTTP 响应没回来。此时再调用一次，会产生另一组输入和另一个 `txid`，两笔都可能确认。

根治方式是拆开：

```text
构造 → 持久化 → 签名 → 持久化 raw/txid → 广播
```

这样广播超时后，只会重播相同 raw。Bitcoin Core 的 `sendrawtransaction` 接受的是已确定 raw transaction，因此重播不会形成第二笔业务付款；需要把 already-known/already-in-chain 视为观察结果，而不是重新构造的理由。[Bitcoin Core `sendrawtransaction`](https://bitcoincore.org/en/doc/25.0.0/rpc/rawtransactions/sendrawtransaction/)

如果某个小币 daemon 只提供不可拆分的 opaque RPC，那么无法在池侧彻底消除这个窗口。只能：

- 每个 wallet 严格单写。
- 每次最多一个未决请求。
- 请求前落库完整 intent。
- 在 wallet comment/metadata 中附加 intent id，如果 daemon 支持。
- 超时立即冻结该币付款。
- 查询 wallet history、输入、余额和链上记录。
- 无法证明未发送时，人工处置。

MPOS 甚至专门记录过这种故障模式：daemon 实际执行了 `sendtoaddress`，却返回 HTTP 500；其处理策略是停止付款 cron，要求运营者核对，以防双付。[MPOS Error Codes](https://github.com/MPOS/php-mpos/wiki/Error-Codes)

### mempool 驱逐怎么处理

mempool 不是共识真相。一个节点没看到交易，不代表交易不存在。

处理顺序：

1. 查自有多个节点。
2. 查 active chain。
3. 查原输入是否仍未花费，或 account nonce 是否已使用。
4. 查是否有 replacement/conflict。
5. 输入未花费且 raw 有效：重播同一 raw。
6. fee 不足：

   - 支持 RBF：以相同输入构造 replacement。
   - 有受控 change：可以考虑 CPFP。
   - Account 链：以相同 nonce 替换。

replacement 必须使用相同输入或相同 nonce，从协议上保证旧、新交易不能同时确认。**用另一组独立输入再给矿工付一次，不叫 replacement，而叫第二次付款。**

---

## 4. 池内记账与链上不一致

需要两个方向完全独立的 reconciler。

### 4.1 池内显示已付，但链上没有

正确设计下，“广播”不应立刻变成最终 `PAID`，而应是：

```text
RESERVED → IN_FLIGHT → SETTLED
```

检测条件包括：

- `SETTLED` item 没有达到要求确认数。
- intent 长时间停留在 `BROADCAST_UNKNOWN`。
- txid 不在 active chain，也不在自有节点 mempool。
- 输入仍未花费，或被其他交易花费。
- 确认 block hash 已不属于主链。
- Zcash opid 成功但池内缺少 txid，或 opid 状态丢失。

修复：

- raw 和 txid 已知、输入未花费：重播同一 raw。
- fee 问题：做有明确关联的 replacement。
- 原交易被受控 replacement 取代：将 item 映射到新 txid。
- 原交易被确认冲突交易永久作废：通过冲正把 `IN_FLIGHT → AVAILABLE`，或重新建立付款 intent。
- 无法证明原交易不会再确认：保持 `RECOVERY`，不能二次付款。

如果旧系统已经错误地把余额清零，应通过补偿流水恢复负债，不能直接 UPDATE 余额。

### 4.2 链上已经付款，池内没有记录

独立的 chain-to-book scanner 应检查所有受控钱包的 outbound transaction：

- UTXO 是否来自池控制的输入。
- Account 地址是否使用了池的 nonce。
- 输出是否能映射到已批准的 payout item。
- change 是否回到受控地址。
- fee 是否在策略范围。
- 是否属于 consolidation、shielding、treasury transfer 或 fee bump。

所有受控 outbound transaction 都必须有一个明确 intent；不仅 payout 要有，钱包整理和 treasury 转账同样要有。

处理：

- 链上交易与现有 intent 完全匹配：补写缺失状态和账务，使用 `txid/vout` 唯一键保证幂等。
- 有付款输出，但找不到 intent：冻结付款，按未授权交易处理；可能是人工转账、软件绕过或私钥泄漏。
- Monero 等无法单靠公共链恢复收款人明细的币：依赖提前保存的 raw、tx metadata、tx key 和 wallet cache。恢复钱包后，部分历史 recipient 信息未必能仅从链上重建，因此不能只备份 seed。[Monero Wallet RPC](https://docs.getmonero.org/rpc-library/wallet-rpc/)

chain scanner 应从持久化 checkpoint 向前扫描，但每次都回退若干区块重扫，以覆盖 reorg。

---

## 5. 打错后的追回与平账

链上确认后不存在技术上的“撤销”。现实手段只有：

1. 未确认时尝试 RBF、同 nonce replacement 或冲突取消，且不保证成功。
2. 联系收款人主动返还。
3. 如果地址属于交易所或 VASP，立即联系其安全/合规团队冻结。
4. 保存证据，走民事、刑事或其他法律渠道。
5. 对可识别矿工建立应收款，从未来收益抵扣。
6. 无法追回的部分由池的事故准备金或权益承担。

### 不同错误的账务归属

#### 多打给了正确矿工

假设应付 10 X，实际付了 20 X：

- 正常 10 X：核销矿工负债。
- 多出的 10 X：记为 `RECIPIENT_RECEIVABLE`。
- 如果回收概率很低，计提坏账；最终进入 `INCIDENT_LOSS`。

不要把矿工的 `AVAILABLE` 悄悄变成 `-10`。应独立显示：

- 新产生的收益。
- 应收债务。
- 本期抵扣。
- 剩余债务。

只有 ToS、账户身份和当地法律允许 set-off 时，才从未来收益抵扣。匿名地址可能直接离开，风险模型中应把回收率按 0 计算。法律可执行性因司法辖区而异，这部分需新加坡当地律师确认。

#### 池自身原因打给错误地址

原矿工的应付负债仍然存在。池必须：

- 把错误转账记为事故损失或对错误收款人的应收。
- 另用池资金向正确矿工付款。
- 不能用“链上已经付过”来核销正确矿工的余额。

#### 矿工自己填写了错误但有效的地址

是否由池承担取决于 ToS、地址变更流程、安全控制和当地法律。技术上仍不可逆。若地址属于交易所且仅缺 memo/payment ID，交易所有时可以人工恢复，但不保证成功。

#### 孤块奖励已经提前付出

如果产品宣传的是池承担风险的“快速付款”，孤块损失应由池准备金吸收。

如果协议明确写明这是 provisional advance，并允许从未来收益抵扣，可以建立 `MINER_RECEIVABLE`；但这会明显削弱“快速确认即最终到账”的卖点，也可能产生法律和口碑风险。

### 标准事故流程

```text
冻结受影响资产的签名和广播
→ 保存 DB、日志、signer、wallet、节点证据
→ 确定受影响 intent、txid、矿工、金额
→ 判断未确认交易是否可替换
→ 联系收款人/交易所/法律渠道
→ 创建 incident id
→ 通过补偿总账入账
→ 双人复核损失和恢复范围
→ 修复根因并完成全量 reconciliation
→ 小额度 canary 恢复
→ 正常恢复
```

绝不能删除原交易、修改原流水或制造一笔“平衡交易”掩盖损失。

---

## 6. 打款审计和总账守恒

### 6.1 审计流水字段

每笔 ledger transaction 至少记录：

- `ledger_tx_id`、`entry_id`、account sequence。
- `asset_id`、network、pool id。
- debit account、credit account。
- atomic amount。
- 业务类型和唯一 business key。
- block、round、reward、payout item、batch、intent、txid 来源。
- `effective_at` 与 `recorded_at`。
- actor/service、部署版本、adapter 版本。
- payout policy、fee policy、rounding policy 版本。
- correlation id、idempotency key、event id。
- reversal_of、replacement_of、incident id。
- 原因、审批人、审批时间。

付款相关还要记录：

- destination version、地址 fingerprint、地址类型、memo/payment ID。
- 输入 UTXO/note 或 nonce。
- 每个输出的二进制目标和金额。
- change、fee、fee rate。
- unsigned/signed payload hash。
- `txid/wtxid`。
- signer key id、策略校验结果、fencing token。
- 每次广播节点、结果和错误。
- 确认高度、block hash、reorg、replacement 链。

Monero `tx_key`、signed metadata、raw transaction 等应加密保存并严格限制访问。

posted ledger entry 必须 append-only。修正只能创建 reversal/compensating entry。数据库 UPDATE 后的审计日志不能替代真正的不可变总账。

### 6.2 池总账守恒

至少做四层校验。

#### 第一层：每笔复式记账自身平衡

对每个资产：

```text
Σ Debit = Σ Credit
```

必须精确到 1 个 atomic unit，容差为 0。

#### 第二层：收益分配守恒

```text
区块可分配收益
= 矿工奖励
+ 池费
+ donation / masternode / founder 等显式分项
+ rounding account
```

#### 第三层：交易守恒

UTXO 链：

```text
Σ inputs
= Σ miner outputs
+ Σ controlled change
+ network fee
```

所有输出必须分类，不能存在“未知输出”。

#### 第四层：资产负债守恒

按每个资产独立检查：

```text
受控链上资产 + 明确应收
= 矿工负债 + 其他负债 + 池权益/准备金
```

矿工负债应分别包括：

- available
- reserved
- in-flight
- 已确认但尚未对外展示完成的 clearing
- 按会计政策确认的 immature/provisional liability

此外还要单独检查流动性：

```text
已确认、可支出的流动资产
≥ 当前到期矿工负债 + 已预留手续费 + 安全缓冲
```

immature coinbase、锁定 note、未解锁 Monero output 不能算作可用流动性。

复式账平衡并不能证明没有打错地址，因此还要验证：

- 每个 settled item 对应链上确认输出。
- 每个 outbound transaction 对应已批准 intent。
- 每个 reserved liability 最多被一个 active payout 占用。
- 每个 replacement family 最终最多有一笔确认付款交易。

### 6.3 校验频率

- DB 事务提交时：局部复式和唯一性校验。
- 签名前：batch 和 transaction 完整校验。
- 每 1–5 分钟：wallet/chain 双向 reconciliation。
- 每个新区块：确认和 reorg 检查。
- 每日：完整关账、钱包资产证明、冷热钱包和负债对账。
- 每次 daemon、wallet、adapter 升级后：全量重跑。

发现未知 outbound transaction、总账不平或余额解释不了时，第一反应是：

> 冻结受影响资产的新签名和新广播，保留挖矿和 share 入账；如果疑似 signer 泄漏，则冻结所有共享 signer 的资产。

不能为了可用性继续付款，也不能自动往“suspense”塞一条数让告警消失。

---

## 7. 批量付款还是逐个付款

### UTXO 链的默认推荐

采用**有上限的 micro-batch**：

- 减少每个收款人的输入/手续费开销。
- 降低钱包调用次数。
- 减少 UTXO 碎片。
- 但限制单次事故爆炸半径。

限额应按以下指标，而不只是输出数量：

- batch 总价值。
- 最大单一 item。
- transaction vsize/weight。
- 输入和输出数量。
- fee 绝对值及 fee rate。
- 构造、签名和证明耗时。
- 日累计限额。
- 热钱包资产占比。

高价值付款应单独成批、双人审批。具体“50 个还是 200 个输出”没有跨链统一答案，必须按链的标准交易限制、手续费和压测结果配置。

### 逐个付款适合

- 高价值矿工。
- 风险或人工复核账户。
- 地址类型特殊。
- 链上手续费很低。
- 需要隔离失败和加速确认。
- Account/nonce 链不支持可信的原生多收款交易。

### “部分输出失败”怎么理解

对普通 UTXO transaction 来说，交易是原子的：

- 要么整笔被接受并最终确认。
- 要么整笔不成立。
- 不存在其中 9 个输出成功、第 10 个地址失败。

某地址非法导致 builder 拒绝时：

1. 将该 item 转入 `HELD_INVALID_DESTINATION`。
2. 其他 item 保持 `RESERVED`。
3. 废弃尚未签名的 transaction intent。
4. 重新建立只含有效 item 的 intent。

如果交易已经签名并因 policy/non-standard 原因被拒，不能简单认为它永远不会上链；矿工可能绕过本节点 policy 打包。此时要检查输入，并通过相同输入的受控 replacement 消除歧义。

Monero `transfer_split`、某些 wallet 批量 API 可能实际产生多笔子交易。这时确实可能出现：

```text
Batch = PARTIALLY_SETTLED
```

必须逐个保存 `tx_hash_list`，把 item 映射到具体子交易。恢复时只能处理未付款 item，绝不能重跑整个 batch。

---

## 8. 崩溃、断电后的恢复流程

启动时应 fail-closed：

1. 暂停新 payout 调度、签名和广播。
2. 获取 wallet leader lease，并产生新的 fencing epoch。
3. 确认旧 executor 已失效。
4. 核对节点 genesis hash、network、同步高度、chainwork。
5. 核对钱包 fingerprint、signer key id、策略版本。
6. 从旧 checkpoint 回退若干区块重扫。
7. 逐个恢复悬挂状态。

| 悬挂状态 | 恢复动作 |
|---|---|
| `DRAFT` | 可丢弃重建，没有账务效果 |
| `RESERVED` 但无 intent | 确认没有任何 signed artifact 后重建或冲正释放 |
| `BUILT_UNSIGNED` | 核对输入租约后重建或继续 |
| `SIGNING` | 按 request id 查询 signer；不能盲目再签 |
| `SIGNED` | 根据已保存 txid 查询；未见且输入可用时只广播同一 raw |
| `BROADCASTING/BROADCAST_UNKNOWN` | 查链、mempool、钱包、输入、nonce；不生成新交易 |
| `SEEN_MEMPOOL` | 继续观察；驱逐后重播同一 raw 或显式 fee bump |
| `CONFIRMING/CONFIRMED` | 核对 block hash 仍在 active chain |
| `REPLACEMENT_PENDING` | 追踪整个 replacement family |
| `REORGED` | 判断原交易回到 mempool、失效还是被冲突交易替换 |
| Zcash `OP_SUBMITTED` | 查询所有 opid/status/result，不能重新提交 |
| Monero pending 子交易 | 根据 tx hash、metadata、key image、wallet transfer 状态核对 |

之后再检查：

- DB 的 UTXO/note/nonce 租约是否与钱包一致。
- 是否存在链上 outbound 但 DB 无 intent。
- 是否存在 settled item 但无确认交易。
- ledger conservation。
- 热钱包流动性。

只有未解释差异为 0，才按顺序恢复：

```text
watcher → broadcaster/recovery → payout scheduler
```

share 接收和收益记账可以继续，只要与 money plane 完全隔离。

---

## 9. 低确认数快速付款与垫付资金

这不是“提前花 coinbase”，因为未成熟 coinbase 根本不能花。它本质是：

> 池用自有流动资金，向矿工发放一笔以 immature coinbase 为支持的 advance。

假设：

- `M`：coinbase maturity。
- `q`：开始垫付时的确认数。
- `D = M - q`：资金被占用的窗口。
- `h`：池占全网算力比例。
- `B`：每个池出块实际垫付给矿工的净额。

稳定条件下，窗口内尚未成熟的池出块数近似：

```text
N ~ Binomial(D, h)
```

小 `h` 时可近似 Poisson，均值为：

```text
E[N] = D × h
E[垫付占用] = D × h × B
```

但不能按均值备钱。应使用精确分布或 Monte Carlo 的高分位数：

```text
R_target
= Q99.9(未来窗口内的垫付峰值)
+ reorg/orphan 压力损失
+ 下一最大付款批次
+ 手续费和运维缓冲
```

生产中更直接的实时指标是：

```text
当前 advance exposure
= Σ 已经付给矿工、但对应 coinbase 尚未成熟/最终的金额
```

流动资金必须始终覆盖该实际 exposure 加压力缓冲。

风险控制：

- 每个币独立准备金，不用另一币种未对冲资金顶替。
- 不得使用矿工余额、immature coinbase 或“未来应该能挖到的块”作为准备金。
- 使用峰值/上置信界算力，而不是长期平均算力。
- 设单矿工、单区块、每日 advance 上限。
- 出现节点 tip 分歧、异常 reorg、孤块率上升、硬分叉、daemon 升级、全网算力突变时自动关闭。
- coverage ratio 低于董事会批准阈值时自动关闭。比如 1.3–1.5 倍压力 exposure 可作为初始测试值，但这是**示例，不是通用安全标准**。
- PPS 的 luck/variance reserve 与这里的 maturity working capital 必须分别计算。
- 风险测算中的孤块追回率按 0，不依赖未来扣款。

对 Bitcoin 主网，5–10 个确认后的正常 reorg 风险很低；但对小众 PoW 链、NiceHash 可租算力链和曾发生深度重组的链，这个判断完全不成立。具体要准备抵抗几个深度重组、几笔池自挖块，需要按链的攻击成本决定，**不存在统一安全数字，不确定性很高**。

如果产品承诺“快速付款且最终”，孤块损失应由池承担；否则应明确写成“可追索 advance”，不能事后改变规则。

---

## 10. Zcash/Hush 和 Monero 的额外坑

### 10.1 Zcash/Hush 系

#### 异步 opid

`z_sendmany` 是异步操作，先返回 opid，之后经历 queued/executing/success/failed。[Zcash `z_sendmany`](https://zcash.github.io/rpc/z_sendmany.html)

状态应至少有：

```text
OP_REQUESTED
→ OP_SUBMITTED(opid)
→ OP_EXECUTING
→ OP_SUCCESS(txid)
  或 OP_FAILED
```

规则：

- 获取 opid 后只轮询该 opid。
- 使用 `z_getoperationstatus` 查看结果并持久化；某些 result API 会在取出结果后清理 operation，因此必须先落库。[Zcash `z_getoperationstatus`](https://zcash.github.io/rpc/z_getoperationstatus.html)
- RPC 超时且没拿到 opid 时，不能再次调用 `z_sendmany`。
- 枚举现有 operation，按参数哈希、时间、钱包交易记录匹配。
- opid 在 daemon 重启后的持久性可能因版本和 fork 不同，**不能假设一定保留，不确定**。

#### note/UTXO 合并

- `z_shieldcoinbase`、`z_mergetoaddress` 也属于异步操作，并可能锁定 UTXO/note。
- 合并、shielding 和 payout 应使用同一 wallet resource queue，不能并发争抢输入。
- 最好将 consolidation 作为独立 treasury intent，在低峰期运行。
- 每次操作都记录被锁 note/UTXO、opid、目标地址和 txid。

Zcash coinbase UTXO 的花费和 shielded recipient 存在额外限制，应优先走明确的 shielding 流程。[Zcash `z_shieldcoinbase`](https://zcash.github.io/rpc/z_shieldcoinbase.html)、[`z_mergetoaddress`](https://zcash.github.io/rpc/z_mergetoaddress.html)

#### 其他问题

- shielded proof 构造可能耗时较长、占用大量 CPU/内存；timeout 不等于失败。
- 显式配置 privacy policy，不能失败后自动降级为透明付款。
- Sapling、Orchard、Unified Address 的 receiver 选择必须落库。
- 手续费规则要按实际 daemon 能力适配；当前 Zcash 的 ZIP 317 规则不能直接套到 Hush 老 fork。
- Hush 及各种 Zcash fork 对 opid、fee、shielding、transaction expiry 的兼容性差异很大，必须逐链 capability probe；这里无法给统一结论，**不确定**。

### 10.2 Monero 系

推荐把构造与广播拆开：

```text
transfer / transfer_split(do_not_relay)
→ 保存 tx hash、tx blob、tx metadata、tx key
→ relay_tx
```

官方 Wallet RPC 提供 `do_not_relay`、`relay_tx`、`transfer_split` 等能力，应充分利用。[Monero Wallet RPC 指南](https://www.getmonero.org/resources/developer-guides/wallet-rpc.html)

特别注意：

- `transfer_split` 可能产生多个 tx hash，必须逐笔映射 item。
- 同一 spend key 只允许一个 wallet writer，不能运行多个可花费实例。
- 判断资金时使用真正的 unlocked/spendable balance，不是 total balance。
- wallet 必须 refresh 到可信高度后才能付款。
- output/key image 状态滞后会导致重复选取或错误判断。
- 大量小 output 会增加选币、ring 构造、fee 和交易大小压力，合并操作要独立调度。
- integrated address、subaddress、旧 payment ID 规则必须精确验证。
- `unlock_time` 必须有上限策略，避免生成异常锁定的付款。
- tx key、metadata、raw transaction 要加密长期保存。
- 钱包从 seed 恢复后，链上可以恢复资金，但未必能恢复原 wallet cache 中所有 recipient metadata；不能只备份 seed。
- view-only 审计钱包如果没有正确导入 key image，不能可靠判断哪些 output 已被花费。
- Monero 公链不能像透明 UTXO 链那样直接从链上证明“某个公开地址收到了某个明文金额”，因此内部元数据和 wallet cache 的重要性更高。

---

## 11. 行业事故与教训

商业池很少公开完整技术 postmortem，因此必须区分“公开确认事实”和“推测根因”。

### 11.1 NiceHash 2017：支付钱包大额失窃

NiceHash 属于算力市场和付款平台，并非传统单一矿池，但事故与矿池 payout 钱包高度相关。官方披露约 4,700 BTC 被盗；公开材料对完整入侵路径披露有限，因此具体技术根因不能过度推断。[NiceHash 官方声明](https://www.reddit.com/r/NiceHash/comments/7i0s6o/official_press_release_statement_by_nicehash/)

教训：

- payout 服务不能直接掌握无限额热钱包私钥。
- signer 必须验证“输出全部来自已批准 payout item”，而不是签任意交易。
- 单笔、单批、每日限额。
- 大额需双人审批。
- 冷、温、热钱包分层，热钱包只保留有限运营资金。
- 异常 outbound 立即全局冻结。

### 11.2 Slush Pool/Linode 热钱包事件

Braiins 回顾过早期 Slush Pool 热钱包因托管基础设施访问安全问题受到攻击的历史，这也是后来硬件钱包研发的背景之一。[Braiins/Slush Pool 回顾](https://medium.com/slushpool/how-trezor-was-born-from-a-hacking-attack-that-affected-slush-pool-trezor-security-a00ef053038c)

教训：

- 云主机权限不能等价于签名权限。
- 节点、payout engine、signer 必须隔离。
- 备份 seed 也不能裸放在同一基础设施。
- 基础设施管理员不应天然具备转币能力。

### 11.3 BTC.com 2022 网络攻击

运营方公开称客户资产约 70 万美元、公司资产约 230 万美元受影响；具体技术根因未完整公开，因此只能确定这是资产与安全域隔离失败类事故，不能断言具体漏洞。[BIT Mining 官方公告](https://www.prnewswire.com/news-releases/bit-mining-limited-subsidiary-experiences-cyberattack-301709947.html)

修法仍是 signer 隔离、限额、独立监控、热钱包最小化和异常 outbound 冻结。

### 11.4 MPOS E0078：daemon 已付款却返回 HTTP 500

这是最贴近状态机的问题。MPOS 文档明确将其视为可能造成双付的危险情况，并要求停止自动付款、人工核对。[MPOS Error Codes](https://github.com/MPOS/php-mpos/wiki/Error-Codes)

根因不是 HTTP 500 本身，而是：

```text
链上副作用已经发生
但调用者没有拿到成功结果
```

正确修法不是简单 retry，而是持久化 signed raw/txid 后再广播。

### 11.5 MPOS E0063：round/share 边界错误影响付款

MPOS 还记录过极快出块条件下，同一 upstream share 被错误关联到多个 block、需要重算受影响 round 的情况。[MPOS Error Codes](https://github.com/MPOS/php-mpos/wiki/Error-Codes)

教训：

- block reward 计算必须绑定不可变 share cutoff。
- share、block candidate、round assignment 必须有唯一关系或明确的多块处理规则。
- 重算不能覆盖旧余额，只能产生 adjustment ledger。
- 奖励计算错误最终会流入 payout，不能把 payout 当成孤立模块。

### 11.6 Monero 2018 multiple-counting bug

Monero 曾披露钱包 RPC 对特制交易可能重复计算 incoming amount，而实际 wallet balance 并未相应增加。依赖 RPC payment 列表自动入账的交易所或服务可能因此过度记账并允许提款。[Monero 官方 postmortem](https://web.getmonero.org/2018/09/05/a-post-mortum-of-the-multiple-counting-bug-2018-09-05.html)

教训：

- 不能只信某一个 `get_payments/get_transfers` 返回。
- 入账记录、实际余额增量、txid/输出唯一性必须交叉核对。
- wallet/daemon 版本要锁定并经过回归测试。
- 钱包升级后应重跑历史 reconciliation。

### 11.7 Poolin 2022 暂停提现

PoolinWallet 曾以保护资产、稳定流动性为由暂停提现。它不一定是 payout engine 重复付款事故，公开资料也不足以确定完整资产负债原因，因此不能过度推断。[PoolinWallet 官方公告](https://medium.com/@PoolinWalletOfficial/sep-5th-announcement-on-the-temporary-suspension-of-withdrawals-96a8915bcff0)

它说明：

- 账面资产不等于可用流动资产。
- immature、锁定、借出或难以及时变现的资产不能覆盖即时提款。
- 必须独立监控 liquidity coverage，而不仅是复式账是否平衡。

### 关于 Miningcore、yiimp、NOMP

这些项目及其 fork 版本差异极大，不能把某一 fork 的问题归因于整个项目。

但传统实现普遍需要重点审计：

- wallet RPC 和 DB 入账之间是否存在外部事务窗口。
- 是否使用一体化 `sendmany`。
- 是否先发币后扣余额。
- 是否只有当前余额表，没有不可变复式流水。
- 是否用语言原生浮点处理币值。
- 是否存在独立的 chain-to-book scanner。
- 重启后是否会重新执行旧 cron job。
- 一笔 batch 被拆成多笔交易后是否仍只保存一个 txid。

Miningcore 通常比早期 portal 在数据库事务和模块化方面更完整；yiimp/NOMP 的大量社区 fork 则差异很大。但具体版本是否已消除上述问题，必须逐版本、逐 coin handler 审计。**我不把这些架构风险说成某个当前版本已经发生过的已证实事故。**

---

最后压缩成 10 条不可妥协的规则：

1. 先在 DB 原子预留余额，再做任何钱包调用。
2. 签名 raw transaction 和 txid 必须先落库，后广播。
3. opaque `sendmany` 超时后绝不自动再调用。
4. 同一 raw 可重播，独立新交易不可作为“重试”。
5. 总账 append-only、复式、atomic integer，禁止浮点。
6. 地址使用不可变版本并绑定资产、网络和二进制目标。
7. 每个 spend wallet 只有一个带 fencing 的写者。
8. 同时运行 intent-to-chain 和 chain-to-book 双向对账。
9. 发现未知 outbound 或账不平，先冻结付款，不做自动平账。
10. 快速付款只能使用池自有、按币隔离的足额准备金。
