下面这份可以直接作为你们的架构验收标准、上线门禁和审计提纲。核心结论只有一句：

> 矿池不是一个 Stratum Server，而是一套“实时共识验证系统 + 金融账本 + 钱包支付系统 + 公网安全入口”；其中任何一层都不能靠下一层替它兜底。

# 1. 「多币矿池必须做对的事」总清单

## 【致命档：做错就是资金损失或整池瘫痪】

1. 钱包私钥必须与 Stratum、Web、数据库主机隔离，热钱包限额且所有出币只能经过唯一的打款引擎。
2. 每次打款必须先持久化唯一 payout intent，再构造、签名、广播，RPC 超时必须先查链上真相，禁止盲目重付。
3. 所有余额变化必须产生借贷平衡、不可变的复式 journal，任何业务代码都不得直接修改余额。
4. 每币必须持续执行“链上受控资产—内部资产账—矿工负债—运营方权益”对账，出现无法解释的资产短缺立即冻结该币打款。
5. 每个币上线前必须用共识金锚验证 PoW、target、端序、算法变体和边界值，不能把“节点接受了”当作全部验证。
6. block candidate 必须由池端独立重构和验证，客户端的“爆块”声明、hash、difficulty 和 reward 均不可信。
7. 每个块必须经历 canonical、confirmation、maturity、orphan/reorg 状态机，普通未成熟奖励不能进入可提现余额。
8. 只有 share 的验证结果和完整经济事件可靠持久化后，才能向矿工返回 `Share Accepted`。
9. 同一 share、block、settlement、payout 事件必须具有全局确定性 ID，重启、重放、并发只能产生一次经济效果。
10. 所有金额必须使用最小单位整数，所有 work 必须从服务端预先分配的整数 target 推导，核心账务禁止浮点数。
11. PPLNS 必须按累计 work 而不是 share 条数或时间计算，窗口边界、爆块 share、stale、冷启动和舍入规则必须固化。
12. PPS/FPPS 必须按币种隔离准备金、风险限额和 kill switch，没有足额同币准备金就不能承诺无条件 PPS。
13. subsidy、交易费、treasury、masternode、founder reward、burn、uncle、merged mining 必须按高度和实际链上输出解析，不能硬编码一个固定块奖励。
14. 每币至少有独立故障域和资源配额，某币节点卡死、验证 CPU 打满或 reorg 不能拖垮其他币。
15. 节点必须检测同步状态、canonical hash、chainwork 和版本升级，单节点返回“正常”不能作为最终链上真相。
16. 源站只允许受信 edge 访问，PROXY protocol 只接受受信转发机，autoban 永远不能封 trusted forwarder。
17. 所有公网入口、JSON、连接、日志、share queue、PoW validator、DB pool 都必须有硬上限，不能依靠内存自动扩张。
18. 超过接入带宽的 DDoS 必须由运营商、云 L4 或清洗服务处理，不能把本机 iptables 当作容量型防护。
19. 数据库备份只有完成异机恢复演练才算有效，必须能从 journal、链和事件日志重建余额。
20. 必须具备按币冻结结算、冻结打款、关闭 PPS、停止自动 ban、隔离节点和全局停止出币的独立 kill switch。
21. 生产配置、费率、结算算法、coin adapter 和地址规则必须版本化，任何修改都不能追溯改变历史事件。
22. 管理员、开发、客服、财务、签名服务必须最小权限分离，人工调账和出币必须留下双人审批与审计证据。

## 【重要档：做错会长期多付、少付或损害信誉】

