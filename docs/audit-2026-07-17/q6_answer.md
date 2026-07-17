整体建议：把这一层设计成“能力抽象 + 币种共识插件”，不要试图用一套通用字段解释所有链。通知只作为提示，节点查询才是事实来源；模板、提交结果、最终性都必须携带节点来源和链状态快照。

## 1. NodeAdapter / WalletAdapter / Notifier 怎么切

三接口分离是合理的，但 `NodeAdapter` 最好内部再按能力拆分：

- `TemplateProvider`：取得模板、长轮询模板、校验模板能力。
- `BlockSubmitter`：提交完全序列化的候选块。
- `ChainReader`：tip、height、block hash、header、chainwork、主链归属、同步状态。
- `WalletAdapter`：地址、余额、构造/签名/发送付款、查询交易和解锁状态。
- `Notifier`：只发 `NewTip`、`PossibleReorg`、`MempoolChanged`、连接断档等事件提示。

关键边界：

- `Notifier` 不是事实来源。收到事件后，必须经 `ChainReader` 重新确认。
- `WalletAdapter` 不能作为区块最终性来源。
- `sendmany` 虽然可能与 Bitcoin Core 共用 RPC endpoint，逻辑上仍属于 `WalletAdapter`。
- 如果 QUIC push 直接推送完整模板，模板内容属于 `TemplateProvider` 的流式实现；QUIC 只是传输协议，不应因为是 push 就全部塞进 `Notifier`。
- 每个模板必须附带来源信息：`node_id、tip_hash、height、chainwork、rules_fingerprint、template_id/longpollid、received_at`。
- 每次提交保存候选块的完整 bytes 和预计算 block ID，不能只保存模板 ID。

五种形态可这样映射：

| 节点形态 | NodeAdapter | WalletAdapter | Notifier |
|---|---|---|---|
| Bitcoin RPC | GBT、submitblock、链查询 | wallet RPC / sendmany | ZMQ、longpoll |
| 门罗系 | daemon RPC | 独立 wallet-rpc | daemon 通知或轮询 |
| 自定义 REST | REST 实现 | REST 或独立钱包 | WebSocket/SSE/轮询 |
| 内嵌节点 | Go 函数调用 | 内嵌或独立钱包 | 进程内 callback |
| QUIC push | QUIC request/stream | 独立安全通道 | QUIC event stream |

适配层启动时应做 capability negotiation，而不是按币种名称猜能力。

## 2. 多节点健康、failover 和状态一致性

健康检查至少分成四层：

1. 传输健康：连接、超时率、RPC 错误率、延迟。
2. 同步健康：是否 IBD、tip 时间是否新鲜、verification progress、peer 数。
3. 共识健康：genesis/network、客户端版本、fork 配置、规则指纹。
4. 链视图健康：height、tip hash、chainwork、最近祖先是否一致。

实际策略：

- 选择一个 sticky primary 作为模板节点，避免频繁切换。
- standby 只有在与 primary 处于同一 tip、相同规则指纹时，才可立即接管。
- 观察节点只负责发现差异和触发 reconciliation，不能直接用它的状态修改另一个节点产生的模板。
- failover 不应只看最高 height，应优先看兼容节点中的有效 chainwork、已知 checkpoint 和同步状态。
- 节点恢复后连续成功若干次再重新加入，避免健康状态抖动。

“从 A 拿模板、用 B 的状态解释”危险，是因为模板是一个原子快照，里面隐含：

- `previousblockhash`
- height、bits/target、MTP 和 nTime 约束
- fork/versionbits 状态
- mandatory payouts
- 交易集合和费用
- mempool 视图

B 即使 height 相同，也可能位于同高度的另一条分支，或启用了不同 fork 规则。不能用 B 的 `coinbasevalue`、target、主链判断去修补 A 的模板。

tip 不一致时：

- 正在同步、tip 过旧的节点排除出模板集合。
- 旧共识版本节点进入隔离状态，不参与模板或提交判定。
- 不要简单多数表决：多数节点可能同时跑旧版本。
- 若兼容节点之间无法确定 canonical cohort，应暂停发新 job 并告警，而不是猜一条链继续挖。
- 自定义链的版本兼容和 fork 指纹定义属于“不确定（币种相关）”，应由该链插件显式提供。

## 3. 爆块提交与超时

应向同一 canonical cohort 中的所有健康节点并发提交同一份、逐字节完全相同的候选块，因为这样可以：

- 降低单节点 RPC 卡顿造成的传播延迟。
- 避免 primary 与网络连接不佳。
- 让多个节点尽快向各自 P2P 邻居广播。
- 提高超时情况下的可观测性。

