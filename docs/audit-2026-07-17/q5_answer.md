先给总原则：矿池账务必须同时维护 3 套互相独立、能够交叉验证的事实，不能用一张 `balances` 表包打天下：

1. **工作事实**：哪些 share 在什么 target、job、顺序下被接受，代表多少 work。
2. **经济合同**：PPLNS/PPS/SOLO 如何把 work 转成债权。
3. **资产事实**：链上实际控制多少币，哪些 immature、spendable、in-flight、orphan。

我的总体建议是：

- 第一版采用“按累计标准化 work 的 PPLNS + SOLO”。
- 所有金额只用最小单位整数，所有比例使用有理数或定点数。
- 每币独立复式子账本，禁止跨币直接轧差。
- 普通孤块只能冲销 `pending`，矿工可提现余额最好等 coinbase maturity。
- 初期不要给十几个小众币一起上 PPS；PPS 应当逐币审批、逐币准备金。
- 内部账不平或链上资产出现无法解释的短缺时，立即冻结受影响币种的结算和打款，但矿工接入与 share 收集可以继续进入隔离状态。

---

# 1. PPLNS 的完整数学合同

## 1.1 不要把 N 定义成“share 条数”

设某 share 的池端 assigned target 为 \(T^s_i\)，当时网络 target 为 \(T^n_i\)，PoW 输出空间大小为 \(Q\)，例如 256-bit PoW 下 \(Q=2^{256}\)。

若共识规则为 `hash ≤ target`，则：

\[
p(T)=\frac{T+1}{Q}
\]

一次成功 share 所代表的期望哈希工作量为：

\[
w_i=\frac{1}{p(T^s_i)}=\frac{Q}{T^s_i+1}
\]

当时找到一个网络块所需的期望工作量为：

\[
W_i=\frac{Q}{T^n_i+1}
\]

把 share 标准化为“网络块等价 work”：

\[
u_i=\frac{w_i}{W_i}
=\frac{T^n_i+1}{T^s_i+1}
\approx\frac{d^{share}_i}{d^{network}_i}
\]

这里的 \(u_i\) 是推荐的 PPLNS 窗口单位。

例如：

- 一个 difficulty 1 share：权重为 1；
- 一个 difficulty 100 share：权重为 100；
- 不能因为二者各占一行数据库就各算一票。

