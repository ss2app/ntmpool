先给结论：你们改成 **HAProxy + PROXY protocol v2**，方向是对的，也是自建 Stratum 前置层的首选。但它只有在下面 4 个条件同时成立时，才算真正解决了“误 ban 转发机导致全池下线”：

1. 真实矿池入口只允许受信转发机通过私网、VPN 或固定白名单访问。
2. 后端监听端口强制要求 PROXY protocol，不做“有就解析、没有也接受”的自动嗅探。
3. 系统永久区分“TCP 直接对端 IP”和“经验证的矿工 IP”。
4. autoban 控制面硬性禁止对任何受信转发机、VPN 网关、节点 RPC 地址下发 ban。

另外必须先接受一个现实：

> 隐藏真实矿池 IP 是“隔离源站、方便切换、降低绕过前置的风险”，不是 DDoS 防护本身。攻击者仍然可以把转发机公网链路打满。超过接入带宽的攻击，只能靠运营商、云厂商 L4、Anycast 或清洗中心解决。

## 一、按“严重程度 × 概率”排序

| 优先级 | 风险 | 典型后果 | 主防线 |
|---|---|---|---|
| P0 | UDP 反射、SYN/ACK flood、带宽型攻击 | 转发机链路直接打满 | 上游 DDoS 清洗、云 L4、运营商 |
| P0 | PROXY 信任边界错误、ban 到转发机 | 一名坏矿工导致全池掉线 | 源 IP 模型、受信代理保护规则 |
| P0 | RandomX、Equihash 等无效 share CPU flood | 验证线程耗尽，正常 share 超时 | 应用层限额、隔离验证池、 bounded queue |
| P0 | 无上限连接、消息队列、日志、输出缓冲 | FD、内存、磁盘或 event loop 被打爆 | HAProxy + 应用层硬上限 |
| P0 | CGNAT、矿场代理、NiceHash 出口被误 ban | 大批正常矿机连坐 | 会话优先、IP 后置、信誉分和影响面保护 |
| P1 | TLS 握手、频繁重连、subscribe 不 authorize | CPU、连接槽位耗尽 | 前置限速、握手超时 |
| P1 | 源站 IP 泄漏、绕过 HAProxy | 防护全部失效 | 源站网络 ACL、独立节点和出口 |
| P1 | 多转发机单点、DNS 切换无效 | 区域矿工长时间离线 | 多入口 + 矿工备用池配置 |
| P1 | share 重放、重复计费 | 统计、结算被污染 | 全局 share 指纹、幂等性 |
| P2 | block withholding、BGP/DNS 劫持 | 长期经济损失或算力被盗 | 统计检测、TLS、RPKI、入口监控 |

---

# 1. 攻击类型全景：谁来防

## 1.1 L3/L4 攻击

| 攻击 | 主要耗尽资源 | 池应用能否防 | 正确防线 |
|---|---|---:|---|
| UDP DNS/NTP/CLDAP/Memcached 反射 | 公网带宽、路由器 PPS | 不能 | 运营商、云清洗、Anycast |
| SYN flood | SYN backlog、conntrack、PPS | 基本不能 | 上游 SYN proxy、边缘防火墙 |
| ACK/RST/异常 TCP 包 flood | PPS、状态表 | 不能 | 上游清洗、XDP/eBPF、防火墙 |
| 完成三次握手后的连接洪水 | FD、HAProxy 连接槽、后端端口 | 部分 | HAProxy、内核、应用共同限流 |
| TLS handshake flood | TLS CPU、内存 | 后端不应承担 | 在边缘终止 TLS、限制握手速率 |
| 慢连接、半死连接 | FD、buffer、goroutine/task | 能 | 前置超时 + 应用状态机 |
| 慢读客户端 | 服务端发送缓冲、job fanout | 能 | 输出 buffer 上限、写超时 |
| DNS/BGP 劫持 | 矿工被重定向 | 不能单独防 | TLS 验证、RPKI、DNS 安全 |