统一提交结果不要只用成功/失败，而应归一化成：

- `ACCEPTED`
- `ALREADY_KNOWN`
- `DEFINITIVE_INVALID`
- `STALE_OR_SIDECHAIN`
- `TRANSIENT_ERROR`
- `UNKNOWN`

Bitcoin Core 通常以 `null` 表示接受，以字符串表示 reject reason；很多分叉币会返回 Boolean、对象、非标准字符串，甚至 HTTP 成功但业务失败。因此每个币种必须有独立映射表。无法识别的返回值归为 `UNKNOWN`，不能擅自判失败。

“到底进没进链”应分三级：

1. 节点接受或报告 duplicate：节点至少认识该块。
2. 能按 block ID 查询到：块已进入节点的 block index。
3. `getblockhash(height) == submitted_block_id`，且 tip 是它的后代：它当前在 active chain。

`submitblock` 超时时：

- 先把状态记为 `UNKNOWN`。
- 立即查询该 block ID。
- 可以向其他节点重提完全相同的 bytes。
- 可以稍后向同一节点重提完全相同的 bytes。
- 绝不能把超时解释成无效，然后为同一份找到的工作修改 coinbase/nTime，提交一个不同块来“重试”。

池可以马上切换矿工到最新 tip 继续工作，但原候选块必须在后台继续追踪。

## 4. 出块最终性判定

不能只信 `submitblock`，因为它只说明节点处理了提交；节点可能接受为 side chain，或者随后立即 reorg。

不能只信 wallet confirmations，因为钱包可能：

- 连接的是另一节点。
- 索引滞后。
- 尚未扫描到 coinbase。
- 在 reorg 后暂时显示旧值。
- 把“已在链上”和“已解锁可花”混成一个字段。

最可靠的通用 PoW 判据是：

- 保存提交时计算出的协议级 block ID。
- 在预期高度查询 active-chain block hash。
- 验证 `getblockhash(h) == submitted_block_id`。
- 验证当前 tip 的祖先链包含它。
- 到 coinbase maturity 后再检查一次，并增加运营安全余量。

这里应比较协议定义的 block ID/hash，而不是要求 RPC 返回的整块序列化结果逐字节相同。比较完整 raw block 通常没有额外收益，还可能受 RPC 表示方式影响。

状态机建议：

`SUBMITTED → KNOWN_BY_NODE → CANONICAL → MATURE → PAYABLE`

旁路状态包括：

`UNKNOWN、REJECTED、ORPHANED、REORGED_OUT`

对纯 PoW 链，maturity 不是绝对最终性，只是重组概率较低。如果链提供 finalized checkpoint/BFT finality，则“finalized hash 包含本块”比 confirmations 更强。该能力属于“不确定（币种相关）”。

## 5. 新块通知延迟

大致特征如下，实际延迟受部署和网络影响：

- 进程内 callback：通常最低，可到微秒至毫秒级；不能在 callback 内做 RPC 或模板构造，应只投递到有界队列。
- ZMQ：本机通常是毫秒级；无持久重放，订阅断线、HWM 溢出、进程重启都可能丢事件。
- GBT longpoll：有新模板时也可很快返回；坑是代理超时、连接假死、节点重启和 longpollid 处理错误。
- P2P 直连监听：可能比 RPC 通知更早看到 header/inv；但必须防伪造 tip、eclipse 和仅收到 header 未完成验证。
- 短轮询：最可靠但延迟下界约为轮询周期，且会增加 RPC 压力。
- QUIC push：低延迟且可带 sequence number；仍要处理断线区间和重放。

必须“推送 + 轮询”双通道：

- 推送负责低延迟。
- 轮询负责补丢、启动同步、断线恢复和验证推送真实性。
- 推送连接恢复后，应报告最后 sequence，并立即主动读取 tip，不要假设期间没有事件。

按 `(height, hash)` 去重，而不是只按 height：

- 同高度不同 hash 可能正是 reorg。
- 同一 hash 可能从 ZMQ、P2P、轮询等多次到达。
- 如果事件没有 height，先查 header 补齐。

本池提交的块被接受也必须触发 job 刷新，因为本池通常比外部 ZMQ/P2P 更早知道潜在新 tip。接受后应立即：

1. 停止继续发旧 `prevhash` 的 job。
2. 向模板节点请求新模板。
3. 把提交事件与稍后的链通知去重。
4. 若最终未成为 active tip，再按实际 canonical tip 重新发 job。