1. 地址验证必须覆盖正确网络、checksum、prefix、大小写、integrated/subaddress、memo/payment ID、z-address 及明确负例。
2. Stratum 方言必须按实际 miner 测试，不能因为协议名称相同就假定 xmrig、T-Rex、lolMiner、ASIC 和 NiceHash 行为一致。
3. `extranonce1`、job ID、template epoch 和 nonce 空间必须跨实例唯一，防止多入口撞 nonce 和跨区 share 重放。
4. vardiff 必须按目标 share 间隔稳定调整，新 difficulty 的生效 job 和旧 job 宽限期必须明确。
5. stale、duplicate、low difficulty、badpow 必须有稳定且可审计的分类，不能因节点延迟或难度切换误伤矿工。
6. PPLNS 边界 share 应按比例切割，不能因 vardiff 粒度让某个高难度 share 在边界获得额外收益。
7. 每块原子单位余数必须使用确定性的守恒舍入，不能对每个矿工独立四舍五入。
8. 矿工算力、有效算力、付费算力、池算力、全网算力和 effort 必须使用不同口径，不能复用 PPLNS 窗口。
9. 同时维护 local candidate、node accepted、canonical block、mature reward 四层指标，才能区分运气、共识 bug 和漏块。
10. 钱包恢复必须扫描链上历史，而不是只恢复数据库中已知 txid。
11. payout fee 是池承担还是矿工承担、如何分摊、何时扣除必须写入结算合同。
12. orphan、deep reorg、错误多付和 fraud 应使用独立 debt receivable，不能只把矿工余额改成负数。
13. 矿工投诉时必须能从 share、job、窗口、舍入、journal 一路追到 payout output 和 txid。
14. accepted share、关键 job、block settlement、journal 和 payout manifest 必须按既定期限归档并具备防篡改证明。
15. 多转发机必须监控 backend 源端口、FD、conntrack、旧 worker 和长连接 timeout，不能只看 CPU。
16. DNS 切换不能当作无感故障迁移，必须让矿工配置 primary/backup，并验证实际固件的 failover 行为。
17. 发布新币、新算法、新节点版本前必须 shadow/canary，不能全量热切换。
18. 人工补偿、launch bonus、stale subsidy 和 goodwill refund 必须进入明确 expense account，不能伪装成正常矿工收益。
19. 每币必须有停服和退场规则，尤其要说明 PPLNS 窗口尾部、未达起付额余额和未成熟块如何处理。
20. 所有报警必须能关联具体 coin、policy version、node、gateway、job 和 journal batch，不能只有一条“pool error”。

## 【进阶档：做好是卖点，做不好暂不致命】

1. 为支持良好的矿工提供 TLS Stratum，并验证客户端确实校验证书，而不只是成功建立加密连接。
2. 对 Bitcoin/ASIC 业务逐步支持 BIP310、version rolling、AsicBoost 和 Stratum V2。
3. 发布每块 settlement manifest、Merkle root 和矿工个人 proof，提升外部可验证性。
4. 建设多区域 edge、托管 Anycast 和独立供应商入口，缩短区域攻击与故障恢复时间。
5. 对大矿场提供 mTLS/VPN、独立 quota、私有备用入口和 SLA。
6. 在准备金、风控和审计成熟后，为少数成熟币提供 capped PPS、PPS+ 或 FPPS。
7. 对低确认快速打款提供独立保险资金池、额度和定价，而不是混用运营余额。
8. 建设统计化 block-withholding、异常 effort 和租赁算力信誉模型。
9. 为隐私链实现自动 note/output consolidation、异步操作追踪和资金碎片健康度管理。
10. 提供每个算力指标的置信区间、gross/effective/paid 差异和公开 luck 解释。
11. 建设自动化链升级观察、共识差异检测和双实现交叉验证。
12. autoexchange、法币估值和跨币支付最后再做，并与原币账本彻底隔离。

---

# 2. 上线前自检 checklist

每个检查项应附：负责人、日期、软件版本、policy version、测试证据、复核人。不能只勾“完成”。

## Gate 0：币种共识合同冻结

### 链和节点

- [ ] 已固定 genesis hash、chain ID/network magic、默认端口和主网标识，测试网地址绝不会通过主网校验。
- [ ] 已确定支持的节点版本、最低版本、升级高度和废弃版本。
- [ ] 两台独立节点对 tip、height、best hash、chainwork、difficulty 和同步状态一致。
- [ ] 已验证节点处于 IBD、落后、分叉、RPC 半失效时，矿池会停止发新 job 或进入安全模式。
- [ ] 已记录 coinbase maturity、普通 finality、最大预期 reorg 和额外风控确认数。
- [ ] 已确认 subsidy、halving/tail emission、treasury、dev、masternode、burn、uncle、merged-mining 规则。
- [ ] 已确认节点 RPC 中 amount、fee、reward、difficulty 的单位和精度。