Rosenfeld 对变难度 share 的分析也明确指出，每个 share 必须按自身预先分配的 difficulty 计分，并且 difficulty 必须在给矿工发工作前确定，不能根据矿工最后提交的 hash“事后挑最高 difficulty”。[Analysis of Bitcoin Pooled Mining Reward Systems](https://arxiv.org/abs/1112.4980)

### 为什么时间窗口也不对

按最近 10 分钟分配会出现：

- 池算力高时，10 分钟内积累的 work 更多，每单位 work 收益反而更低；
- 池算力低时，每单位 work 收益更高；
- 矿工可以根据池当前算力选择加入或离开；
- 网络 difficulty 变化后，相同时间代表的网络期望工作不同。

因此理想窗口是：

> 最近 \(X\) 个“网络块等价 work”，例如 \(X=1\)、\(X=2\)，而不是最近 N 条 share 或最近 N 分钟。

\(X=2\) 表示窗口大约覆盖池找到 2 个块所需的期望工作量。窗口越长，单个 share 收益方差越低，但兑现周期越长。没有统一最佳值，工程上的初始范围通常是 1–2 个期望块单位。

## 1.2 PPLNS 有序工作轴

每个币种维护一条确定性的 accepted-work 序列。

设：

\[
C_i=\sum_{j\le i}u_j
\]

每个 share 对应工作轴上的区间：

\[
(C_{i-1},C_i]
\]

多 Stratum 实例时，不能仅用墙钟时间排序。必须有：

- 每币全局提交序号；或
- 单分区 append-only 事件流；或
- 确定性复合顺序，如提交序列、gateway ID、gateway local sequence。

排序的公平性不是最关键的，**可重复性**才是关键：同一批原始事件重放 100 次，边界 share 和分配结果必须完全一致。

## 1.3 窗口终点：是否包含爆块 share

存在两种自洽合同：

### 方案 A：不包含爆块 share，推荐

设爆块 share 为 \(s_b\)，令 \(C_b^-\) 表示它进入序列前的累计 work。

块 \(b\) 的窗口是：

\[
(C_b^- - X,\ C_b^-]
\]

也就是：

- 爆块 share 不参与自己发现的这个块；
- 它正常进入工作序列；
- 它可以参与后面发现的块。

这更接近严格的“share 是对未来块的收益合同”，也与 Rosenfeld 的 unit-PPLNS 描述一致。

### 方案 B：包含爆块 share

若遗留系统已经采用这种做法，则窗口为：

\[
(C_b-X,\ C_b]
\]

此时爆块 share 只能按池给它分配的 share work \(u_b\) 计算，绝不能因为它碰巧满足 network target，就把它当作一个完整网络块的 work。

两种方案都能守恒，但分配结果不同。必须写进 `settlement_policy_version`，上线后不能悄悄切换。

另一个常见 bug 是：爆块进入快速提交路径，却没有进入普通 accepted-share 序列，导致它是否参与窗口取决于线程竞态。

## 1.4 边界 share 必须按比例切割

定义 share \(i\) 在块 \(b\) 窗口中的有效 work：

\[
e_{i,b}
=
\max
\left(
0,\ 
\min(C_i,C_b^-)
-
\max(C_{i-1},C_b^--X)
\right)
\]

如果 share 完全在窗口内，则 \(e_{i,b}=u_i\)。

如果它恰好跨过左边界，则只计交集部分。

不能简单地“最后一条整体纳入”：

- 否则实际窗口可能显著超过 \(X\)；
- 高 difficulty share 落在边界时会产生跳变；
- 矿工分配结果会因 vardiff 粒度而变化；
- 两个做同样 work 的矿工可能仅因 share 切块不同而获得不同长期期望。

金额和 work 均不能使用浮点数。最稳的来源是服务端实际发出的整数 target，而不是 Stratum 中可能经过浮点转换的 difficulty 字符串。

## 1.5 分配公式

设块 \(b\) 实际由池控制的 gross reward 为 \(G_b\)，它应来自最终 canonical coinbase/奖励交易实际输出，而不是硬编码“固定 subsidy”。

先排除：

- 共识强制的 masternode/dev/founder/treasury 输出；
- 不归矿池控制的奖励；
- 销毁部分；
- 合并挖矿中属于其他链的奖励。

设：

- \(F_b\)：池费；
- \(O_b\)：合同约定的其他分配；
- \(A_b=G_b-F_b-O_b\)：矿工可分金额。

矿工 \(m\) 的窗口 work 为：

\[
U_{m,b}=\sum_{i\in m}e_{i,b}
\]

窗口已满时：

\[
x_{m,b}=A_b\frac{U_{m,b}}{X}
\]

并且：

\[
\sum_m x_{m,b}=A_b
\]

这是舍入前的精确有理数结果。

## 1.6 连续快速出块和窗口重叠

每个块独立建立自己的窗口。

如果短时间内连续找到 \(b_1,b_2,b_3\)：

- 每个块都有自己的终点；
- 三个窗口可以大面积重叠；
- 同一个 share 可以从多个块获得奖励；
- share 不会因为在前一个块中分过钱就被“消费掉”；
- 不会在找到块后清空窗口。

这正是 PPLNS 与 round-based proportional 的根本区别。Rosenfeld 的定义明确指出 PPLNS 消除了 round 边界，前一块之前的 share 仍可能参与后一块。[PPLNS 分析](https://cryptochainuni.com/wp-content/uploads/Analysis-of-Bitcoin-Pooled-Mining-Reward-Systems-Meni-Rosenfeld.pdf)

如果想让一个 share 最多支付一次，那是 pay-once-PPLNS，属于另一套经济合同，不能混称普通 PPLNS。

同一高度找到两个竞争块时，可以分别生成 provisional settlement，但最终只能让 canonical 的那个进入可用余额；另一个必须作为 orphan 反转。

## 1.7 stale share 是否计入

推荐规则：

| share 状态 | PPLNS work |
|---|---:|
| 有效、满足 assigned target、父块仍有效 | 100% |
| 旧 job，但 prevhash 未变，只是模板刷新 | 可以计入 |
| prevhash 已变化的真正 stale | 0 |
| duplicate | 0 |
| low difficulty | 0 |
| badpow | 0 |
| 未授权、job 不属于会话 | 0 |
| 能产生实际 uncle/ommer 奖励 | 进入单独的 uncle 收益合同 |

真正 stale share 已不可能为当前池创造 canonical block，把它放入 PPLNS 分母会稀释正常矿工。

如果为了降低跨国矿工延迟损失而“付 stale”：

- 应作为池方补贴；
- 记入 `stale subsidy expense`；
- 不能偷偷放进正常 PPLNS 窗口稀释其他矿工；
- 补贴比例必须版本化。

算力统计可以记录 stale 的有效 PoW，但结算 work 与统计 work 是两套口径。

## 1.8 冷启动窗口不满

设当前只有 \(S<X\) 的历史 work。

有两种政策：

### 按实际 S 归一化

\[
x_{m,b}=A_b\frac{U_{m,b}}{S}
\]

这样一个块全部发给矿工，但早期每单位 work 收益被放大了 \(X/S\) 倍。

例如窗口只填了 20%，早期矿工每单位 work 获得正常水平的 5 倍。这不是错误，但属于明确的 launch bonus，也会吸引窗口套利。

### 始终按 X 归一化，推荐作为标准合同

\[
x_{m,b}=A_b\frac{U_{m,b}}{X}
\]

矿工合计获得：

\[
A_b\frac{S}{X}
\]

剩余：

\[
A_b\left(1-\frac{S}{X}\right)
\]

进入明确的 `PPLNS bootstrap/continuity reserve`。

这样早期矿工每单位 work 与稳定期完全相同。可以把该 reserve 用于：

- 计划停池时补偿仍处于窗口尾部、尚未遇到未来块的 share；
- reorg 风险；
- 明确的启动补贴。

不能把这部分无记录地归为“池利润”。

如果商业上希望新池冷启动更有吸引力，可以宣布“窗口不满时全额分配”，但要明确这是 launch bonus，而不是偷偷改变数学分母。

## 1.9 网络 difficulty 和奖励变化

上述标准化 \(u_i\) 已解决 vardiff 和网络 difficulty 变化问题：每条 share 使用提交时的 network target。

但实际奖励 \(A_b\) 如果发生已知跳变，例如：

- halving；
- 交易手续费剧烈变化；
- MEV；
- treasury 规则切换；

简单地把未来实际块奖励分给过去窗口，仍可能产生 hopping。

严格 unit-PPLNS 还会为每条 share 保存一个 `amplifier`：

\[
a_i=\text{该 share 提交时，如果它爆块，预期可分奖励}
\]

每次未来块对该 share 的理论支付变为：

\[
(1-f)\frac{a_i e_{i,b}}{X}
\]

但当历史 \(a_i\) 不同时，本次总支付可能不等于当前实际块奖励，差额必须由池的 normalization reserve 吸收。这就给运营方引入了部分方差风险。

因此我的实际建议是：

- 第一版使用 actual-reward work-PPLNS，保证逐块资产守恒；
- 承认在 halving/极端手续费跳变附近不具备完全 hopping-proof；
- 规则变化时建立新 settlement epoch；
- 不要一开始就实现复杂 amplifier，除非你们真的愿意承担差额风险。

---

# 2. PPLNS、PPS、FPPS、SOLO 的账务本质

| 模式 | share 被接受时 | 找到块时 | 方差承担者 | 孤块损失 |
|---|---|---|---|---|
| PPLNS | 形成潜在窗口权益 | 按实际块奖励分配 | 矿工 | 通常矿工无最终收入 |
| PPS | 立即产生确定矿工债权 | 块全部属于池方 | 池 | 池方承担 |
| FPPS | 立即产生 subsidy + 估算手续费债权 | 实际奖励归池 | 池，且增加 fee 估算风险 | 池方承担 |
| PPS+ | subsidy 按 PPS，手续费按实际块/PPLNS | 混合 | 双方分担 | 按组成部分 |
| SOLO | 普通 share 只用于验证算力 | 找到者获得实际块减池费 | 单个矿工 | 找到者承担，除非另有保险 |

行业对 PPS、PPS+、FPPS 的命名并非完全统一。以 F2Pool 的公开说明为例，其 FPPS 扩展 PPS 并加入交易手续费，而具体手续费部分如何估算、按什么时间窗口计算，需要看每个池的合同。[F2Pool payout scheme 说明](https://f2pool.zendesk.com/hc/en-us/articles/360061042332-Payout-schemes-PPS-PPLNS-FPPS-PPS)

## 2.1 PPS 数学

对 share \(i\)：

\[
P_i=(1-f)\hat R_i u_i
\]

其中：

- \(u_i\)：share 的网络块等价 work；
- \(\hat R_i\)：share 提交时对应模式承诺的期望块奖励；
- \(f\)：池费。

PPS 的关键是：share 一旦被最终接受，这笔债权不再依赖池是否找到块、块是否孤、节点是否宕机。

因此：

- PPS share credit 与具体 found block 没有一一配对；
- 不能在孤块后回滚矿工 PPS 收益；
- 找块收入和 share 支出分别进入池方 P&L；
- 池方长期利润来自期望收入减 PPS 支出、风险损失和运营成本。

## 2.2 FPPS 的手续费估值

必须明确：

- 使用全网块还是本池块；
- 过去 24 小时、过去 144 块还是日历日；
- 是否排除异常高费块；
- 使用哪个时区；
- 数据延迟；
- orphan 是否进入样本；
- MEV、uncle、merged mining 是否计入；
- 本次 share 使用哪个已冻结的费率版本。

推荐：

- 使用已经结束的前一统计期，而不是含当前未来数据；
- 每条 share 固化 `fpps_rate_version`；
- 估值器改变后新建 policy version；
- 不允许结算时回看并选择对池方有利的费率；
- 异常值裁剪必须事先公开。

## 2.3 PPS 如何定价

headline fee 至少要覆盖：

\[
f_{\text{headline}}
\ge
f_{\text{orphan}}
+
f_{\text{invalid}}
+
f_{\text{BWH}}
+
f_{\text{FPPS estimator}}
+
f_{\text{OPEX}}
+
f_{\text{capital}}
+
f_{\text{profit}}
\]

真正能用于准备金模型的不是宣传费率，而是净风险边际：

\[
m=f_{\text{headline}}-\text{所有预期损失和成本}
\]

如果 \(m\le0\)，再大的短期准备金也只是延迟破产。

## 2.4 准备金公式

Rosenfeld 在理想化的固定奖励、独立 Poisson、无限经营期模型中给出：

\[
R_{\min}
=
B\frac{\ln(1/\delta)}{2m}
\]

其中：

- \(B\)：每块有效奖励；
- \(m\)：净风险边际；
- \(\delta\)：最终破产概率上限。

例如：

- \(m=2\%\)，允许破产概率 \(0.1\%\)：

\[
R/B=\frac{\ln 1000}{0.04}\approx172.7
\]

也就是约 173 个块奖励的准备金。

- \(m=3\%\)，破产概率目标 \(10^{-6}\)：

\[
R/B=\frac{\ln 10^6}{0.06}\approx230.3
\]

这就是为什么低费率 PPS 所需准备金常常远超直觉。原论文也给出了低费率、小准备金下最终破产概率极高的示例。[PPS safety-net 公式](https://cryptochainuni.com/wp-content/uploads/Analysis-of-Bitcoin-Pooled-Mining-Reward-Systems-Meni-Rosenfeld.pdf)

但该公式仍然没包含：

- 交易手续费波动；
- orphan/reorg；
- invalid block；
- block withholding；
- NiceHash 突然涌入的 PPS 敞口；
- 节点宕机；
- coinbase maturity 流动性；
- 钱包事故和运营成本；
- 小币流动性枯竭。

所以生产公式应是：

\[
R_{\text{required}}
=
\max
\left(
R_{\text{ruin model}},
R_{\text{stress drawdown}},
R_{\text{liquidity}}
\right)
+
R_{\text{reorg/BWH/operational}}
\]

coinbase maturity 的额外流动性需求可以粗略估为：

\[
R_{\text{maturity}}
\approx
\alpha M B(1-f)
\]

其中：

- \(\alpha\)：池占全网算力比例；
- \(M\)：coinbase maturity 的网络块数。

Bitcoin coinbase 至少等待 100 个块才能花费，目的之一正是降低 stale/fork coinbase 被花掉的风险。[Bitcoin Developer Guide](https://developer.bitcoin.org/devguide/block_chain.html)

准备金必须：

- 按币种隔离；
- 主要持有同币资产；
- 与运营费用、热钱包日常余额分开；
- 不把尚未成熟 coinbase 当作可用流动性；
- 给每币设置最大 PPS hashrate/PPS liability；
- 达到风险利用率上限时自动停止接收新 PPS 算力，转 PPLNS 或拒绝新增。

## 2.5 你们是否应该做 PPS

我的明确建议是：**第一阶段不要全面做 PPS**。

尤其不适合：

- 低算力、易 51% 攻击的小链；
- RPC 和 reward 规则不稳定的魔改链；
- 经常 hard fork；
- 交易所/钱包流动性差；
- network target 或 coinbase maturity 没吃透；
- NiceHash 可瞬间占据大比例算力；
- 尚未完成独立账务审计。

可以先做：

- PPLNS；
- SOLO；
- 对少数成熟币提供 capped PPS；
- 或 subsidy PPS + fee PPLNS 的 PPS+。

只有当某个币满足以下门槛才开放 PPS：

- 共识、奖励、reorg 行为经过长期验证；
- 分录和链上对账连续无差异；
- 压测过 hashrate 突增；
- 有独立、隔离、可证明的同币准备金；
- 能统计 orphan、BWH、invalid 风险；
- 有 per-coin PPS kill switch。

---

# 3. 舍入和余数

## 3.1 先计算精确 entitlement

对矿工 \(m\)：

\[
x_m=A\frac{U_m}{X}
\]

不要对每个 worker、每条 share 单独舍入。先聚合到最终 settlement account，再舍入。

第一步：

\[
q_m=\lfloor x_m\rfloor
\]

剩余最小单位：

\[
R=A-\sum_m q_m
\]

## 3.2 推荐 largest remainder

计算每个矿工的小数余数：

\[
r_m=x_m-q_m
\]

把剩余的 \(R\) 个最小单位，依次给余数最大的 \(R\) 个账户。

这样保证：

\[
\sum_m allocation_m=A
\]

优点：

- 每个块严格守恒；
- 每人的误差小于 1 个最小单位；
- 不会产生凭空多币或少币。

相同余数的 tie-break 必须确定，例如：

\[
H(block\_hash\parallel settlement\_account\_id\parallel policy\_version)
\]

不能按数据库当前返回顺序，否则索引、并行计划或升级可能改变结果。

## 3.3 长期公平

单块 largest remainder 仍可能对经常处于某种分数位置的账户产生微小偏差。更严格的方案是 cumulative balanced rounding：

- 为每个账户保存累计精确 entitlement；
- 保存累计已分原子单位；
- 下一块优先补偿累计“少分最多”的账户；
- 保证每个账户长期误差保持在约 1 个最小单位以内。

必须防止拆账户薅 rounding dust：

- 在 settlement account 层聚合，不按 worker；
- 使用累计误差，不是每次重新开始；
- 极小余额仍受最低打款门槛约束。

## 3.4 pool fee 的舍入顺序

推荐顺序：

1. 获取实际 gross reward \(G\)；
2. 排除不归池控制的强制输出；
3. 用有理数算 pool fee；
4. pool fee 向下取整，使剩余原子单位归矿工；
5. 得到矿工 distributable \(A\)；
6. 对 \(A\) 做 largest remainder。

策略必须版本化，包括：

- fee 计算基数；
- fee 舍入方向；
- beneficiary 聚合层级；
- largest remainder 规则；
- tie-break；
- 边界 share 处理。

绝不能对每人 `round()` 后再假设总和自然等于块奖励。

---

# 4. 守恒对账和复式记账

## 4.1 不要只写一个“万能等式”

“已确认奖励、余额、已付、准备金”中，有些是累计流量，有些是时点存量。强行放进一个式子很容易重复计算。

至少维护 3 个独立恒等式。

### A. 单块结算守恒

PPLNS/SOLO：

\[
G_b=M_b+F_b+O_b+R_b
\]

其中：

- \(G_b\)：池实际控制的 canonical gross reward；
- \(M_b\)：矿工分配；
- \(F_b\)：pool fee；
- \(O_b\)：其他合同分配；
- \(R_b\)：显式 rounding/bootstrap 项。

采用 largest remainder 且窗口已满时，应有 \(R_b=0\)。

PPS/FPPS 没有逐块等式：

- share credit 是池支出；
- found block 是池收入；
- 二者只能长期期望匹配。

### B. 矿工负债滚动等式

\[
\begin{aligned}
L_{\text{close}}
=&L_{\text{open}}
+\text{reward credits}
+\text{refunds}
+\text{positive adjustments}\\
&-\text{confirmed payout principal}
-\text{miner-borne fees}
-\text{debt offsets}\\
&-\text{reward reversals}
-\text{negative adjustments}
\end{aligned}
\]

注意：

- 创建打款批次只是 `available → payout_in_transit`，矿工总负债没有减少；
- 只有达到合同定义的链上确认状态，才能减少总负债；
- 已付金额是期间流量，不是当前资产负债表项目。

### C. 资产负债表恒等式

每币独立：

\[
\begin{aligned}
&A_{\text{free}}
+A_{\text{encumbered}}
+A_{\text{immature}}
+A_{\text{other}}
+A_{\text{miner debt receivable}}\\
=&L_{\text{pending}}
+L_{\text{available}}
+L_{\text{payout in transit}}
+L_{\text{refund}}
+L_{\text{other}}\\
&+E_{\text{contributed reserve}}
+E_{\text{retained earnings/loss}}
\end{aligned}
\]

重要概念：

- `reserve` 是权益的用途标签，不是一笔凭空多出来的资产；
- 只有资产侧真实存在的币才能支持 reserve；
- 垫付若来自股东，就是 contributed capital 或 operator loan；
- 钱包余额不应该等于矿工余额，因为中间还有 pool equity、fee、immature、退款、在途等。

## 4.2 对账频率和 delta 处理

建议：

| 对账 | 频率 |
|---|---|
| 每个 journal batch 借贷平衡 | 同步、每次提交 |
| 每块分配守恒 | 每个块 |
| 打款批次输入/输出/fee | 签名前、广播后、确认后 |
| 节点状态与内部 block 状态 | 1–5 分钟 |
| 钱包资产与资产子账 | 每小时 |
| 每币完整 close | 每日 |
| 独立重算/财务复核 | 每月 |
| reorg、升级、hard fork 后 | 立即 |

delta 规则：

- 复式 journal 借贷不平：任何 1 个最小单位都算 P0，立即停止账务写入和打款。
- 单块分配不等于 distributable：立即冻结该币结算。
- 链上资产小于资产账且无法被已知 mempool/reorg/immature 项解释：立即冻结该币打款。
- 出现无法解释的资产“盈余”也不能直接记收入，它可能代表漏记矿工负债或退款。
- 只有被明确匹配到 txid、block、mempool、change、immature 的 timing delta 才可继续运行。

一般只冻结受影响币种。如果怀疑共用 ledger engine、金额库或数据库事务出错，则冻结全部币种。

冻结期间：

- 继续收 share；
- 新权益进入 quarantine/pending；
- 不生成新的可提现余额；
- 不删除、不改写旧 journal；
- 修复只使用 compensating entries。

## 4.3 必须与链上真相对账

内部账自洽只能证明“数据库自己同意自己”，不能证明币真的存在。

需要分别核对：

- canonical block hash、height、chainwork；
- immature coinbase；
- spendable UTXO/account balance；
- locked/encumbered UTXO；
- mempool 中的 outgoing transaction；
- change；
- watch-only；
- conflicted/replaced/dropped transaction；
- privacy chain 的 notes、nullifiers/key images。

不要只调用一个 `getbalance`。不同节点版本对 immature、watch-only、unconfirmed 的默认口径可能不同；Bitcoin Core 本身也会把 coinbase 细分为 `generate`、`immature`、`orphan`。[Bitcoin Core wallet transaction 分类](https://bitcoincore.org/en/doc/30.0.0/rpc/wallet/gettransaction/)

定位资产差异的顺序：

1. 用两台独立节点确认 canonical tip 和 chainwork。
2. 从链上重建池控制的 UTXO/address/note inventory。
3. 与资产 ledger 按 txid/outpoint/note 对齐。
4. 找到首次出现差异的 block height 或 transaction。
5. 追到对应 block journal、settlement manifest。
6. 再追到矿工 allocation 和 payout output。
7. 用前一日已平 checkpoint 做二分定位。

## 4.4 复式账户建议

### 资产

- Hot wallet spendable
- Cold wallet spendable
- Encumbered by payout
- Immature coinbase
- Other receivables
- Miner debt receivable

### 负债

- Miner pending rewards
- Miner available balances
- Miner held/frozen balances
- Payout in transit
- Refund payable
- Other payable

### 权益、收入、费用

- Operator contributed reserve
- Operator loan payable
- Block subsidy revenue
- Transaction fee/MEV/merged-mining revenue
- Miner reward expense
- Payout network fee expense
- Orphan/reorg loss
- Bad debt expense
- Promotion/stale subsidy expense
- Rounding adjustment
- Retained earnings/loss

## 4.5 典型分录

| 事件 | 借方 | 贷方 |
|---|---|---|
| PPLNS 块进入 canonical pending | Immature asset \(G\)；Miner reward expense \(M\) | Block revenue \(G\)；Miner pending liability \(M\) |
| 块成熟 | Spendable asset；Miner pending liability | Immature asset；Miner available liability |
| 普通 PPLNS 孤块 | 对原入账做完整反向 compensating entries | 同左 |
| PPS share 被接受 | PPS reward expense | Miner available/pending liability |
| PPS 池找到块 | Immature asset | Block revenue |
| PPS 块孤掉 | Orphan/revenue reversal | Immature asset；不得冲矿工 PPS liability |
| 创建打款批次 | Miner available liability | Payout-in-transit liability |
| 打款确认 | Payout-in-transit liability | Spendable asset |
| 池承担网络费 | Network fee expense | Spendable asset |
| 矿工承担网络费 | Miner available liability；Network fee expense | Fee recovery；Spendable asset |
| 打款失败且未上链 | Payout-in-transit liability | Miner available liability |
| 收到待退款资金 | Wallet asset | Refund payable |
| 退款确认 | Refund payable；Network fee expense | Wallet asset |
| 运营方注资 | Wallet asset | Contributed capital 或 operator loan |
| 发放运营补贴 | Promotion expense | Miner liability |
| 形成矿工欠款 | Miner debt receivable | Miner reward expense reversal |
| 用未来收益抵债 | Miner liability | Miner debt receivable |
| 欠款无法收回 | Bad debt expense | Miner debt receivable |

任何人工余额调整都必须有一个对应的 expense、income、receivable 或 clearing account。禁止直接执行“余额加 10”。

---

# 5. 孤块、reorg 和 debts

## 5.1 最好的防线：pending 不可提现

UI 应区分：

- `estimated`：尚未产生正式债权；
- `pending`：已有 provisional allocation；
- `confirmed/locked`：链上存在但未成熟；
- `available`：达到结算确认条件；
- `payout_in_transit`；
- `paid`。

普通孤块应当只冲销 `pending`。如果等到 coinbase maturity 后才进入 `available`，绝大多数普通 fork 根本不会产生矿工债务。

对于小众低算力链，建议：

\[
\text{available threshold}
=
\max(\text{consensus maturity},\ \text{risk confirmations})
\]

必要时在 coinbase maturity 后再加额外安全深度。

## 5.2 已入 available 但未支付

处理顺序：

1. 冻结该币新打款。
2. 找到该块全部 allocation journal。
3. 先冲销矿工尚未支付的 available/held 余额。
4. 已进入 batch 但未广播的，从 batch 移除并退回后再冲销。
5. 广播状态不确定的，不得假设可撤销，先按链上交易状态处理。
6. 剩余已支付部分形成 `miner debt receivable`。

不能直接把余额改成负数并删除历史。

## 5.3 已经支付

链上不可逆，只剩经济追索：

- 未来同币收益抵扣；
- 与大型矿场协商退回；
- 按合同主张债权；
- 池方准备金吸收；
- 欠款最终核销。

建议债务记录包含：

- debt ID；
- source block、allocation、payout；
- 原因：reorg、duplicate、overpayment、fraud；
- principal；
- 已回收；
- 剩余；
- recovery rate；
- 是否允许跨币；
- 创建和核销时间；
- policy version；
- 人工审批和证据。

不要把欠款只表示成 `balance=-123`。

## 5.4 追缴顺序和比例

推荐顺序：

1. 同一错误事件尚未支付的余额；
2. 同币未来收益；
3. 大客户合同保证金；
4. 自愿退款；
5. 经明确同意的跨币抵扣；
6. 池方坏账核销。

跨币抵扣涉及价格、汇率时点、交易费用和法律问题，默认禁止。若允许，必须事前合同约定并锁定汇率来源和时间。

具体比例建议：

| 原因 | 建议追缴 |
|---|---|
| PPS 正常 orphan/luck | 0%，由池承担 |
| PPLNS/SOLO 明示“早付但条件未最终满足” | 最终追缴 100%，每期新收益抵扣 25%–50% |
| debt 小于一个起付额 | 可 100% 抵扣下一笔 |
| duplicate/fraud/故意利用漏洞 | 100% 抵扣，必要时冻结 |
| 池自身 bug、矿工无主观过错 | 先冲未付余额；已支付部分视规模由池部分或全部承担 |
| 极端系统性 deep reorg | 按预先公布的保险和损失 waterfall，不得临时拍脑袋 |

“每期抵扣 50%”是比较实际的默认值：矿工仍能收到一部分收益，同时债务逐步回收。大型商业矿场可以合同约定 100% 或使用保证金。

## 5.5 矿工不再回来

- 债权保留，但按会计周期计提 impairment。
- 经过约定期限，例如 90/180 天无回收，转坏账费用。
- 会计核销不一定等同于法律上放弃债权。
- 法律追偿、跨币抵扣、数据保存期限依司法辖区而异，此处“不确定”，应由当地律师确认。
- 不应把其他矿工余额拿来填特定矿工欠款，除非结算合同明确允许社会化损失。

## 5.6 深 reorg 处置流程

1. 立即冻结受影响币种的 settlement finalization 和 payout。
2. 获取至少两个节点对 tip、height、chainwork/finality 的看法。
3. 保存 reorg 前后的 headers、block、wallet、mempool 证据。
4. 确定真正 canonical chain。
5. 标记被移除和重新进入的块。
6. 生成补偿 journal，不删除原 journal。
7. 重算受影响 PPLNS allocation。
8. 对未付余额冲销，对已付形成 debt 或池损失。
9. 重做资产、负债、钱包三套对账。
10. 独立复核后恢复。

任何“已经成熟的块被回滚”都应视为 P0。不能因为节点 RPC 一时又显示正常就自动恢复付款。

---

# 6. 算力统计口径

## 6.1 矿工和池算力

在时间区间 \([t_0,t_1]\) 内：

\[
\hat H
=
\frac{\sum_i w_i}{t_1-t_0}
\]

对于 Bitcoin 风格 difficulty，常见近似是：

\[
\hat H
=
\frac{\sum_i d_i\cdot2^{32}}{\Delta t}
\]

但只在该链 difficulty-1 定义确实对应 \(2^{32}\) 期望哈希时成立。多币系统最好从实际 target 推导 \(w_i\)，不要把 `2^32` 写死到核心。

建议展示 3 种池/矿工算力：

- **Gross valid hashrate**：所有能验证 PoW 的 share，包括 stale。
- **Effective hashrate**：有效、非 stale、满足 assigned target 的 work。
- **Paid hashrate**：实际进入结算合同的 work。

self-reported hashrate 只能作为诊断字段，不能作为账务真值。

统计窗口建议：

- 5 分钟：即时状态，噪声大；
- 15 分钟：运维；
- 1 小时：用户主要显示；
- 24 小时：长期比较；
- 或按时间半衰期的 EMA。

## 6.2 为什么不能复用 PPLNS 窗口

PPLNS 窗口是 work-domain 窗口，其实际时间长度会随池算力变化：

- 算力上升，窗口覆盖时间缩短；
- 算力下降，窗口覆盖时间变长；
- 找块时窗口以块事件结束；
- 连续块窗口重叠；
- 冷启动窗口不满。

如果用它计算“当前算力”，统计区间本身会被算力和找块事件共同决定，产生选择偏差。

因此：

- PPLNS 用累计 work；
- hashrate 用固定时间；
- 两者只能共享底层 share work，不能共享窗口。

## 6.3 share 数量与置信度

share 到达近似 Poisson。

若窗口只有 \(k\) 个等 difficulty share，算力估计相对标准差约为：

\[
\frac{1}{\sqrt{k}}
\]

因此：

- 25 个 share：约 20% 的 1σ 噪声；
- 100 个：约 10%；
- 400 个：约 5%。

vardiff 下可以近似使用有效样本量：

\[
n_{\text{eff}}
\approx
\frac{(\sum_i w_i)^2}{\sum_i w_i^2}
\]

所以只显示一个没有置信区间的“精确到小数点后两位算力”，本质是假精确。

## 6.4 全网算力

### `getnetworkhashps`

Bitcoin Core 的 `getnetworkhashps` 是根据最近 N 个块估算，默认 120 块；它明确返回的是 estimated network hashes per second，不是真值。[Bitcoin Core RPC 文档](https://bitcoincore.org/en/doc/25.0.0/rpc/mining/getnetworkhashps/)

优点：

- 考虑历史块 work；
- 可跨 difficulty 调整；
- 不直接假设当前网络已经达到 difficulty equilibrium。

但小众链经常存在：

- RPC 复制 Bitcoin 实现却没适配自己的 PoW；
- 返回固定值或溢出；
- timestamp 被矿工操纵；
- difficulty 多算法共用错误；
- fast-retarget 逻辑不同。

因此必须交叉验证。

### `difficulty × 基准 work ÷ 目标出块时间`

它表示：

> 在当前 difficulty 下，为了长期保持目标出块时间，理论上需要多少算力。

这不是最近真实观测算力。刚发生算力突增/突降时，它会明显滞后。

### 更通用的链上估计

如果能获得每块 chainwork：

\[
\hat H_{\text{network}}
=
\frac{
ChainWork(h)-ChainWork(h-k)
}{
t_h-t_{h-k}
}
\]

建议：

- 同时计算 30、120、1000 块窗口；
- 对极短异常 interval 做稳健统计；
- 明确标注 estimate；
- 与节点 RPC 比较；
- 多算法链分别算各算法 work。

如果链只有 target 和 header，也可以自行累加每块 consensus work。

如果连“target 如何转换为预期 work”都不确定，或者时间戳没有可信约束：

- 不能从池 share 反推全网算力，除非已知池的市场份额；
- 不要显示一个看似精确的数字；
- 可以显示 `network difficulty`、最近块时间和“算力估算不可用”。

“干脆不显示”比显示错误的全网算力更专业。

## 6.5 effort 和 luck

对每条有效结算 work：

\[
\lambda_i=\frac{w_i}{W^{network}_i}
\]

一个 round 的累计 expected blocks：

\[
\Lambda=\sum_i\lambda_i
\]

若找到 1 个块，其 effort 为：

\[
Effort=100\%\times\Lambda
\]

如果期间 network difficulty 变化，必须逐 share 使用当时的 \(W^{network}_i\)，不能用最终 difficulty 除整个 round。

期间找到 \(K\) 个 canonical block：

\[
CumulativeEffort
=
100\%\times\frac{\Lambda}{K}
\]

推荐把 luck 定义为：

\[
Luck
=
100\%\times\frac{K}{\Lambda}
\]

但行业里有人把 luck 当 effort，有人显示其倒数，所以 UI 最好直接显示 `effort`，并解释“低于 100% 表示幸运”。

还应拆成：

- candidate effort：相对于所有池端验证出的 full-PoW candidate；
- canonical effort：相对于最终上链块；
- revenue effort：相对于实际获得可花奖励的块。

---

# 7. effort 长期偏离 100% 的诊断

## 7.1 单轮非常不可靠

找到块所需标准化 effort 近似指数分布：

\[
P(Effort>x)=e^{-x}
\]

其中 \(x\) 用 100% 为 1。

因此：

- 中位 effort 约 69.3%，不是 100%；
- 超过 300% 的概率约 5%；
- 超过 500% 的概率约 0.67%；
- 超过 1000% 的概率约 0.0045%。

一次 300% round 完全可能只是运气，不能报警为共识 bug。

累计 \(K\) 个块后，总 effort 近似 Gamma 分布，平均每块 effort 的标准差约：

\[
\frac{1}{\sqrt K}
\]

100 个块时仍有约 10% 的 1σ 波动。

## 7.2 建议监控规则

计算：

\[
\Lambda=\sum_i\frac{w_i}{W_i}
\]

观察 canonical block 数 \(K\)，理论上：

\[
K\sim Poisson(\Lambda)
\]

建议：

- \(\Lambda<10\)：只展示，不报警；
- \(\Lambda\ge20\) 且精确 Poisson 双尾概率小于 \(10^{-3}\)：warning；
- 概率小于 \(10^{-6}\)，或多个不重叠窗口持续异常：critical；
- 必须校正多币、多窗口反复检验带来的误报。

## 7.3 effort 长期过高

可能原因按检查顺序：

1. share work 算大了：vardiff difficulty、diff1 constant、target 转换错误。
2. network work 算小了。
3. duplicate share 被重复计 work。
4. full block target 比较端序错误。
5. `hash < target` 与 `hash ≤ target` 边界错误。
6. 某算法实际 PoW 与节点共识实现不同。
7. full candidate 被验证出来但没有进入 submit 通道。
8. 节点 `submitblock` 被拒、超时或提交到错误链。
9. 模板无效、coinbase/merkle/版本位错误。
10. orphan/传播延迟过高。
11. block withholding。
12. 找到块但 block 事件漏写数据库。
13. multi-instance 去重把真实块误认为重复。
14. 网络 difficulty RPC 错误。

## 7.4 effort 长期过低

可能是好运，也可能是：

- share work 少算或丢 share；
- 同一个 block 被重复计数；
- orphan/uncle/merged block 混进 canonical 数；
- network work 算大；
- difficulty 调整高度错一位；
- 某些矿工按 achieved difficulty 被过度计分；
- block candidate 重放。

## 7.5 用三段漏斗定位

持续维护：

\[
\text{accepted work}
\rightarrow
\text{local full-PoW candidates}
\rightarrow
\text{node accepted blocks}
\rightarrow
\text{canonical rewarded blocks}
\]

- accepted work 正常，local candidate 少：PoW/target/难度/BWH 问题。
- local candidate 正常，node accepted 少：模板、共识、RPC、节点问题。
- node accepted 正常，canonical 少：传播、fork、51%/reorg 问题。
- canonical 正常，钱包奖励少：coinbase 输出、reward 解析或钱包问题。
- 钱包奖励正常，矿工账少：settlement/ledger 问题。

这比只看一个“池 luck”数字有用得多。

还可以抽样检查 accepted hash 在 share target 下是否近似均匀分布。如果大量 hash 异常集中在 target 边缘，可能是 target、端序、代理或伪 share 问题。

---

# 8. 矿工投诉时的证据链

池方应能从投诉钱包一路追到链：

> 地址/账户  
> → worker 和登录绑定  
> → share 收据  
> → assigned target/job  
> → PoW 验证结果  
> → 全局累计 work 顺序  
> → 某块的 PPLNS 窗口  
> → 边界切割  
> → 精确 entitlement  
> → 舍入  
> → ledger journal  
> → payout batch  
> → txid/output/确认

## 8.1 必须保留的数据

### share 证据

- immutable share ID；
- coin、pool、port、algorithm；
- account、worker；
- session/gateway；
- job/template ID；
- assigned target，不仅是浮点 difficulty；
- network target；
- nonce、extranonce、ntime、version、solution；
- server receive time；
- 全局 work sequence；
- accepted/stale/duplicate/badpow 原因；
- work amount；
- PoW hash 或可重算数据；
- validator/version。

### job/template

- prevhash；
- height；
- template hash；
- coinbase/merkle commitments；
- network target；
- reward/mandatory outputs；
- node endpoint/version；
- consensus adapter version；
- job 下发和失效时间。

### block settlement

- block hash、height；
- canonical/orphan 历史；
- policy version；
- \(X\)、窗口终点、累计 work；
- boundary share 和部分权重；
- 每账户精确 work；
- gross reward、fee、distributable；
- 舍入前分数；
- rounding 排名；
- 最终原子单位分配；
- journal batch ID。

### 打款

- balance journal；
- payout request；
- batch；
- output index；
- principal、miner-borne fee；
- txid；
- 广播和确认历史；
- 重发/RBF/替代关系。

## 8.2 防篡改

数据库里的记录只能证明“你们现在声称当时是这样”。

若想真正提高自证能力：

- share 事件使用 append-only log；
- 每个批次计算 Merkle root；
- 每块发布 settlement manifest hash；
- 每日对 journal、share、block manifest 做签名；
- 将签名和 hash 存入独立 WORM/object-lock 存储；
- 可选择把每日 root 提交给外部时间戳服务；
- 给矿工提供自身 allocation 的 Merkle proof。

这样发生争议后，无法无痕重写过去记录。

## 8.3 建议保留期

| 数据 | 建议 |
|---|---|
| 复式 journal、人工调整、债务、打款 | 永久或至少 7–10 年 |
| found block、orphan、reorg、settlement manifest | 永久 |
| 每块分配明细 | 永久 |
| accepted share 原始记录 | 热存 90–180 天，压缩归档至少 1–2 年 |
| 与未结清债务/争议相关 share | 直到结案后再保留法定期限 |
| rejected share 完整 payload | 7–30 天，之后保留聚合和样本 |
| job/template | found block 对应的永久；普通 job 90–180 天或更长 |
| 分钟级算力聚合 | 1–2 年 |
| 小时/日级统计 | 长期或永久 |
| IP、设备指纹 | 按隐私需要尽量短，通常短于财务证据 |

具体法定期限取决于运营实体和司法辖区，属于“不确定”，需结合新加坡及客户所在地区的会计、税务和隐私要求确认。

---

# 9. 最容易埋、最晚才爆的 bug

按危险程度排序：

## 9.1 结算模式串线

例如 SOLO 连接仍进入 PPS/PPLNS share credit 路径：

- 矿工既得到找到块的 SOLO 奖励；
- 又按普通 share 得到一次奖励；
- 账面负债不断增加，但链上没有对应资产。

Prohashing 2017 年公开披露过类似事故：share inserter 错误地给 SOLO 矿工额外 credit，累计错误支付约 24.37 万美元，之后对部分余额进行了 forfeiture，并限制不让账户变成负数。[Prohashing 运营方说明](https://forums.prohashing.com/viewtopic.php?p=9287)

防法：

- reward scheme 是 share/job 生成时固化的 contract ID；
- 不能根据矿工当前设置动态回看；
- 每个 scheme 的 journal 账户分开；
- SOLO share 的理论 reward credit 必须始终为零，只有 canonical block event 能产生权益。

## 9.2 share 重复入账

常见来源：

- 消息重放；
- 消费者重启；
- event bus 至少一次投递；
- 数据库事务提交成功但 ACK 丢失；
- 多 region 同时处理；
- settlement job 重跑。

Prohashing 也公开报告过某些 share 被多次 credit、随后调整 payout 的事故。[Duplicate shares incident](https://forums.prohashing.com/viewtopic.php?t=301)

必须有：

- 确定性 share fingerprint；
- 数据库唯一约束；
- journal event idempotency key；
- settlement batch 唯一键；
- 重跑只得到同一结果。

## 9.3 价格源直接制造负债

多币池、autoexchange 池最危险。

Prohashing 另一次公开事故中，错误的 Pepecoin 卖价使系统在约 13 秒内 credit 超过 1 万美元，而同期实际找到的块只有约 20 美元。[April 28 balances corrected](https://forums.prohashing.com/viewtopic.php?t=1442)

防法：

- 链上原币账与法币估值账分离；
- price feed 只能影响展示和兑换订单，不能修改已产生的原币守恒；
- 价格变化设置最大跳变、成交量和多源验证；
- `原币数量 × 汇率` 是估值，不是新增资产；
- autoexchange 产生的是交易所 receivable/asset conversion，不是凭空增加 miner liability。

## 9.4 vardiff 按 share 条数计分

低难度矿工得到过多，高难度 ASIC/代理得到过少，系统仍然可能逐块守恒，因此多年不一定被总账发现，只会表现为矿工之间持续不公平。

必须按 assigned target 推导 work。

## 9.5 浮点金额和精度错位

典型问题：

- 1 BTC = \(10^8\)，某链却是 \(10^6\)、\(10^{12}\)；
- RPC 返回 coin 单位，数据库以 atomic unit 解释；
- JavaScript Number 超过安全整数；
- decimal 转 integer 时四舍五入；
- fee 在 coin 和 atomic unit 各扣一次。

只允许：

- atomic integer；
- 有理 fee；
- 明确 decimals metadata；
- 对 RPC 边界做字符串解析和范围校验。

## 9.6 reward 硬编码

魔改链可能有：

- halving；
- tail emission；
- treasury；
- masternode；
- founder reward；
- burned fee；
- uncle；
- merged mining；
- dev fee 动态高度；
- coinbase 输出不全归池。

最终资产必须依据 canonical reward transaction 中池实际控制的输出。`getblocktemplate.coinbasevalue` 可以用于预期和校验，但不能代替成熟后的链上资产事实。

## 9.7 赢块 share 丢失或重复

候选块快速路径和普通 share 路径分离，可能造成：

- 爆块 share 未进入 PPLNS；
- 进入两次；
- global sequence 顺序不一致；
- block event 在 share 事务提交前执行。

必须把“share accepted”和“block candidate”作为同一逻辑事件的两个幂等后果。

## 9.8 maturity/reorg off-by-one

不要自己凭 `height + maturity` 猜是否可花，应结合：

- 节点 consensus 状态；
- coinbase output spendability；
- canonical hash；
- confirmations；
- 多节点一致性。

## 9.9 把 pending 交易当作已经付款

这会造成：

- miner liability 提前消失；
- mempool 驱逐后无人恢复；
- RBF 后旧 txid 失联；
- 钱包余额和内部账同时看似“差一笔”。

`payout_in_transit` 必须一直是负债，直到满足付款确认合同。

## 9.10 无完整区块验证

2015 年 BIP66 切换期间，约半数 Bitcoin 算力在未完整验证前块的情况下继续挖，产生多段无效链；公开通告称多个大型矿工损失超过 5 万美元。[Bitcoin.org BIP66 事件通告](https://bitcoin.org/en/alert/2015-07-04-spv-mining)

教训是：

> share 账算得再精确，如果块本身无效，资产侧就是零。

必须：

- 用完整节点验证父块；
- 升级期间双节点/双版本观察；
- 不在未验证 header 上直接 SPV mining；
- 把 invalid candidate 与 orphan 分开统计。

## 9.11 block withholding

Eligius 运营方曾公开称遭遇 block withholding，估算损失约 300 BTC；当事方对故意还是基础设施 bug 存在争议，因此攻击意图“不确定”。[Eligius 事件说明](https://bitcointalk.org/index.php?topic=650831.0)

它不会造成内部账不平：

- share 全部有效；
- PPS liability 正常增加；
- 只是应出现的 block revenue 没出现。

所以必须靠 effort 漏斗、统计异常和 PPS 风险限额发现，而不是普通会计等式。

## 9.12 只保留余额，不保留形成过程

很多 NOMP/yiimp/MPOS 二次开发会把“账户余额”当主数据，发生修复时直接 UPDATE。几年后能看到余额，却无法回答：

- 来自哪个块；
- 哪个 policy；
- 哪些 share；
- 为什么冲销；
- 是否已经支付。

正确原则是：

> journal 和原始业务事件是事实，balance 只是 journal 的缓存投影。

Miningcore 风格的 PPLNS 重算若依赖历史 share，也必须在裁剪 share 前保存不可变 settlement manifest；否则 block 晚变 orphan 或矿工多年后申诉时无法重现。这里描述的是常见二次开发风险，并非断言某个特定版本已经发生事故。

---

如果把这一轮压缩成 8 条不可破坏的会计公理，就是：

1. share 数不等于 work；一切从 assigned target 推导。
2. PPLNS 没有 round reset，快速块窗口天然重叠。
3. exact entitlement 与 atomic rounding 分两步。
4. 每个业务事件都生成借贷相等的 journal。
5. balance 是投影，不是事实源。
6. 内部账、链上资产、钱包状态必须三方对账。
7. 普通 orphan 只应影响 pending；PPS orphan 由池承担。
8. effort 要同时比较 work、candidate、node accepted、canonical、wallet reward 五层。