## 6. 模板归一化和强制校验

建议做三层校验。

第一层：结构和范围

- network/genesis 是否正确。
- `previousblockhash、height、version、bits、target、curtime/mintime` 是否齐全且合理。
- 所有交易能否解码，依赖关系是否正确。
- 模板是否过期、来源节点是否仍处于同一 tip。

第二层：区块内部一致性

- coinbase 编码、高度承诺、extranonce 空间和 coinbase 大小。
- Merkle root、字节序、txid/wtxid 使用是否正确。
- block size、weight、sigops 限制。
- nTime 是否满足 MTP、模板时间范围和允许的 time rolling。
- versionbits 不得被矿工 version rolling 意外清除。

第三层：币种共识规则

- coinbase 最大允许金额。
- SegWit witness commitment 和 coinbase witness reserved value。
- treasury、masternode、founder、dev fee 等强制输出的金额、脚本和顺序。
- fork 激活高度、网络升级版本、AuxPoW 等扩展规则。
- 交易费、区块权重惩罚等链特有计算。

mandatory payout 的字段名、取整方式、输出顺序在分叉币之间差异极大，属于“不确定（币种相关）”；不能做通用猜测。插件不认识激活规则或必付字段时，应拒绝发模板。

如果节点支持 GBT proposal mode，可在发给矿工前，把池构造后的候选结构送给一个或多个兼容节点预校验。它不能替代池端校验，但能抓住 coinbase、commitment 和 fork 规则错误。

空块处理：

- 节点已同步，只是新 tip 后 mempool 模板尚未准备好：可以短暂发 coinbase-only job，降低空窗期。
- 随后取得完整模板，立即切换。
- 节点本身未同步或 mempool 异常：不能把空模板当正常模板。
- 不要把旧 tip 的交易集合直接搬到新 tip 上。

Bitcoin 风格中，coinbase 最多 claim 模板对应的 subsidy 加实际纳入交易的 fees；少 claim 通常只是销毁差额，多 claim 会被拒绝。若删除模板交易，就必须同步扣除相应 fee，并遵守 `mutable` 规则。强制支付通常包含在 coinbase 总额内，池只能领取扣除强制输出后的部分。门罗系奖励还可能受 block weight penalty 影响，必须采用 daemon 提供的算法或模板，不应套 Bitcoin 计算。

## 7. RPC 超时、重试和认证

不要设置一个全局超时。可用以下值作为 LAN 环境起点，再按 p99 调整：

- connect：`0.5–2 秒`
- tip/header 等轻查询：`2–5 秒`
- 普通 GBT：`5–15 秒`
- submitblock：`10–30 秒`，验证慢的链可到 `60 秒`
- longpoll：独立长连接，允许数分钟后主动重连
- sendmany/钱包选币签名：`60–300 秒`

重试原则：

- tip、header、block 查询：幂等，可带 jitter 重试或切换节点。
- GBT：可重取，但新结果是新快照，不能与旧模板字段混用。
- submitblock：只能重提完全相同的 bytes。
- 广播已经持久化的同一 raw transaction：通常可安全重播。
- `sendmany` 这类“创建并发送”复合操作：超时结果不明，不能盲目重试，否则可能双付。

付款最好拆成：

1. 以内部 payout operation ID 建账。
2. 构造并签名。
3. 持久化 txid 和 raw transaction。
4. 广播同一 raw transaction。
5. 按 txid 对账。

如果只能使用 `sendmany`，必须串行化付款，并在超时后先检查 wallet transaction/history 再决定是否重试。

安全落地：

- RPC 只绑定 localhost、Unix socket 或私网地址。
- 跨主机使用专用 VLAN/VPN、TLS/mTLS 和防火墙 allowlist。
- Bitcoin Core 优先 cookie 或 `rpcauth`，不要硬编码明文密码。
- Digest auth 是否支持取决于具体节点，属于“不确定（实现相关）”。
- 反向代理补认证可以用，但代理必须校验池端 mTLS、移除客户端伪造的认证 header，并成为唯一可访问上游 RPC 的主体。
- 模板读取、区块提交、钱包付款应使用不同凭证和网络权限。
- 日志禁止记录 Authorization、wallet passphrase 和完整敏感请求。
- 钱包 RPC 尤其不能与节点读 RPC 一起暴露公网。

## 8. 无 RPC / 内嵌节点的特有问题

直接 import Go 链库减少序列化和通知延迟，但把节点故障域并入了矿池：