### 共识哈希金锚

- [ ] 至少有一组官方节点生成的真实 header/blob，其 PoW hash 与池端结果逐字节一致。
- [ ] 已验证算法 variant、height fork、seed hash、epoch、personalization 和 network-specific 参数。
- [ ] 已验证输入端序、输出端序、compact target 和 full target 转换。
- [ ] 已验证 `hash < target` 或 `hash ≤ target` 的准确共识语义。
- [ ] 已构造 target 以下、等于 target、target 以上的边界测试。
- [ ] 对金锚修改 nonce/header 任意一位后，池端会拒绝。
- [ ] share target、network target、difficulty、expected work 的往返转换无精度损失。
- [ ] diff1 constant 不是从 Bitcoin 复制来的错误常量。
- [ ] 高度跨越算法 fork 时，旧算法 share 被拒、新算法 share 被接受。

### 地址

- [ ] 普通地址、脚本地址、Bech32/Bech32m、subaddress、integrated address、z-address 等正例全部通过。
- [ ] checksum 错、长度错、prefix 错、测试网地址、主网相邻链地址等负例全部拒绝。
- [ ] 大小写、前后空格、Unicode 同形字符和不可见字符有明确处理。
- [ ] memo、payment ID、destination tag 等必填项缺失时会拒绝。
- [ ] 地址 canonicalization 不会把两个不同合法地址归并成一个。
- [ ] 已验证真实小额付款能被目标钱包识别和消费。

## Gate S：允许公开接收 share 之前

### 模板与 job

- [ ] `getblocktemplate` 或等价 RPC 的 height、prevhash、target、bits、version、transactions、coinbase value 均已映射。
- [ ] coinbase、merkle root、witness commitment、reserved offset、extranonce 区域和 mandatory outputs 正确。
- [ ] CryptoNote 的 blob、reserved offset、seed hash、major version 正确。
- [ ] Equihash 的 solution 长度、personalization、nonce 和 header 序列化正确。
- [ ] PIVX/PoS 混合链已确认 PoW 阶段、奖励结构和可能的共识特殊字段。
- [ ] job ID、template epoch、extranonce1 在重启、多实例、多区域下不会碰撞。
- [ ] 同 prevhash 模板刷新与 prevhash 改变能正确区分，`clean_jobs` 语义正确。
- [ ] 完整 block candidate 能由池端重构，并在 regtest/testnet 或受控环境被节点接受。
- [ ] 修改 coinbase、merkle、nonce、version 或 solution 的负例会被节点和池端拒绝。
- [ ] candidate 快速路径不会绕过普通 share 持久化、去重和审计。

### share

- [ ] share 使用服务端发 job 时固化的 target 计 work，不能按实际 hash 达到的最高 difficulty 事后提权。
- [ ] 返回 `Accepted` 前，share ID、job、target、work、结果和经济事件已可靠提交。
- [ ] 连接断开、ACK 丢失和重复 submit 不会重复计 work 或重复 credit。
- [ ] duplicate 能跨会话、重启和必要的多 region 范围识别。
- [ ] stale、low difficulty、badpow、invalid job、unauthorized 有稳定原因码和统计。
- [ ] vardiff 在目标 share 间隔下稳定，调整只在明确的新 job 生效。
- [ ] GPU 批量提交能被 bounded queue 吸收，不会被误判为攻击。
- [ ] RandomX/Equihash 等昂贵验证有独立 concurrency、queue 和 per-session budget。
- [ ] 伪 block candidate 不能触发无限节点 RPC 或优先队列耗尽。
- [ ] share ingestion、validator、block submitter 任一崩溃后重放结果一致。

### 协议兼容

- [ ] 对该币实际支持的 xmrig、SRBMiner、T-Rex、lolMiner、Gminer、BzMiner、cpuminer 或 ASIC 固件逐一连过。
- [ ] subscribe、authorize/login、job、submit、difficulty、error 和 reconnect 行为符合对应方言。
- [ ] worker 命名、`地址.worker`、rig ID、password 参数和固定难度已验证。
- [ ] xmrig-proxy/矿场代理一条连接承载多矿机时，nonce、统计和 vardiff 正确。
- [ ] NiceHash/MRR 的最低难度、extranonce、连接探测和重连已真实测试。
- [ ] 未知 method 会返回兼容错误，不会导致会话崩溃或无限日志。
- [ ] 长连接、无新块、job 重发和非 clean job 不会被前置 timeout 误踢。