如果转发机只有 1 Gbps 上行，攻击流量达到 10 Gbps，本机 iptables 即使把包全部丢掉也没有意义——1 Gbps 链路在包到达 iptables 以前就已经满了。AWS 的官方防护模型也把 SYN、UDP flood 等放在网络边缘处理，而把应用特有行为交给更高层规则，这正是矿池应采用的分层方式。[AWS DDoS 防护分层说明](https://docs.aws.amazon.com/decision-guides/latest/waf-or-shield/waf-or-shield.html)

## 1.2 L7/Stratum 协议攻击

| 攻击 | 正确处理 |
|---|---|
| 超长 JSON、深度嵌套、巨型数字、永不换行 | 在分配大 buffer、解析 JSON 以前按字节数截断 |
| 非法 JSON flood | 小样本允许协议错误；连续错误立即断开并计入信誉 |
| subscribe 后不 authorize | 保持最小状态，10–15 秒未完成登录即断开 |
| 未 authorize 狂发 submit | 先返回一次协议错误，继续发送则断开并短期 ban |
| 大量随机 nonce、无效 PoW | 每会话验证预算、算法隔离队列、连续 badpow 快速断开 |
| 合法但低难度 share flood | 立刻提高下一 job 难度；不要因为池自己分配的低难度去 ban |
| stale flood | 通常是网络、旧 job 或客户端问题，不能仅凭 stale 比例 ban IP |
| duplicate/replay | 确定性 share 指纹、跨实例短期去重 |
| 假爆块 | 池端独立重算 PoW、网络 target、coinbase、merkle root；客户端声明没有任何特权 |
| job 抢跑/偷 job | job 本身通常不是秘密；重点防跨会话 extranonce、job 重放和链路劫持 |
| block withholding | 标准 Stratum 很难证明，只能长期统计，不能靠普通 invalid-share ban |
| 日志洪水 | 聚合计数、限频采样，绝不逐条记录完整垃圾 payload |
| auth/address 查询洪水 | 地址验证缓存，不因每次失败查询数据库或节点 RPC |

“伪造爆块”应当在架构上根本不存在：是否是 block candidate，必须由服务端完成 PoW 后得出，不能相信客户端字段。即使是 candidate，也要经过完整区块构造校验后才允许进入节点提交快速通道。

---

# 2. 自动 ban 的可落地规则

以下是上线初始值，不是协议标准。假设：

- 普通直连矿工目标 share 间隔为 10–20 秒；
- 大型矿场代理、NiceHash、MRR 使用独立 profile；
- 先用 24–72 小时 shadow mode 观察正常流量的 p99；
- 告警可以设在正常 p99 的约 3 倍，限流约 5 倍，真正 IP ban 必须再叠加语义证据。

## 2.1 连接和协议边界

| 项目 | 建议初始值 | 超限动作 |
|---|---:|---|
| TLS handshake | 5–8 秒 | 断开；重复才计信誉 |
| 首条完整 Stratum 消息 | 3–5 秒 | 断开 |
| subscribe + authorize/login 总时间 | 10–15 秒 | 断开，不直接 ban |
| 单条客户端消息 | 方言上限 16–32 KiB；绝对上限 64 KiB | 立即断开 |
| JSON 嵌套深度 | 8–16 层 | 断开 |
| 未授权累计输入 | 16–64 KiB | 断开 |
| 未授权消息数 | 5–10 条 | 断开 |
| 拼接半条消息的最长时间 | 5–10 秒 | 断开 |
| 拼接半条消息时的最低速率 | 2 秒宽限后 100–500 B/s | 防 Slowloris；仅作用于未完成消息 |
| 单 IP 新连接 | 持续 1–2 次/秒，burst 10–20；约 30–60 次/分钟 | 先限速，不立即 ban |
| 单 IP 未授权并发 | 10–20 | 拒绝新增 |
| 单 IP 已授权并发 | 50–100 | 进入观察；CGNAT/矿场单独配置 |
| TLS 握手速率 | 2–5 次/秒，burst 20 | 边缘限流 |
| 未授权消息速率 | 5 次/秒，burst 10 | 超出即断开 |
| 授权后非 submit 控制消息 | 10 次/秒，burst 20 | 限流/断开 |
| 慢读客户端输出 buffer | 64–256 KiB | 丢弃可合并旧 job；继续增长则断开 |
| 无写入进展 | 15–30 秒 | 断开 |

不要给整个矿池一个统一的消息大小：自定义 Equihash、Hush 或魔改链的 solution、job 字段可能更大。应当按方言和币种配置，但所有配置上面再加一个全局绝对上限。

## 2.2 share 速率和验证预算

对目标 share 间隔 10–20 秒的普通连接：

- 正常持续 submit 应远低于 1 次/秒。
- 可以允许 0.5–1 次/秒的持续预算。
- 为网络恢复、GPU 批量提交预留 64–256 条 burst queue。
- 大型代理可以提高到 256–1024 条，但必须绑定明确的 proxy/farm profile。
- CPU 昂贵算法每会话同时验证建议限制为 2–8 个，等待队列 32–128 个。
- 全局队列必须按“最多能处理 1–2 秒的验证量”确定，不能无限堆积。

关键策略是“先证明正常，再扩大预算”：

1. 新连接只获得较小验证预算。
2. 前几条 share 如果是有效 PoW，只是 stale 或暂时 low difficulty，说明客户端确实做过工作，可以放宽 burst，并立刻调整难度。
3. 如果前 10 条连续都是 badpow，再继续付出几十、几百次 RandomX 验证成本没有意义，直接断开。
4. 大量有效 share 是 vardiff 问题，不是攻击证据。
5. 大量随机无效 share 才是 CPU flood 证据。

## 2.3 具体触发规则

| 信号 | 建议阈值 | 第一动作 | IP ban 条件 |
|---|---|---|---|
| 非法 JSON | 连续 3 次，或 60 秒内 5 次 | 断开 | 10 次/分钟或重复会话：10 分钟 |
| 超长消息 | 1 次超过绝对上限 | 立即断开 | 10 分钟内 2 次：1 小时 |
| 未授权 submit | 第 1 次返回错误；10 秒内 3 次 | 断开 | 1 分钟内 10 次：1 小时 |
| authorize/login 失败 | 10 次/分钟或 30 次/10 分钟 | 限速 | 重复无成功登录：10 分钟 |
| badpow | 连续 10 次，或至少 20 条中超过 50% | 会话断开 | 同 IP 至少 3 个会话重复：1 小时 |
| low difficulty | 难度切换宽限结束后，至少 20 条中超过 80%，或 30 秒 50 条 | 断开/重发新 job | 单独不做长 ban；重复会话可 10 分钟 |
| duplicate | 至少 50 条中超过 20%，或 10 秒内 100 条相同重放 | 限流 | 多会话有组织重放：10 分钟至 1 小时 |
| stale | 至少 50 条中超过 90%，持续 2–5 分钟 | 断开重连 | 绝不能仅凭 stale 比例 ban IP |
| 重连洪水 | 60 次/分钟且几乎没有 productive 会话 | 限流 | 10 分钟内数百次且无成功挖矿：10 分钟 |
| subscribe 不 authorize | 10–15 秒 | 断开 | 同 IP 大规模重复才 ban |
| authorize 后不挖 | `max(5 分钟, 20 × 目标 share 间隔)` | 断开 | 通常不 ban |
| 已稳定挖矿后完全空闲 | 20–30 分钟 | 断开 | 不 ban |
| 合法 share 速率过高 | 超出目标数倍 | 提高 difficulty | 不 ban |

必须加 3 个全局熔断条件：

- 如果多个 ASN、多个 IP、多个矿工版本同时出现 badpow，优先判断 job、算法、端序或新版本部署错误，暂停 share 类 autoban。
- 如果全池 stale 同时上升，优先检查节点、模板更新、跨区网络和 clean job，不得批量 ban。
- 如果某条新规则 5 分钟内准备断开超过 0.5%–1% 的在线会话，或影响超过约 1% 的池算力，自动停止向防火墙下发并报警。

## 2.4 ban 时长

推荐阶梯：

1. 第一次：10 分钟。
2. 24 小时内再次触发：1 小时。
3. 第三次：6 小时。
4. 持续重复：24 小时。
5. 长期明确恶意：7 天。
6. 自动永久 ban：原则上不要做。

动态 IP、移动网络、VPN、CGNAT 都可能换人。所谓“永久 ban”应仅用于：

- 人工确认的攻击基础设施；
- 明确的恶意代理、扫描器网段；
- 保留负责人、证据、工单和复核日期；
- 最好仍按 30–90 天复审，而不是永不解除。

自动解封后进入 30–60 分钟 probation：连接和 burst 配额降低，但只要产生正常有效 share，信誉逐步恢复。

## 2.5 信誉分

可以采用一个初始模型：

- 非法 JSON：+10
- 未授权 submit：+20
- 超长帧：+50
- badpow：+5
- 非协议重放 duplicate：+2
- 高频登录失败：+5
- 正常时间衰减：15–30 分钟半衰期

动作阈值示例：

- 30 分：限速、降低验证预算。
- 50 分：断开并应用层 ban 10 分钟。
- 100 分：向前置下发 1 小时 edge ban。

注意：

- 钱包地址通常是公开的，Stratum password 经常也不是真正密码，攻击者可以拿受害人的地址来发垃圾。不能仅凭地址把“账户”永久封掉。
- TLS 指纹、User-Agent、ASN、地区只能作为软信号，不能单独 ban。
- 一个 IP 可能是整座矿场、CGNAT 或 xmrig-proxy。share 语义攻击应优先封会话、worker、已认证代理身份，最后才封 IP。

## 2.6 白名单、CIDR 和持久化

至少拆成 4 类列表：

1. **Trusted forwarder**：允许提供 PROXY 地址，但不免除协议校验。
2. **Managed miner/farm**：提高连接和 burst 配额，但不跳过 PoW。
3. **Temporary allowlist**：事故期间保护重要矿场，有负责人和过期时间。
4. **Denylist**：带范围、原因、证据、TTL 和策略版本。

“受信转发机”与“矿工白名单”绝不能共用一个概念。

存储建议：

- Redis：实时计数、信誉分、TTL ban。
- PostgreSQL：人工 ban、长期 ban、不可变审计和策略版本。
- nftables/ipset/eBPF、HAProxy runtime map：只是边缘派生状态，不是最终真相。
- HAProxy stick-table 适合本机或 peers 间短期速率计数，但不适合作为唯一持久审计源。HAProxy 本身支持连接数、连接速率和通用计数器等 stick-table 维度。[HAProxy 配置手册](https://docs.haproxy.org/2.8/configuration.html)

每条 ban 至少包含：

- IP/CIDR、IPv4/IPv6；
- scope：币种、端口、区域或全部 Stratum；
- 原因码和证据摘要；
- 首次、最后触发时间；
- 生效和过期时间；
- 置信度、升级级别；
- 来源实例、策略版本；
- 自动或人工、操作人；
- 命中次数；
- `verified_client_ip` 的来源证明。

重启时从中央 desired state 重建边缘集合。Redis/控制面故障时，应继续执行本地限额和最后一次快照，但不要因为控制面不可用而拒绝所有矿工。

---

# 3. 转发后如何拿真实 IP

## 3.1 PROXY protocol v1/v2

基本流程是：

> 矿工 TCP 连接  
> → HAProxy 看到矿工公网源地址  
> → HAProxy 新建后端连接  
> → 在后端数据流最前面写入 PROXY v2 头  
> → 矿池先验证直接对端确实是受信 HAProxy  
> → 解析 PROXY 头得到矿工地址  
> → 再解析 TLS 或 Stratum 数据

v1 是 ASCII 文本；v2 是二进制，IPv6 解析更高效，并支持 TLV。自建新系统建议统一 v2。HAProxy 官方文档明确提供 `send-proxy-v2` 语义，也说明后端必须理解该版本。[HAProxy 配置手册](https://docs.haproxy.org/2.8/configuration.html)

安全模型应保存这些不同字段：

| 字段 | 含义 | 能否用于矿工 ban |
|---|---|---:|
| `transport_peer_ip` | TCP socket 看到的直接对端，通常是 HAProxy | 不能 |
| `proxy_reported_ip` | PROXY 头里声称的地址 | 尚不能 |
| `verified_client_ip` | 确认直接对端受信后接受的地址 | 可以 |
| `ip_provenance` | DIRECT、PROXY_V1、PROXY_V2 | 决策依据 |
| `proxy_id` | 哪个区域、哪台前置提供的地址 | 审计和追踪 |

信任边界必须这样落地：

1. 源站防火墙只允许转发机的私网/VPN 地址访问 Stratum backend。
2. backend listener 必须收到合法 PROXY 头，否则直接拒绝。
3. 只在 `transport_peer_ip` 属于 trusted proxy 集合时，才生成 `verified_client_ip`。
4. 公网直连端口和 PROXY backend 必须是两个独立监听，不能混用。
5. 多跳代理时，每一跳只信任自己的直接上游。
6. 转发机扩缩容时，信任集合应精确到代理身份或隧道身份，不应直接信任某个云厂商的大 CIDR。
7. PROXY CRC32C 只能检测传输错误，不能证明发送者身份。

PROXY protocol 官方规范甚至明确要求接收端不要猜测头是否存在，并要求访问过滤，只允许受信代理使用，否则攻击者可以伪造源地址。[PROXY protocol 规范](https://www.haproxy.org/download/3.1/doc/proxy-protocol.txt)

绝对不要在公网端口实现：

> “如果开头像 PROXY 就解析，否则按普通 Stratum 处理。”

否则攻击者可以自己发送一个伪造的 PROXY 头，把垃圾行为栽赃给任意矿工 IP。

### 与 TLS 的顺序

- TLS passthrough：HAProxy 发出的 PROXY 头在 TLS ClientHello 之前。后端必须先消费 PROXY 头，再把余下字节交给 TLS。
- TLS 在 HAProxy 终止：HAProxy 后端可以传明文 Stratum，但应走私网、WireGuard 或再用 mTLS。
- 如果 TLS 库直接接管 socket 而没有先解析 PROXY 头，它会把 PROXY magic 当成非法 TLS 数据，连接全部失败。
- 健康检查也必须带正确的 PROXY 头，或者使用完全独立的健康检查端口。HAProxy 文档特别提示了某些自定义 health-check 地址需要单独启用 PROXY 发送。[HAProxy PROXY 配置说明](https://docs.haproxy.org/2.8/configuration.html)

NGINX stream 能接收 PROXY v1/v2，并暴露原始客户端地址；官方文档说明 v2 接收支持始于 1.13.11。[NGINX stream 文档](https://nginx.org/en/docs/stream/ngx_stream_core_module.html) 但 NGINX 向上游发送的具体版本、版本控制能力要按实际构建版本验证，不能想当然地认为一定发 v2；这也是我更偏向 HAProxy 的原因之一。

## 3.2 没有 PROXY 头时还能不能拿到真实 IP

分情况：

### 纯 TCP 代理、socat、自建 connect-and-copy

拿不到。

这类代理接受矿工连接后，重新建立一条到后端的 TCP 连接。后端只可能看到代理源 IP，原始源地址已经不在新 TCP 四元组里。Stratum 数据中也没有可信的通用“真实 IP”字段。

HTTP 的 `X-Forwarded-For` 不适用于原始 Stratum TCP；硬塞进去会破坏协议，而且矿工可以伪造。

### iptables DNAT，不做 SNAT

可以保留源 IP，但必须满足：

- 数据包本身被路由到后端，而不是重建 TCP 连接；
- 回程必须经过同一转发机；
- 后端必须有正确的策略路由；
- 跨公网、跨云时通常需要 GRE/IPIP/WireGuard 等隧道承载并保持内层源地址。

### DNAT + SNAT/MASQUERADE

后端拿不到。SNAT 已经把源地址真正改掉。

### 其他替代方案

- DSR；
- Linux TPROXY；
- HAProxy transparent source；
- GRE/IPIP 隧道保留源地址；
- 支持 source preservation 的云 L4。

这些方案能避免 PROXY 头，但策略路由、回程、内核权限和故障排查明显更复杂。除非团队有成熟网络经验，否则 HAProxy + PROXY v2 + 私有隧道更稳。

如果暂时没有可信真实 IP：

- 池应用必须禁用 IP autoban；
- 只能按会话、worker、已认证代理身份处理；
- IP 连接速率和并发必须在真正看到源 IP 的转发机上执行；
- 池端可以把“关闭哪条 edge connection”的决定回传给转发机，而不能凭后端 socket IP 封禁。

## 3.3 你们之前连坐事故的根治

你们现在改成 HAProxy + PROXY protocol，属于正确且务实的根治方向。还要再补这些硬保护：

- ban 引擎的输入类型只能是 `verified_client_ip`，不能接受任意 socket peer。
- 任意 ban 目标若命中 trusted forwarder、VPN、节点、监控或管理 CIDR，控制面拒绝执行并触发 P0 告警。
- PROXY 解析失败只能记为“代理链路错误”，绝不能退化成“把代理 IP 当矿工 IP”。
- 如果某个入口突然有 99% 的矿工都显示成同一个 IP，应立即判为 PROXY 链路失效，而不是开始 ban。
- 每次部署都做一次回归：模拟一个坏矿工，验证只有该 `verified_client_ip` 被断开，转发机和其他矿工不受影响。
- 如果 HAProxy 前面还有云 L4，而云 L4 做了 SNAT，则云 L4 也必须提供可信源地址；否则 HAProxy 看到的仍只是云 L4。多跳 PROXY 链必须逐跳传递原始地址。

剩余隐患是：HAProxy 一旦被攻陷，它作为受信代理可以伪造任意矿工 IP。因此转发机上不能放钱包、节点 RPC 权限或数据库凭据；它对后端只应拥有 Stratum 数据面的最小访问权限。

---

# 4. ban 应该在哪一层执行

| 层 | 适合处理 | 不适合处理 |
|---|---|---|
| 运营商/云清洗 | 带宽型、UDP 反射、SYN/ACK、超大 botnet | Stratum 语义 |
| XDP/eBPF/nftables/ipset | 已确认恶意 IP、包速率、连接前丢弃 | invalid share、stale、账户行为 |
| HAProxy | 连接速率、并发、TLS 握手、慢连接、动态 ACL | 深度 PoW 语义 |
| Stratum gateway | 状态机、JSON、authorize、job、share 语义 | 已打满公网链路的攻击 |
| PoW validator | 独立验证和公平调度 | 连接管理、永久 ban |
| Reputation service | 跨实例关联、TTL、信誉和策略 | 直接处理高频网络包 |

原则是：

- 应用层最懂“为什么坏”；
- 前置层最适合“廉价地丢”；
- 上游最适合“在链路进来以前丢”。

建议联动流程：

1. 应用立即关闭当前会话，避免等待全局决策。
2. 应用发布带证据的 `BanRecommendation`，包含真实 IP、scope、TTL、原因、置信度、策略版本和 proxy provenance。
3. 中央信誉服务检查 allowlist、trusted proxy、影响会话数、影响算力和策略爆炸半径。
4. 高置信度决定通过 mTLS 或签名消息发给所有入口。
5. 边缘 agent 更新 HAProxy runtime map 和带 TTL 的内核集合。
6. agent 返回版本和 ACK。
7. 中央持续 reconciliation，确保重启、漏消息和解封后最终一致。

不要让池应用通过 SSH 到转发机逐条执行防火墙命令。这样难以幂等、难以审计，也等于把防火墙管理权限交给公网协议进程。

分层建议：

- 单个矿工偶发 badpow：应用层处理就够。
- 某 IP 高频重连、非法 JSON、超长帧：下发 HAProxy 或防火墙。
- 成千上万 IP 的 botnet：不要无限扩张 denylist，交给上游基线和 DDoS 清洗。
- 语义置信度不足：只封会话，不下发 edge ban。
- 需要立刻断开已有连接时，除了更新防火墙，还要让 HAProxy 或应用主动终止现有 session；只禁止新连接往往不够。

---

# 5. 转发/反代层怎样做稳

## 5.1 选型

| 方案 | 优点 | 缺点 | 建议 |
|---|---|---|---|
| HAProxy | L4 成熟；明确支持 PPv2；stick-table、ACL、健康检查、平滑 reload 强 | 配置和容量调优需要经验；不懂 Stratum 语义 | 自建首选 |
| NGINX stream | 简单稳定；TLS、PROXY、TCP 转发成熟 | 动态信誉、连接计数和运行时控制不如 HAProxy 顺手 | 简单转发可用 |
| 自建 Go TCP proxy | 可做协议嗅探、连接 ID、精确联动 | 要重新解决 backpressure、半关闭、buffer、FD、GC、零停机和攻击面 | 放在 HAProxy 后，不建议直接裸露公网 |
| 云厂商 L4 | 大带宽、Anycast、自动清洗、健康检查 | 成本；长连接 timeout；源 IP/PROXY 行为和端口限制各家不同 | 有预算时放最前面 |
| 普通 HTTP CDN/WAF | 对网站有效 | 通常根本不代理原始 Stratum TCP | 不可当 Stratum DDoS 方案 |

推荐路径：

> 具备 DDoS 能力的云 L4/Anycast  
> → 区域 HAProxy  
> → WireGuard/私网  
> → Stratum gateway

## 5.2 长连接的关键坑

### timeout

HAProxy 的 client/server timeout 是“不活动超时”，不是绝对连接寿命；官方也建议 TCP 模式下两侧保持一致。[HAProxy timeout 说明](https://docs.haproxy.org/2.8/configuration.html)

初始建议：

- backend connect timeout：2–5 秒；
- PROXY 头等待：2–3 秒，私网通常更短；
- TLS handshake：5–8 秒；
- client/server inactivity：30–60 分钟；
- TCP keepalive：空闲 60–120 秒后开始，3–5 次探测；
- 半关闭连接：单独设置较短的 FIN timeout；
- 应用层另有 authorize/no-share/idle 规则，不要完全依赖 TCP inactivity。

池可以每 30–90 秒发送非 clean 的 job 重发或轻量保活，但必须做矿工兼容性回归，不能每次都让矿工清空工作。

### FD、内存和 conntrack

正常高峰最好只使用经过压测容量的 40%–50%：

- 60% 告警；
- 70% 开始拒绝低信誉新连接；
- 85% 硬性 shed load；
- productive 老连接优先于新连接。

需要同时测：

- 每条明文连接内存；
- 每条 TLS 连接内存；
- 两侧 socket 数；
- conntrack；
- HAProxy 线程；
- reload 后旧 worker 持有的长连接；
- 日志系统写入能力。

### 后端源端口耗尽

这是长连接转发特别容易漏掉的限制：

> 一个转发机源 IP 到一个固定 backend IP:port，能使用的本地源端口数量大致只有几万，理论上也不会超过约 64K。

即使 HAProxy 的 FD 足够，单个四元组也可能先耗尽。解决办法：

- 多个转发机源 IP；
- 多个 backend IP；
- 多个 backend 端口；
- 多实例分片；
- 监控 ephemeral port、TIME_WAIT 和连接失败原因。

不要把 10 万条矿工连接都从一个源 IP 打到同一个后端端口。

### job/extranonce 唯一性

多入口、多 gateway 时：

- `extranonce1` 必须包含实例/区域唯一空间；
- job ID 应包含实例 epoch，重启后不能复用旧 ID；
- share 指纹必须包含币种、模板、prevhash、job、extranonce、nonce、ntime、version/solution；
- duplicate 去重不能只存在当前 TCP session 内，否则跨区重放可能被重复计费。

## 5.3 转发机被打后的切换

推荐：

- 至少 2–3 个区域入口；
- 尽量跨供应商、跨 ASN；
- DNS health check + 30–60 秒 TTL；
- 明确发布 primary 和 backup pool 地址；
- 大矿场使用多个入口配置或本地 farm proxy；
- 入口故障时先停止接新连接，已有连接尽量 drain。

“完全无感切换”基本做不到：

- DNS 只影响下一次解析；
- 现有 TCP 不会迁移到新 IP；
- 很多矿工缓存 DNS；
- `client.reconnect` 在不同矿工和 ASIC 固件上的支持不一致，不能作为唯一方案；
- Anycast 路由变化也可能重置长连接。

托管 Anycast 可以缩短恢复，但自己搭 BGP Anycast 对长期 TCP 状态要求很高，路由抖动会导致大量连接重置。具体稳定性取决于供应商，必须实测。

可以给少量战略矿场提供不公开、带 VPN/mTLS 或固定授权的备用入口。但“秘密 IP”不能代替真正清洗能力。

## 5.4 防止真实矿池 IP 泄漏

最重要的不是“不让别人知道”，而是“即使知道也无法直连”。

必须做：

- 源站 Stratum 公网 ACL 默认拒绝，只允许边缘隧道地址。
- 网站/API、Stratum、节点 P2P、节点 RPC、监控、管理面使用不同网络边界。
- 节点 P2P 使用独立公网 IP；真实矿池通过私网 RPC 访问节点。
- 区块广播由独立节点网络完成，避免 P2P first-seen 暴露 Stratum origin。
- 源站出网使用独立 NAT，不让其公网地址出现在 webhook、软件更新、监控回调和节点 peer 中。
- DNS 不发布 origin A/AAAA。
- 错误响应不返回 backend hostname、私网拓扑、RPC 地址。
- TLS 证书只使用公开 edge hostname；证书透明度日志中不要出现 origin 专用名称。
- 边缘机不允许访问钱包、数据库管理端口或节点管理 RPC。
- 定期从外网扫描 origin，确认所有非 edge 来源都无法连接。
- 独立保护 DNS 注册商、API key、MFA 和 registry lock。

爆块数据本身一般不会直接包含矿池服务器 IP；真正容易暴露的是节点 P2P 广播、共用公网出口、DNS、监控和错误日志。

---

# 6. TLS Stratum 值不值得上

值得，但定位要正确。

TLS 能防：

- 矿工钱包地址、worker、密码被被动监听；
- 中间人修改 authorize、job 或服务器响应；
- BGP、Wi-Fi、运营商链路上的简单 Stratum 劫持；
- 连接到假池时，通过证书验证识别错误服务器。

TLS 不能防：

- UDP/SYN/带宽 DDoS；
- 恶意矿工主动发送垃圾；
- TLS handshake flood；
- 源 IP 暴露；
- 被攻陷的 HAProxy；
- miner 本身不验证证书。

建议：

- 在边缘终止 TLS；
- edge → origin 走 WireGuard、私网或 mTLS；
- 支持 TLS 1.2/1.3；
- 禁用 TLS 1.3 early data/0-RTT 处理 authorize 和 submit，避免重放语义；
- 为旧 ASIC 保留独立明文端口，但把它当兼容降级；
- TLS 端口可以提供在 443 等常见端口，但仍是原始 TCP，不是 HTTP；
- 对 TLS 握手单独限速；
- 测试证书轮换、SNI、系统时间错误和完整证书链。

不少老 ASIC 和第三方固件虽然宣称支持 TLS，但证书验证、SNI、CA 更新行为不一致；具体兼容度属于“不确定”，必须逐机型实测。TLS 图标亮了不等于客户端真的验证了服务器身份。

---

# 7. “看起来合法”的捣乱连接

## 7.1 分阶段授予资源

建议连接状态和资源预算逐步升级：

> CONNECTED  
> → SUBSCRIBED  
> → AUTHORIZED  
> → 首个有效 share  
> → PRODUCTIVE  
> → TRUSTED

每升一级才增加：

- 并发验证额度；
- burst queue；
- 连接存活时间；
- worker 数量；
- job fanout；
- 边缘白名单资格。

Bitcoin V1 必须兼容先 subscribe 后 authorize，但 subscribe 阶段只分配轻量 session 和 extranonce，不写数据库、不进入大规模 job fanout。

## 7.2 校验顺序

每条 submit 按成本从低到高：

1. 消息长度、JSON 类型和字段数量。
2. method 是否允许、状态机顺序是否正确。
3. worker/session 是否已 authorize。
4. job 是否属于当前币种、端口、会话和有效 epoch。
5. nonce、ntime、version、extranonce、solution 长度与范围。
6. 精确 duplicate/replay 检查。
7. PoW 验证。
8. assigned share target。
9. network target。
10. 完整 block candidate 验证。
11. 节点提交。

绝不能因为客户端声称“这是爆块”就跳过前面步骤，也不要让每个伪 candidate 都调用节点 RPC。

## 7.3 隔离昂贵算法

不同 PoW 验证池必须隔离：

- SHA256、Scrypt 等便宜验证不能与 RandomX/Equihash 共用一个不受限队列；
- 每币、每算法有独立 concurrency 和 queue；
- 按会话、账户进行公平调度，不能让一个 IP 塞满整个全局队列；
- validator 进程异常不能拖死 Stratum event loop；
- 队列满时先暂停读取或拒绝低信誉连接，不应继续堆内存。

## 7.4 高频重连

频繁重连的成本包括：

- TCP/TLS 握手；
- 地址验证；
- session/extranonce 分配；
- DB/Redis 查询；
- job 序列化和发送；
- 日志。

处理方式：

- 边缘连接 token bucket；
- 登录和地址验证短期缓存；
- 失败连接不持久化完整 session；
- 不对垃圾连接逐条写数据库；
- 对成功 productive 会话提高信誉；
- 矿场断电恢复导致的全局重连风暴只做排队和限速，不应大规模 ban。

## 7.5 block withholding

这是最难的“完全合法外观”攻击：

- 攻击者持续提交正常低难度 share；
- 找到 network target 时故意不提交；
- 所有协议、PoW、连接行为都可以正常；
- 标准 Stratum 下，池通常无法证明某个矿工曾找到并丢弃某个块。

只能统计矿工累计工作量对应的期望块数。例如期望找到 10 个块却一个都没有，在简单 Poisson 假设下概率约为 `e^-10`，大约 0.0045%。但这是统计示例，不是自动定罪标准，还涉及多重检验、算力波动、矿工切换和池本身的 luck。

可落地做法：

- 对高 PPS 敞口的大矿工记录 difficulty-weighted expected blocks；
- 采用长期 Bayesian/SPRT 异常检测；
- 异常时先限制 PPS 敞口、要求更强身份、转为风险较低的结算条件；
- 不要因为短期倒霉直接扣款或封 IP；
- 钱包/IP 都可能变化，需要跨时间行为关联；
- 是否为故意攻击往往“不确定”，运营和法律判断要与统计结论分开。

---

# 8. 长期运营的完整参考架构

## 8.1 数据面

> 矿工  
> → 权威 DNS / primary + backup  
> → 云 L4 / Anycast / DDoS 清洗  
> → 区域边缘防火墙  
> → HAProxy：TLS、真实源 IP、连接限速、PROXY v2  
> → WireGuard/私网  
> → Stratum gateway：协议状态机、authorize、信誉  
> → bounded share queue  
> → 按算法隔离的 PoW validator  
> → block candidate 快速通道  
> → 独立 node gateway / 节点 RPC

## 8.2 控制面

> 边缘和应用 telemetry  
> → Reputation/Policy service  
> → Redis 热状态 + PostgreSQL 审计  
> → 带 TTL、签名、版本号的 ban 决策  
> → 各区域 edge agent  
> → HAProxy runtime map + nftables/ipset/eBPF

## 8.3 必须监控的指标

边缘：

- bps、pps、SYN rate；
- 当前连接、新建连接、TLS handshake；
- 每 IP/ASN 连接分布；
- PROXY 缺失、非法头、版本错误；
- backend connect failure；
- 源端口和 conntrack 使用率。

应用：

- CONNECTED、SUBSCRIBED、AUTHORIZED、PRODUCTIVE 数量；
- JSON 错误、未知 method、登录失败；
- badpow、low diff、duplicate、stale；
- 每算法验证耗时和 queue depth；
- job 延迟、节点模板延迟；
- block candidate 和节点提交结果。

防护：

- 各策略 ban 数和命中数；
- 每次规则影响的会话、worker、算力；
- trusted proxy 命中保护次数；
- ban 下发延迟和各 edge 版本；
- origin 上的非 edge 直连尝试。

几个关键报警：

- 大量矿工地址突然“坍缩”为同一个转发机 IP；
- 新部署后全池 badpow 同时上升；
- 全池 stale 同时上升；
- 5 分钟内 ban 超过 0.5%–1% 在线会话；
- 某个 ban 目标属于 trusted proxy、内部 CIDR 或节点；
- validator queue 上升但边缘流量正常；
- 日志量比消息量增长更快。

还应准备一个“一键停止自动向前置推 ban”的 kill switch。触发后：

- 已有本地协议校验继续；
- 应用仍可关闭当前恶意 session；
- 暂停新的跨区域 IP ban；
- 不影响正常 authorize、share 和 block submit。

---

# 9. 行业案例和教训

## BTC Guild，2013 年 DDoS

公开报道显示 BTC Guild 曾因 DoS 数小时不可用，池算力一度降到正常水平的约四分之一；只有使用未公开私有服务器的部分矿工仍能工作。[Heise 对 BTC Guild 事件的报道](https://www.heise.de/news/Groesster-Bitcoin-Pool-von-Hackerangriffen-geplagt-1969644.html)

教训：

- 备用入口确实能降低单点影响；
- 但秘密 IP 只是临时提高攻击门槛；
- 大矿工必须提前配置备用连接；
- DNS 临时切换不能替代 miner-side failover；
- 公共和私有入口应隔离，私有入口也要认证和限流。

一项针对 2011–2013 年 Bitcoin 生态的研究从公开材料中整理出 40 个服务遭遇的 142 次独立 DDoS 事件，矿池和交易平台是重点目标。[Empirical Analysis of Denial-of-Service Attacks](https://fc14.ifca.ai/bitcoin/papers/bitcoin14_submission_17.pdf)

## 2014 年 BGP 劫持矿工流量

Dell 研究人员发现，攻击者通过 BGP 劫持把来自多个 ISP 的矿工连接转到恶意服务器，再利用 reconnect 行为把矿工引向攻击者控制的池；可观测损失约 8.3 万美元，峰值约每天 9000 美元。[WIRED 对 BGP 矿池劫持的报道](https://www.wired.com/2014/08/isp-bitcoin-theft/)

教训：

- 隐藏 origin、换端口、DNS failover 都防不了路由劫持；
- TLS 必须真正验证证书；
- 对自有 IP 前缀发布 RPKI ROA；
- 监控 BGP 路由变化；
- DNS、注册商和证书控制面与 Stratum 一样关键；
- 不应盲目信任服务器要求矿工永久改连任意新 hostname。

## Eligius，2014 年 block withholding

Eligius 运营方公开称有若干客户端实施或造成 block withholding，估计损失约 300 BTC；相关当事方对“故意攻击还是矿机基础设施 bug”存在争议，因此攻击意图部分应标为“不确定”。[Eligius 运营方公开说明](https://bitcointalk.org/index.php?topic=650831.0)

教训：

- IP、JSON、invalid share 防护完全发现不了这类行为；
- PPS/类 PPS 池承担的风险最大；
- 需要按贡献工作量计算期望块数；
- 统计异常只能触发调查和风险降额，不能轻率地永久 ban 或没收余额；
- 大矿工需要更强身份和合同条款。

## Flexpool，2021 年“看起来像 DDoS”的内部故障

Flexpool 曾公开说明，一次最初被认为是 DDoS 的大规模故障，最后确认是语言运行时/调度并发问题，而非外部攻击。[Flexpool 运营方事件说明](https://www.reddit.com/r/EtherMining/comments/mipgyc)

教训：

- 不要看到连接暴涨、延迟和 CPU 满就立即认定攻击；
- 无界队列、调度饥饿、GC、日志和锁争用能制造与 DDoS 几乎相同的症状；
- 攻击响应第一步应定位饱和层：带宽、SYN、FD、event loop、validator、数据库还是日志；
- “误判攻击然后批量 ban”可能把内部故障扩大成矿工事故。

公开矿池事故通常只披露“遭遇 DDoS”，很少公开包速率、攻击向量和完整 postmortem，因此对更多历史事件的具体根因判断大多“不确定”，不宜把论坛传闻当成架构依据。

---

最后压缩成你们第一阶段必须落实的 10 条：

1. 源站默认拒绝，只有 edge 私网/VPN 能访问。
2. HAProxy 统一发 PPv2，backend 强制 PP，不做可选嗅探。
3. 永久分离 `transport_peer_ip` 与 `verified_client_ip`。
4. trusted proxy 永远不能进入矿工 autoban 集合。
5. 边缘处理连接，应用处理语义，上游处理带宽。
6. 所有队列、buffer、日志、验证并发都有硬上限。
7. stale、low difficulty、短期 badpow 不直接触发 IP ban。
8. ban 带 TTL、证据、scope、策略版本和爆炸半径熔断。
9. 多 edge + 矿工 primary/backup；不要承诺 TCP 无感迁移。
10. TLS、独立节点网络、RPKI/DNS 安全共同防止算力被劫持。