- 链库的 panic、死锁、OOM、数据竞争会拖垮整个池。
- 全局 singleton、全局网络参数、多链同时运行经常冲突。
- callback 可能持有链状态锁；池端在 callback 里反查链状态容易死锁。
- 节点同步和模板构造抢占 CPU、内存、GC。
- 链库升级可能改变数据库格式、共识默认值或 API 语义。
- 内存中的 tip 与已落盘 chainstate 可能不一致。

落地要求：

- callback 只复制最小事件并入队，不能重入链库。
- 启动前校验 network、genesis、checkpoint、数据库 schema 和共识参数。
- 只在 chainstate 完成恢复并追到可接受新鲜度后启用挖矿。
- chainstate、tip、undo 数据采用原子提交或 WAL。
- 定期生成带校验的 snapshot/checkpoint。
- 崩溃恢复从最近可信 checkpoint 加增量日志重放，不能每次全链重验。
- 保留足够 reorg undo 数据，超过深度则进入隔离恢复流程。
- panic recover 只能保护局部 goroutine，无法证明共享状态未损坏；严重异常应让 watchdog 重启并重新做完整一致性检查。

如果可靠性比极限延迟更重要，即便没有传统 RPC，也可把链库放入独立 sidecar，通过本机 IPC 隔离故障。

## 9. reorg 检测和入账回滚

以下任一情况都应触发 reorg reconciliation：

- 新 tip 的 parent 不是本地已知 tip。
- 同一 height 出现不同 hash。
- 已记录高度的 `getblockhash` 改变。
- 已 canonical 的本池块 confirmations 变为负数、零或查不到。
- 节点切换后 canonical view 不同。
- 通知 sequence 出现断档。

处理流程：

1. 保存最近一段 canonical header/hash 环形历史。
2. 从新旧 tip 向后寻找 common ancestor。
3. 得到 detached 区间和 attached 区间。
4. 立即使旧 tip 的所有 mining job 失效。
5. 重新检查每个本池候选块的 canonical membership。
6. 对 detached 块写补偿账，不要删除原账。
7. 重新计算 maturity 和可付款余额。

账务应使用 append-only ledger：

- 未成熟奖励先记 `tentative/immature`。
- reorg 后写 reversal/orphan entry。
- PPLNS/PROP 是否撤销矿工余额取决于池规则。
- PPS 通常由池承担 orphan 风险，不应把已承诺的 PPS 收益反向扣给矿工。
- 若发生超过 maturity 的深 reorg，使用风险准备金或负债账户处理，并暂停付款告警。

寻找 common ancestor 超过本地保留深度时，不要猜测；暂停入账，从可信节点重新同步一段 header/block index。

## 10. 常见著名坑

最常见的事故模式包括：

- 把 JSON-RPC HTTP 200 当成 submitblock 接受。
- 把 `null`、空字符串、Boolean 和 reject string 的语义在分叉币间照搬。
- submitblock 超时后生成不同 coinbase 再提交，造成状态无法对账。
- 只看 wallet confirmations，错过钱包索引滞后或 reorg。
- ZMQ 断线后没有轮询补洞，矿工持续挖旧 `prevhash`。
- longpoll 连接假死，没有 deadline、重连和备用轮询。
- 只按 height 去重通知，吞掉同高度换 hash 的 reorg。
- 本池爆块后等待外部通知，没有主动刷新 job。
- SegWit 下混淆 txid 和 wtxid，算错 Merkle root 或 witness commitment。
- bits、target、difficulty 和端序互相误用。
- 忘记 BIP34 高度、coinbase flags、coinbase 大小限制或 extranonce push 编码。
- version rolling 覆盖节点要求的 versionbits。
- 把 block bytes 限制当 block weight 限制，或误解 sigops 单位。
- 模板删除交易后仍领取原 `coinbasevalue`。
- 漏掉 treasury/masternode/founder 输出，或者金额正确但脚本、顺序、取整错误。
- 在 fork 激活边界继续使用缓存模板。
- 用同步落后的节点做 failover，仅因为它 RPC 延迟更低。
- 多节点同高度就认为一致，没有比较 hash、chainwork 和规则版本。
- P2P 监听一收到 header 就当成已验证新 tip，受到伪造或无效链干扰。
- 内嵌节点 callback 中反向调用持锁 API，造成死锁。
- crash 后只恢复 height，不恢复对应 hash、chainstate 和 undo 状态。

最重要的三条工程不变量是：模板必须带来源快照；提交必须保存完全相同的候选块 bytes；任何最终性和入账判断都必须基于 canonical-chain membership，而不是某一个 RPC 返回字段。