### 网络与防护

- [ ] 源站 Stratum 只能从受信 edge/VPN 地址访问，公网无法绕过前置。
- [ ] HAProxy 向 backend 发送预期版本的 PROXY protocol，backend 强制要求该头。
- [ ] 池同时记录 `transport_peer_ip` 和 `verified_client_ip`，二者不会混用。
- [ ] 模拟坏矿工后只 ban 真实矿工 IP，不会 ban 转发机或 CGNAT 下全部矿工。
- [ ] trusted forwarder、节点、VPN、管理 CIDR 有不可绕过的 autoban 保护。
- [ ] 连接、握手、JSON、消息、字节、验证、日志和输出 buffer 均有硬上限。
- [ ] 上游 DDoS 服务确实支持原始 TCP Stratum，而不只是 HTTP WAF。
- [ ] 多 edge 的 backend 源端口、FD、conntrack 和最大长连接数已压测。
- [ ] TLS 入口和明文兼容入口完全分离，PROXY 与 TLS 解析顺序已测试。

## Gate A：允许产生正式矿工收益之前

### PPLNS/SOLO/PPS

- [ ] settlement scheme 在 job/share 产生时固化，SOLO share 不可能进入 PPS/PPLNS credit 路径。
- [ ] PPLNS 使用累计标准化 work，不使用 share 条数、墙钟时间或客户端自报算力。
- [ ] 爆块 share 是否参与自己块、窗口端点和边界切割已冻结为 policy。
- [ ] 连续两个、三个快速块的重叠窗口有金锚结果。
- [ ] 一个高 difficulty share 跨窗口边界时能按比例切割。
- [ ] 冷启动窗口不满时，launch bonus 或 bootstrap reserve 的结果正确。
- [ ] 真正 stale 不计 PPLNS，模板刷新但 prevhash 未变的 share 不被误拒。
- [ ] pool fee、mandatory recipients、miner distributable 的原子单位守恒。
- [ ] largest remainder 或 balanced rounding 的结果确定且合计严格等于 distributable。
- [ ] 相同原始事件重算多次，allocation 和 journal ID 完全相同。
- [ ] PPS 的 share rate 使用提交时 subsidy、network target 和 FPPS rate version。
- [ ] 若启用 PPS，准备金、最大算力、最大日 liability、自动降级和 kill switch 已验证。

### 块生命周期

- [ ] found、submitted、node-accepted、canonical、confirmed、mature、orphan、reorg 状态可完整迁移。
- [ ] 同高度两个候选块只会让最终 canonical 块产生最终收益。
- [ ] 普通 orphan 能完整冲销 pending，但不会删除历史 journal。
- [ ] mature 前节点重启、丢 ZMQ、RPC 超时后，恢复扫描能找回块状态。
- [ ] 深 reorg 演练能冻结该币、重算 settlement、形成 debt/损失并完成对账。
- [ ] coinbase maturity 不存在 off-by-one。
- [ ] local candidate、node accepted、canonical、mature reward 指标能独立比较。
- [ ] 节点升级或 hard fork 前后都通过对应高度的共识金锚。

### 复式账本

- [ ] 每个 journal batch 借方等于贷方，差 1 个最小单位也不能提交。
- [ ] balance 能从零开始仅依靠 journal 重建。
- [ ] 人工调整必须有 correction/expense/receivable 对方账户，不能直接改余额。
- [ ] pending、available、held、payout-in-transit、paid、debt 是不同账户或明确状态。
- [ ] 每币资产、负债、权益完全隔离，法币估值不会改变原币数量。
- [ ] pool reserve 是有真实资产支持的权益，不是数据库中的虚拟数字。
- [ ] 随机抽取正常块、快速重叠块和 orphan 块，都能从原始 share 重算到相同 journal。
- [ ] 每块 settlement manifest 包含 policy、窗口、边界、fee、rounding 和 journal batch。

## Gate P：允许真实打款之前

### 钱包和签名

- [ ] 热钱包只持有限定运营额度，其余资产位于独立冷钱包或受控签名系统。
- [ ] Stratum、Web、数据库主机没有钱包私钥和签名权限。
- [ ] signer 只接受经过策略检查、带唯一 payout intent 的请求。
- [ ] 单笔、单批、单日、单币限额和双人审批阈值已生效。
- [ ] 钱包锁、nonce、UTXO reservation、change、note/output selection 在重启后可恢复。
- [ ] 钱包备份和密钥恢复已在隔离环境真实演练。

### 打款状态机

- [ ] 到达起付额只创建候选，不会直接调用发送 RPC。
- [ ] payout intent 在任何签名或广播前持久化。
- [ ] 构造、签名、广播、mempool、确认、final 状态分别落库。
- [ ] 同一 payout intent 无论任务重入多少次都只能对应一个经济付款。
- [ ] 在“节点已广播但 RPC 超时”的注入测试中，不会自动创建第二笔付款。
- [ ] 能通过 txid、inputs/nonce、outputs、memo 和 intent fingerprint 搜索未知交易。
- [ ] 余额不足、手续费估算失败、非法地址、节点拒绝分别进入正确可重试/不可重试状态。
- [ ] 批量交易中一个地址非法时整批不签名，不存在“部分输出其实成功”的错误假设。
- [ ] mempool 驱逐、RBF、nonce replacement、交易冲突和节点重启有明确恢复流程。
- [ ] 打款失败且确定未上链时，payout-in-transit 能幂等退回 available。
- [ ] 达到确认条件前，矿工总负债不会因为广播成功而提前消失。
- [ ] 矿工承担或池承担 network fee 的分录正确且不会重复扣费。

### 链上和守恒

- [ ] 能从链上枚举池控制的 spendable、encumbered、immature、pending 和 conflicted 资产。
- [ ] 钱包 RPC 数字已与原始 UTXO/account/note inventory 交叉验证。
- [ ] 创建批次前和确认后都执行资产负债对账。
- [ ] payout principal、network fee、change 与交易实际输入输出完全匹配。
- [ ] 未知外部入账进入 suspense/refund payable，不会被当作 pool profit。
- [ ] 普通孤块、深 reorg、错误多付和 miner debt 的分录均已演练。
- [ ] 恢复扫描能从空业务缓存重建所有未完成 payout 状态。
- [ ] 任意无法解释的资产 delta 会自动冻结该币后续打款。

## Gate O：允许无人值守自动运营之前

- [ ] 已在每个事务边界注入进程崩溃，确认重启不会漏记或重复经济效果。
- [ ] 已演练数据库主库故障、消息重复、消息延迟和消费者重启。
- [ ] 已演练全部节点不可用、节点分叉、节点落后和错误 RPC 数据。
- [ ] 已演练单 edge、单 region 和 backend gateway 故障。
- [ ] 已完成从备份恢复数据库，再由链和 journal 对账的全流程。
- [ ] 已验证 NTP 异常不会改变事件顺序、到期时间或 settlement 结果。
- [ ] 所有 kill switch 已由非开发人员按 runbook 操作过。
- [ ] 报警覆盖资产差异、journal 不平、异常 ban、candidate 漏斗、reorg、钱包余额、validator queue 和 payout unknown。
- [ ] 首批真实付款采用低限额、人工复核，并逐输出链上确认。
- [ ] 连续完成至少一个完整成熟周期、一次 payout cycle 和一次恢复演练后，才提高限额。
- [ ] 上线审批由 coin owner、账务负责人、安全负责人和钱包负责人共同签字。

---

# 3. 只能刻进核心的 10 条 Invariant

1. **没有通过当前 job、session、assigned target、PoW 和 network target 的完整验证，并完成可靠持久化，就绝不能返回 `Share Accepted`。**

2. **同一个逻辑 share、block、settlement、journal event 或 payout intent，在全系统中最多产生一次经济效果。**

3. **work 只能由服务端预先分配的整数 target 推导，金额只能使用最小单位整数，核心账务禁止浮点数。**

4. **每一笔余额变化都必须对应借贷相等的不可变 journal，余额只是 journal 的可重建投影。**

5. **每次结算必须绑定不可变的 coin/policy version，PPLNS 只按累计 work 计算，分配总额必须与可分资产严格守恒。**

6. **矿工可提现负债必须由成熟 canonical 资产、明确的运营方补贴或足额隔离的 PPS 准备金支持。**

7. **块状态只能由 canonical hash、chainwork/finality 和成熟规则决定，orphan/reorg 只能通过补偿分录处理，禁止删除历史。**

8. **任何出币都必须先有唯一、持久化的 payout intent；RPC 返回未知时，查清链上真相之前禁止重付。**

9. **链上受控资产与资产账出现任何无法解释的差异时，受影响币种必须自动停止结算最终化和打款。**

10. **任何外部输入都不可信且资源消耗必须有界：PROXY 地址只信受信 edge、trusted forwarder 永不 autoban、连接与验证队列永不无界。**

---

# 4. 合理实施顺序和里程碑

## M0：先写合同，不写矿池功能

必须第一天锁定：

- 原子金额、work 和 target 类型；
- 全局事件 ID、幂等规则；
- append-only journal 和科目表；
- share、block、settlement、payout 状态机；
- coin/policy/config version；
- 每币命名空间和故障隔离；
- 证据保留模型；
- edge IP 信任模型；
- 钱包和签名权限边界。

退出条件：团队可以只看规格，明确回答“任意崩溃点之后如何恢复、会不会重复付钱”。

## M1：只做一个最简单币的完整纵切

选择一个规则稳定、节点成熟的 Bitcoin-like 币，在 regtest/testnet 完成：

> template → Stratum → share → block candidate → node accept → maturity

此时不做十币抽象、不做 PPS、不做自动打款。

退出条件：共识金锚、share 幂等、candidate 重构和 block lifecycle 全部通过。

## M2：完成账务核心

实现：

- 复式 journal；
- PPLNS/SOLO；
- 原子舍入；
- pending/available；
- orphan/reorg 补偿；
- balance 重建；
- 每块 settlement manifest。

退出条件：正常块、连续块、冷启动、边界 share、orphan、deep reorg 均可重复重算且守恒。

## M3：完成钱包和打款闭环

实现：

- 钱包 gateway；
- 隔离 signer；
- payout intent；
- 构造/签名/广播拆步；
- RPC unknown reconciliation；
- 链扫描；
- wallet/ledger 对账；
- 限额和审批。

退出条件：在每个状态转换前后强制崩溃，均不会重复付款或丢失矿工负债。

## M4：生产安全和可恢复性

实现：

- HAProxy + PROXY v2；
- 源站 ACL；
- 真实 IP 模型；
- rate limit/autoban；
- validator 隔离；
- bounded queue；
- node/DB/edge HA；
- 备份恢复；
- 监控、报警、kill switch。

退出条件：一个坏矿工、一个坏币、一个坏节点和一个坏 edge 均不能影响其他币或导致全池打款停止。

## M5：协议兼容矩阵

逐个接入真实矿工：

- CPU/GPU miner；
- proxy；
- ASIC 固件；
- NiceHash/MRR；
- TLS；
- vardiff；
- reconnect/failover。

退出条件：每个宣称支持的 miner/version 都有录制交互、回归用例和明确例外。

## M6：第一个币小额 canary

上线策略：

- 初期只 PPLNS/SOLO；
- 限制在线算力和热钱包余额；
- 首批打款人工批准；
- 每块人工抽查 settlement；
- 每次打款链上逐输出核对；
- 每日完整 close。

退出条件：至少跑完多个成熟周期、真实 payout、节点重启、服务重启和一次恢复演练。

## M7：从“一个币”提炼 coin adapter

先接第二个明显不同的币族，再抽象接口：

- Bitcoin-like；
- CryptoNote；
- Equihash/Zcash；
- PIVX/特殊奖励；
- 自定义 RPC。

原则：

> 只有两个真实实现都需要的稳定差异才进入核心接口；某币的一次性怪癖留在 adapter。

退出条件：新增币只增加 adapter、配置、金锚和测试，不修改账务、打款、入口核心。

## M8：多币规模化

增加：

- per-coin resource quota；
- 独立 node/wallet；
- 多区域 edge；
- 自动 payout；
- ban 控制面；
- 审计 manifest；
- 容量和成本管理。

退出条件：任意币暂停、重启、reorg、钱包维护都不影响其他币。

## M9：最后做商业增强

可以后补：

- PPS/FPPS；
- 低确认快速打款；
- Stratum V2；
- Anycast；
- public settlement proof；
- autoexchange；
- 跨币支付；
- 高级 BWH 检测；
- 移动端和精美 dashboard。

明确不能后补、后期几乎改不动的东西：

- 原子单位和 work 类型；
- 事件身份与幂等；
- journal 模型；
- share/block/payout 状态机；
- policy version；
- per-coin 隔离；
- 钱包权限边界；
- PROXY 真实 IP 信任边界；
- 原始证据与留存；
- 链上资产对账模型。

---

# 5. 审计时优先检查的 5 个地方

## 1. 资产到底够不够

我会独立枚举每个币的链上受控资产，再与：

- immature；
- spendable；
- encumbered；
- miner pending；
- miner available；
- payout-in-transit；
- refund；
- debt；
- operator equity

逐项核对。

一票否决红旗：

- 只拿钱包 `getbalance` 减矿工余额；
- 多币用法币价值合并看“总体没亏”；
- 把 immature 或未确认 change 当可用资金；
- 对不上但仍继续打款；
- reserve 只是数据库数字，没有隔离资产。

## 2. 打款遇到“未知结果”时怎么做

我会选一笔真实 payout，在广播前后分别模拟进程崩溃和 RPC timeout。

一票否决红旗：

- 定时任务根据余额重新生成付款；
- 没有持久化 payout intent；
- `sendmany` 超时后直接再调用一次；
- 广播成功就扣清矿工负债；
- 只按 txid 查交易，不会按 inputs/nonce/outputs 恢复；
- 开发或客服能直接在节点控制台打钱。

## 3. 任取一个块能不能从 share 重算到账

我会随机选择：

- 一个普通块；
- 一个连续快速块；
- 一个跨 vardiff 边界的块；
- 一个 orphan/reorg 块。

然后从原始 share 独立重算窗口、work、fee、rounding 和 journal。

一票否决红旗：

- 按 share 条数计算；
- settlement 依赖当前配置而不是历史 policy；
- 原始 share 已删且没有 manifest；
- balance 对得上，但无法解释形成过程；
- 修复方式是直接 UPDATE balance；
- 重跑 settlement 会再次 credit。

## 4. coin adapter 是否真的理解共识

我会检查：

- PoW 金锚；
- target 边界；
- fork 高度；
- reward 输出；
- maturity；
- candidate 重构；
- invalid/orphan/reorg 分类；
- 节点版本和升级流程。

一票否决红旗：

- 主要逻辑从另一个币复制，只改 symbol 和端口；
- `getblocktemplate` 的 coinbasevalue 被直接当作最终资产；
- 只用一个节点；
- 节点接受 block 就认为一定 canonical；
- 没有算法 fork 前后的测试向量；
- effort 长期异常但解释为“运气不好”。

## 5. 一次故障能炸多大

我会主动制造：

- 一个矿工垃圾 share flood；
- 一台节点挂死；
- 一币 reorg；
- Redis/DB/message bus 短暂失败；
- 一台 HAProxy 下线；
- 一条错误 autoban 规则；
- 一个钱包 RPC 卡住。

一票否决红旗：

- 所有币在一个无隔离进程或共享无界队列；
- 一个币节点超时拖住全局 event loop；
- 池看到的 IP 全是转发机；
- autoban 能封 trusted proxy；
- reputation/Redis 故障导致全部矿工拒绝；
- 没有按币冻结和全局停止出币开关；
- 备份从未恢复过；
- 报警只说“系统异常”，无法定位 coin/job/policy/journal。

最终判断标准不是“现在有没有出过事故”，而是：

> 任意输入重放不会多记账，任意进程崩溃不会多付款，任意孤块不会制造无来源资产，任意单币故障不会扩散，任意账目数字都能从不可变证据重算出来。
