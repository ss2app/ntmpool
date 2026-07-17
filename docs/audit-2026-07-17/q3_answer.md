先给结论：不要试图做一个“万能 Stratum parser”，而应做成：

```text
TCP/TLS framing
  → 方言识别
  → 方言专用 Codec
  → 统一 Session / Job / Share 内部模型
  → 币种专用 Job Builder + PoW Validator
```

可以统一的是连接、限流、worker、job 生命周期、share 统计；不能统一的是 target/difficulty 解释、nonce 空间、job 字段、大小端和 submit 重建规则。

兼容性的最高优先级是：

1. job 绑定自己的难度，vardiff 切换后不误拒旧 job。
2. extranonce/nonceprefix 空间永不冲突。
3. 每个有 `id` 的请求都快速返回格式正确的响应。
4. `mining.configure`、未知扩展、探测失败不直接断连或封 IP。
5. 不把一个 TCP read 当成一条 JSON。
6. 方言和 coin/algorithm 由 endpoint 尽早确定，避免猜错后“假装成功”。

---

# 1. Stratum 方言全景图

## 1.1 对照表

| 方言 | 握手与客户端消息 | 服务端消息 | 难度/target | Job 核心字段 | Submit 核心字段 |
|---|---|---|---|---|---|
| Bitcoin Stratum V1 | `mining.configure`、`mining.subscribe`、`mining.authorize`、`mining.submit`；可选 `mining.extranonce.subscribe`、`mining.suggest_difficulty` | `mining.set_difficulty`、`mining.notify`、`mining.set_extranonce`、`mining.set_version_mask`、`client.reconnect` | `set_difficulty` 传 difficulty 数值；`notify.nbits` 是区块 target，不是 share target | `job_id, prevhash, coinb1, coinb2, merkle_branch[], version, nbits, ntime, clean_jobs` | `worker, job_id, extranonce2, ntime, nonce`；version rolling 后增加 `version_bits` |
| Bitcoin-like altcoin SV1 | 消息名通常同 Bitcoin | 通常同 Bitcoin | 可能有不同 `diff1 target`、difficulty multiplier | 通常相同，但 coinbase、header、merkle、PoS 字段可能魔改 | 通常 5 参数，但某些 fork 增加字段 |
| Equihash ZIP 301 | `mining.subscribe`、`mining.authorize`、`mining.submit`、可选 `mining.suggest_target` | `mining.set_target`、`mining.notify`、`client.reconnect` | 直接发送 256-bit big-endian target，避免 difficulty 换算歧义 | `job_id, version, prevhash, merkle_root, reserved, time, bits, clean_jobs` | `worker, job_id, time, nonce2, equihash_solution` |
| Zhash/Equihash 变体 | 大体继承 ZIP 301 | 在 `notify` 增加算法参数 | 通常仍是 full target | 常增加 `algo_type`、`personalization`，不同 `(N,K)` 的 solution 长度不同 | 与 ZIP 301 类似，但 solution 解析随参数变 |
| CryptoNote/Monero Stratum | `login`、`submit`、`keepalived`；登录可携带 `agent, rigid, algo, nicehash` | 登录响应内含首个 job；后续 `method: job` | job 内直接给 compact target；历史上常见 4-byte little-endian target，也有 8-byte 扩展 | `blob, job_id, target, id/session_id`，新版本常有 `height, seed_hash, algo` | `session id, job_id, nonce, result hash` |
| NiceHash CryptoNight/RandomX 扩展 | CryptoNote 登录，声明 NiceHash 能力 | CryptoNote job，但 blob 中为 marketplace/proxy 预留 nonce 字节 | 同 CryptoNote target | 必须给代理可分割的 nonce/blob 空间 | nonce 必须保留代理指定字节 |
| ethproxy / Ethereum GetWork 风格 | `eth_submitLogin`、`eth_getWork`、`eth_submitWork`、`eth_submitHashrate` | 返回或主动推送 work array | work 直接带 full target | `header_hash, seed_hash, target`；通常无 job id、无 clean_jobs | `nonce, header_hash, mix_hash` |
| EthereumStratum/1.0.0 | `mining.subscribe`，第二参数明确为 `EthereumStratum/1.0.0`；然后 authorize/submit | `mining.set_difficulty`、`mining.notify`、`mining.set_extranonce` | difficulty scalar，规范使用 Bitcoin diff1 换算 | `job_id, seed_hash, header_hash, clean_jobs` | `username, job_id, miner_nonce` |
| KawPow：Bitcoin-SV1 型 | 走 subscribe/authorize/submit | Bitcoin 风格 notify/set_difficulty | difficulty scalar | 很多 Ravencoin pool 使用 coinbase/merkle 型 Bitcoin job | 常见 Bitcoin 5 参数 submit，由池重建 header |
| KawPow：Eth/NiceHash 型 | subscribe 返回 nonceprefix | 通常为 Ethash/ProgPoW 派生 job | 可能是 difficulty，也可能直接 target | `job_id, header_hash, seed_hash/height, clean` 等；具体顺序不统一 | miner nonce，部分方言还带 mix hash |
| Stratum V2 | 二进制 `SetupConnection`、`OpenChannel` 等 | `SetTarget`、`NewMiningJob`、`SetNewPrevHash` | 直接对 channel 设置 target | Standard/Extended Channel、数字 job id、extranonce prefix | channel id、sequence、job id、nonce、ntime、version，Extended 另带 extranonce |

Equihash 的 ZIP 301 是少数正式写清 target、nonce 切分、error code 和 job 字段的 altcoin Stratum 规范。[Zcash ZIP 301](https://zips.z.cash/zip-0301)

EthereumStratum/1.0.0 的关键区别是：

- subscribe 必须带协议版本字符串。
- subscribe 返回 extranonce，但没有 Bitcoin 式 `extranonce2_size`。
- extranonce 规范最多 3 字节。
- difficulty 只对之后发送的 job 生效。
- notify 是 `job_id, seedhash, headerhash, clean_jobs`。[NiceHash EthereumStratum/1.0.0](https://github.com/nicehash/Specifications/blob/master/EthereumStratum_NiceHash_v1.0.0.txt)

这里的 Ethereum 方言现在主要用于 ETC、ETHW、Ethash/Etchash 派生链，不是已经转 PoS 的 Ethereum 主网。

## 1.2 几个最容易理解错的字段

### Bitcoin V1

`mining.notify` 中的 `nbits` 是网络区块 target 的 compact 表示。矿工提交普通 share 时，验证标准来自之前的 `mining.set_difficulty`，两者不能混用。

同一个 job 必须保存：

- 该 job 创建时的 share difficulty/target。
- extranonce1。
- extranonce2 size。
- version mask。
- block template id。
- clean epoch。

不能只在 session 上存“当前难度”。

### Equihash

Equihash 没有 coinbase extranonce2。它用 32 字节 block-header nonce：

```text
nonce = nonce1（池/代理分配） + nonce2（矿工搜索）
```

`nonce2` 长度必须严格等于 `32 - nonce1_length`。solution 还包含 canonical CompactSize 前缀；只检查 solution payload 而忽略长度前缀，会误拒 ASIC/GPU。

Zhash 等变种可能在 notify 后追加 `algo_type`、`personalization`。NiceHash 的 Zhash 规范就是这种扩展。[NiceHash Zhash 规范](https://github.com/nicehash/Specifications/blob/master/Zhash_NiceHash_v1.0.txt)

### CryptoNote

CryptoNote 并没有一个像 ZIP 301 那样长期统一、正式的规范。实际兼容基准基本是 XMRig、各主流 pool 和历史 nodejs-pool 行为。

典型流程：

```text
login
→ 登录响应包含 session id + 首个 job
→ server 推送 job
→ client submit
→ 可选 keepalived
```

必须支持的 job 字段至少包括：

- `blob`
- `job_id`
- `target`
- session `id`

RandomX 及魔改币常需要：

- `height`
- `seed_hash`
- `algo`
- 某些币的额外 seed 或 client nonce offset

target 的 4-byte/8-byte 表达不要做成全局判断，按 coin + agent compatibility profile 处理。

### KawPow

KawPow 没有一个被所有矿工、矿池严格共同遵守的唯一 wire spec，这是兼容工作的重灾区。

至少要准备两种 profile：

1. Bitcoin coinbase/merkle 型。
2. Ethash/NiceHash nonceprefix 型。

NiceHash 的 KawPow 规范明确要求：pool 给 NiceHash 的 nonceprefix 最多 0–2 字节，因为 NiceHash 自身还要增加一字节来分割下游矿工空间。[NiceHash KawPow 规范](https://github.com/nicehash/Specifications/blob/master/KawPow_NiceHash_v1.0.txt)

仅根据方法名 `mining.subscribe` 无法判断是哪一种。

## 1.3 哪些能合并，哪些不能

可以合并：

- TCP/TLS、LF/CRLF framing。
- JSON-RPC id correlation。
- session、authorize worker 集合。
- job cache、duplicate cache。
- vardiff 估算器。
- response timeout、metrics、限流。
- share 的异步验证和记账管线。
- Bitcoin 系同构 fork 的 coinbase/merkle 构造骨架。
- Ethash、Etchash、KawPow 的部分 DAG/seed 生命周期管理。

必须独立：

- Bitcoin V1 Codec。
- Equihash ZIP 301 Codec。
- CryptoNote Codec。
- ethproxy Codec。
- EthereumStratum Codec。
- KawPow 各主要 profile。
- Stratum V2 二进制 Codec。
- 每个算法的 PoW validator。
- 每种 header/coinbase/endianness 重建器。

内部统一对象应使用：

```text
ShareTarget：精确大整数 target
AssignedWork：该 job 的精确工作量
NonceSpace：结构化 nonce/extranonce 描述
JobValidity：clean epoch、prevhash、过期策略
```

不要把内部统一模型建立在 `double difficulty` 上。

---

# 2. 按端口还是第一条消息嗅探

## 推荐方案

**coin/algorithm 按 hostname + port 明确路由；只在同一 coin endpoint 内做有限的方言嗅探。**

例如：

```text
XMR RandomX         → CryptoNote 专用端口
ZEC Equihash        → ZIP 301 专用端口
RVN KawPow          → KawPow 主端口 + NiceHash 专用端口
BTC direct ASIC     → SV1 direct 端口
BTC proxy/rental    → 大 extranonce2、高初始 diff 专用端口
BTC SV2             → 单独二进制/TLS endpoint
```

### 按端口的优点

- 在发 subscribe response 前已经知道 coin 和协议。
- 可以为 direct ASIC、GPU、NiceHash 设置不同初始难度及 nonce 空间。
- 故障和 DDoS 隔离更容易。
- 不会把 Equihash 的 subscribe 当成 Bitcoin。
- 监控和兼容测试清晰。

缺点是端口多、运营说明复杂。

### 第一条消息嗅探能可靠识别的情况

| 第一条 method | 基本判断 |
|---|---|
| `login` | CryptoNote |
| `eth_submitLogin` | ethproxy |
| `mining.subscribe` 且参数带 `EthereumStratum/1.0.0` | EthereumStratum |
| `mining.configure` | Bitcoin/BIP310 可能性高，但不能单凭这一条确定 coin |
| 普通 `mining.subscribe` | 仍可能是 Bitcoin、Equihash、KawPow 或其他 SV1 变种 |

最大的问题是：Bitcoin、Equihash、KawPow 可能都先发普通 `mining.subscribe`，而矿工通常会等待 subscribe response 后才 authorize。池无法等地址出现后再决定，否则形成握手死锁。

因此“全币种共用一个端口、靠第一条 JSON 自动识别”不值得做。

## 矿工逐级回落探测时怎么回应

矿工可能依次尝试：

```text
EthereumStratum → ethproxy → 普通 Stratum
```

或：

```text
mining.configure → 普通 mining.subscribe
```

池端原则：

1. 已知但不支持的 extension，返回 `false`，不要断连。
2. 完全不匹配当前专用端口的方言，快速返回格式正确的错误，然后正常 close。
3. 不要静默 10–30 秒等 miner 超时。
4. 协议探测失败不计入 auth failure，不触发 IP ban。
5. 允许同一 IP 短时间快速重连多次。
6. 不要返回“看似 subscribe 成功、实际字段完全错误”的结果；这比明确失败更糟。
7. 同一个 TCP 包可能包含 configure、subscribe、authorize 多条消息，必须全部按 LF 切分处理。
8. 一条 JSON 可能被拆成很多 TCP packet，不能假设一次 read 就是一条消息。

错误后关闭时，先把 response flush 出去，再给一个很短的 graceful-close 窗口。直接 RST 会让部分矿工把它判断成网络故障，而不是协议不匹配。

---

# 3. ASIC 兼容关键点

## 3.1 `mining.configure` 与 BIP 310

`mining.configure` 应允许作为连接后的第一条请求。对每个 extension 返回：

- `true`：支持并启用。
- `false`：不支持。
- string：配置有问题。

未知 extension 不应导致整个 request 失败，更不能断连。[BIP 310](https://github.com/bitcoin/bips/blob/master/bip-0310.mediawiki)

建议支持：

- `version-rolling`
- `minimum-difficulty`
- `subscribe-extranonce`
- `info`

### version rolling

矿工提交：

- 自己支持的 mask。
- `min-bit-count`。

池返回：

```text
effective_mask = miner_mask & server_safe_mask
```

如果位数不足：

- 返回 `false` 或较小 mask。
- 不断连。
- 允许固件降级运行。

一旦返回 `version-rolling: true`：

- `mining.submit` 必须接受第 6 个 `version_bits` 参数。
- 按 job 当时的 mask 验证。
- 完整 version 计算必须是：

```text
(job_version & ~mask) | (version_bits & mask)
```

如果池声称支持但忽略第 6 个参数，ASIC 会连续 `badpow`，很快切备用池。

`mining.set_version_mask` 按 BIP 310 是立即生效的例外；必须保存 mask epoch，处理刚好在变更边界飞行中的 share。

### AsicBoost

现代 overt AsicBoost 主要依赖 version rolling。需要：

- 给足够的安全 version bits。
- 不允许覆盖共识或软分叉 signaling bits。
- signer/job builder 和 share validator 使用相同 mask。
- block candidate 重建时应用矿工提交的 version bits。

旧式 covert AsicBoost、特定厂商私有实现兼容性差，不建议为了兼容做隐式猜测。具体固件行为存在版本差异，**不确定**。

## 3.2 extranonce2 size

subscribe response 中：

- extranonce1 必须为偶数长度 hex。
- `extranonce2_size` 是字节数，不是 hex 字符数。
- miner 提交的 extranonce2 必须按规定长度组合 coinbase。

建议分端口：

| 端口类型 | 建议 |
|---|---|
| 普通直连 ASIC | 优先兼容常见 4-byte extranonce2 固件 |
| 大矿场代理 | 提供 6–8 字节，具体按代理验证 |
| NiceHash/MRR/Braiins Hashpower | 独立 endpoint，按平台要求配置 |

部分老 ASIC 固件把 extranonce2 写死为 4 字节；另一些 marketplace/proxy 又要求至少 6、7 或 8 字节。这两个目标不适合强行共用一个端口。

例如 Braiins Hashpower 当前明确要求目标 pool 的 `extranonce2_size >= 7`；这不代表 NiceHash 和 MRR 也统一要求 7。[Braiins Hashpower 兼容要求](https://academy.braiins.com/braiins-hashpower/faqs/basics)

## 3.3 会让 ASIC 直接断连的细节

高风险项：

- subscribe response 层级或数组长度错误。
- `extranonce2_size` 单位弄成 hex 字符数。
- difficulty 用字符串而不是 number。
- difficulty 使用科学计数法，某些简陋 JSON parser 不接受。
- job id 使用超长 UUID；一些固件假定短十六进制。
- version、ntime、nbits 长度或大小端错误。
- merkle branch 顺序错误。
- `clean_jobs` 类型不是真正 boolean。
- authorize 返回 `"true"` 字符串而不是 boolean。
- submit response id 不匹配。
- 不响应 submit，导致固件 pending queue 塞满。
- 收到 `mining.configure` 便关闭连接。
- 声称支持 version rolling，却拒绝第 6 参数。
- 难度太低导致 ASIC 瞬间提交成千上万 share。
- 难度太高，长时间一个 share 都没有，watchdog 判 pool 死。
- 改难度后不发新 job。
- 改 extranonce 后不发新 job。
- 重复使用相同 extranonce1 给并发 session。
- 服务器主动发送 banner、HTTP 文本或非 JSON 日志。

兼容层还要容忍：

- `\n` 和 `\r\n`。
- 空 user agent、null 参数、缺省参数。
- request id 重复、数字或字符串 id。
- authorize 与 subscribe 被 pipeline 到同一个 packet。
- hex 大小写。
- 少量固件 extranonce2 前导零异常。

最后一项不要全局宽松处理。应建立按 `agent/fingerprint` 启用的 quirk profile，避免错误补零造成 duplicate work。

---

# 4. NiceHash/MRR 租赁算力兼容清单

## 4.1 通用清单

- 提供独立 rental endpoint。
- 支持极高初始 hashrate，不从 CPU/GPU 低难度起步。
- 支持 `mining.extranonce.subscribe`。
- 支持 `mining.set_extranonce`，随后立即发新 job。
- 为代理保留足够 nonce/extranonce 空间。
- 支持一个 IP、一个连接代表大量下游设备。
- 不按 IP share 数量粗暴限流。
- submit response p99 尽量控制在 500 ms 内。
- 允许短时间大量连接、断开、重新 authorize。
- 固定或缓慢 vardiff，避免 marketplace 统计被频繁难度变化扰动。
- pool verifier 使用的 authorize 不应触发封禁。
- 识别 block candidate 时仍必须本地完整验证，不能因为来源是 NiceHash 而简化。
- PPLNS 按 difficulty-weighted work，不按 share 条数。

## 4.2 `mining.extranonce.subscribe`

矿工成功 subscribe 后可能发送：

```text
mining.extranonce.subscribe
```

池应返回 success。需要切换时发送：

```text
mining.set_extranonce(extranonce1, extranonce2_size)
→ 新 mining.notify
```

新 extranonce 应从下一 job 开始使用；即使模板没变，也必须让矿工切换 work。最好生成新 job id，并明确处理旧 job 的失效范围。[NiceHash extranonce extension](https://github.com/nicehash/Specifications/blob/master/NiceHash_extranonce_subscribe_extension.txt)

不要把 URL 中的 `#xnsub` 当成池会收到的参数；它通常由矿工或 marketplace 自己解析，池实际看到的是 protocol method。

## 4.3 算法专用要求

### Bitcoin/SHA256

- 足够大的 extranonce2。
- BIP 310/version rolling。
- overt AsicBoost。
- 高初始 difficulty。
- 短 job id。
- xnonce 动态切换。

### CryptoNote/RandomX

- 登录识别 `nicehash` 能力。
- 在 blob 的正确 reserved/nonce offset 中分配 proxy 字节。
- 支持 `algo` negotiation。
- 支持 4-byte/8-byte target profile。
- 每个 job 的 blob 必须包含唯一 nonce 空间。

NiceHash 历史 CryptoNight 扩展会固定 nonce 的一部分供其下游分配。[NiceHash CryptoNight modification](https://github.com/nicehash/Specifications/blob/master/NiceHash_CryptoNight_modification_v1.0.txt)

### KawPow

NiceHash 型 nonceprefix 端口应限制 pool prefix 长度，给 marketplace 留出继续切分空间。不要拿 direct RVN 端口未经测试直接填进 NiceHash。

### Equihash

- nonce1 不能长到让下游 nonce2 空间不足。
- solution 长度和 personalization 严格按 `(N,K)`/coin。
- `mining.set_target` 必须先于对应 job。
- NiceHash 端口使用平台支持的具体 Zhash/Equihash profile。

## 4.4 最低难度

NiceHash 的最低难度按算法、订单规模和平台策略变化，没有一个长期通用数字。其 verifier 可能直接提示：

```text
difficulty too low: provided=X, minimum=Y
```

池应提供：

- dedicated fixed-diff 端口。
- password `d=Y`。
- BIP 310 `minimum-difficulty`。
- API/控制台设置 rental worker floor。

NiceHash 的公开购买指南也使用 `d=...` 作为兼容办法。[NiceHash Buying Guide](https://static.nicehash.com/marketing%2FBuying%20Guide.pdf)

MRR 也支持 `#xnsub`，并依赖 miner 正确处理 difficulty change 和 `client.reconnect`；但其不同算法代理行为并不完全等同 NiceHash。[MRR xnonce 说明](https://www.miningrigrentals.com/helpcenter/Rigs/57)

## 4.5 用租赁算力冷启动值不值

技术上可行，经济上通常不是“挖矿套利”，而是市场推广或网络安全预算。

主要坑：

- 租赁价格通常包含市场溢价，期望挖出价值可能低于租金。
- 算力可瞬间进入、瞬间离开。
- 小链 difficulty 被推高后，租赁结束可能长时间不出块。
- 租赁算力可能占全网大多数，形成中心化或 51% 攻击观感。
- 大量算力同时切 job，瞬间放大 share flood。
- 链发生 fork/reorg 时，早付风险显著增大。
- PPLNS 窗口太短会被短时租赁算力支配。
- 交易所可能对异常算力、深度重组提高确认数或暂停充值。
- NiceHash/MRR 中间层隐藏真实矿机数量，问题定位困难。

我的建议：

- 如果目的只是“页面上看起来有算力”，不值。
- 如果是正式新链启动，可把租金明确列为安全/营销预算。
- 使用算力占比、持续时间、最高成本、链上 reorg 的硬限制。
- 不要同时上线低确认快速付款。
- PPLNS 以累计 work 定义窗口。
- 对可租算力占比很高的小链，准备自动提高确认数和关闭付款。

“租赁算力长期不超过全网 20%–30%”可以作为保守运营参考，但对新链未必现实，且不是安全定理，**不确定**。

---

# 5. vardiff 怎么设计

## 5.1 推荐默认值

可以从以下范围起步，之后按算法压测：

| 场景 | 目标 share 间隔 |
|---|---:|
| CPU/普通 GPU | 10–20 秒 |
| 高性能 GPU/ASIC | 5–15 秒 |
| 普通 proxy 聚合连接 | 5–15 秒，按整个连接算 |
| NiceHash/rental | 使用较高固定 floor，或 10–20 秒的慢 vardiff |
| 高延迟地区 | 15–30 秒，降低协议压力 |

我会把普通默认设为 15 秒，retarget interval 设为 60–90 秒。

## 5.2 估算方法

不要只看最后两个 share 的时间差。使用窗口内累计 work：

```text
estimated_work_rate = Σ job_assigned_difficulty / observation_time
new_difficulty = estimated_work_rate × target_share_interval
```

如果所有 share difficulty 相同，相当于：

```text
new_diff = old_diff × target_interval / observed_average_interval
```

建议：

- 至少观察 60–90 秒。
- 或至少积累 6–10 个有效 share。
- 使用 EWMA 平滑。
- 触发区间设为目标的 ±30% 左右。
- 每次普通调整限制在 ×0.5 到 ×2。
- 极端 share flood 可以触发一次紧急 ×4 或更高，但仍要发新 job。
- 长时间没有 share，达到 3–4 个目标周期后再逐步降难度。
- 记录历史 worker/agent hashrate，为重连设置合理 initial diff。

用于 hashrate/vardiff 估算的 share，可以包括 PoW 有效但 stale 的 work；不能包括 duplicate、badpow 和 malformed。

## 5.3 大批量涌入几百个 share

常见原因：

- GPU miner 本地缓冲后集中发送。
- 网络短暂停顿恢复。
- 一个 proxy 背后很多矿机。
- 初始 difficulty 严重过低。
- block 更新前后的并发提交。

正确处理：

1. 以消息被完整解析的时间记录 arrival time。
2. 每个 submit 立即关联 job 和该 job 的 assigned target。
3. 先做便宜校验：

   - session/worker。
   - job 是否存在。
   - 字段长度。
   - duplicate key。
   - nonce/extranonce 范围。

4. 再进入 PoW 验证队列。
5. 不因“同一毫秒到达几百条”直接判 duplicate 或 low diff。
6. response 保持原 request id，允许异步乱序返回。
7. 设置每 session 的 in-flight 上限，但采用 TCP backpressure，而不是把有效 share 标成 invalid。
8. PoW 队列饱和时触发运维保护、提高下一 job 难度。
9. stale 以 job 是否仍有效判断，不以“验证队列排队太久”判断。
10. block candidate 使用高优先级验证和提交通道。

## 5.4 难度何时生效

原则：**难度变化对下一 job 生效，不对现有 job 立即生效。**

流程：

```text
计算 next_diff
→ 发送 set_difficulty / set_target
→ 发送新 job
→ 新 job 保存新 target
→ 旧 job 在 grace window 内仍按旧 target 验证
```

原因：

- 矿工可能已经在旧 job 上工作。
- `set_difficulty` 与 `notify` 在网络中存在到达边界。
- GPU/ASIC 可能缓存多个结果。
- proxy 可能把旧 job 分发给大量下游矿机。
- EthereumStratum/1.0.0 和 ZIP 301 都明确体现了“对之后 job 生效”的语义。

CryptoNote target 直接在 job 内，因此只能通过发新 job 改难度。

如果只更新 session 的 current difficulty，旧 share 会被按新难度误拒，这是 vardiff 最经典的生产事故之一。

---

# 6. 拒单语义和矿工容忍度

## 6.1 Bitcoin/Equihash 常用错误码

| 情况 | code | 推荐 message |
|---|---:|---|
| stale/job 不存在 | 21 | `Job not found` 或 `Stale share` |
| duplicate | 22 | `Duplicate share` |
| 未达到该 job target | 23 | `Low difficulty share` |
| 未授权 worker | 24 | `Unauthorized worker` |
| 未 subscribe | 25 | `Not subscribed` |
| malformed、bad solution、其他 invalid | 20 | `Invalid share`、`Invalid solution` |
| 未知 method/extension | 20 或 JSON-RPC `-32601` | `Method not found` / `Not supported` |

ZIP 301 也正式保留了 20–25 这套错误码。[ZIP 301 error codes](https://zips.z.cash/zip-0301)

Bitcoin SV1 share 推荐返回：

```text
成功：result=true, error=null
失败：result=false 或 null，error=[code, message, null]
```

对 Bitcoin/cgminer/ASIC 兼容端口，我倾向 share reject 使用 `result=false`；对严格 ZIP 301 端口使用规范要求的 `result=null`。这就是为什么 response serializer 要按 dialect 分开。

### stale

只在以下情况使用：

- job id 不存在或已过期。
- prevhash 已变，旧 job 不再接受。
- clean epoch 已使旧 job 作废。

不能因为服务器验证慢、vardiff 已变或 miner 延迟高就把 share 标 stale。

### duplicate

duplicate key 应包括完整 work identity：

```text
session/extranonce epoch
+ job id
+ extranonce2/nonceprefix
+ ntime
+ nonce
+ version bits
+ solution/mix hash（视算法）
```

不能只按 nonce 去重；同一个 nonce 在不同 extranonce、不同 job 上完全合法。

### low difficulty

PoW 本身可能有效，但没有达到该 job 的 share target。必须用 job 保存的 target，不是 session 当前 target。

### badpow/invalid

包括：

- Equihash solution 本身无效。
- CryptoNote 提交的 result hash 不匹配本地计算。
- KawPow mix/nonce/header 不一致。
- version bits 越界。
- coinbase/header 重建失败。

第一次 badpow 不应立刻断连或封禁。只有连续、高比例、确定性 badpow 才进入 ban policy。

## 6.2 各类矿工的容忍度

| 客户端类型 | 一般行为 |
|---|---|
| XMRig | 对正确格式的 share error 比较宽容；login 失败、长期无 job、keepalive 无响应会重连 |
| SRBMiner-Multi | 支持方言多，但不同算法走不同 parser；错误 schema 或 job 字段数错误容易直接重连 |
| T-Rex/lolMiner/Gminer/BzMiner | 普通 stale/duplicate 能容忍；连续 reject、submit timeout、job timeout 会切备用池 |
| cgminer/cpuminer | 多数能显示 20–25 错误；老版本对未知 method 和奇怪 JSON 类型较脆弱 |
| ASIC 固件 | 最不统一；有的忽略未知 notification，有的收到不认识的消息就重连 |
| NiceHash/MRR proxy | 对响应延迟、extranonce、difficulty 和 nonce 空间尤其敏感 |

闭源矿工和 ASIC 固件的准确 watchdog 阈值经常随版本改变，没有可靠统一数据，**不确定**。不要围绕“第几次 reject 会切池”设计，而要保证：

- 每个 submit 都响应。
- p99 足够低。
- reject 分类准确。
- 不发送 malformed JSON。
- authorize/subscribe 快速完成。
- 第一个 job 不延迟。
- unknown extension 不断连。

最容易让矿工判池死的不是一次 stale，而是：

- submit 一直没有 response。
- response id 错。
- authorize 成功后迟迟没有 job。
- 连续 `low difficulty`。
- 重复 `Job not found`。
- JSON schema 在连接中途变化。
- `set_difficulty` 后没有新 job。
- pool 每收到 unknown method 就 close。

## 6.3 未知 method 怎么回

分三类：

1. 有 request id 的未知 method：

   - 返回 method-not-found。
   - 保持连接。
   - 不计 invalid share。

2. 已知但不支持的 extension：

   - `mining.configure` 中返回对应 extension `false`。
   - `mining.extranonce.subscribe` 可返回 `false + Not supported`。
   - 不断连。

3. 常见无害 method：

   - `eth_submitHashrate`
   - `mining.suggest_difficulty`
   - `mining.suggest_target`
   - `keepalived`
   - 一些 capabilities/info 方法

   最好真正支持；若业务上不用，也可 no-op success，避免闭源矿工误判。

有 `id` 的请求不要静默忽略。notification 的 `id=null` 才不要求响应。

---

# 7. 心跳、保活和 clean_jobs

## 7.1 V1 没有统一 ping 怎么办

Bitcoin SV1 主要靠：

- 持续 TCP 连接。
- submit response。
- 新 block/job。
- TCP keepalive。
- miner 自己的 work timeout。

不要发送自创 `ping` 给未知 ASIC。

建议：

- TCP keepalive idle 设为约 30–60 秒，连续若干探针后判死。
- 长出块链每 30–60 秒刷新一次 job。
- 同一 prevhash 的交易模板更新使用新 job id、`clean_jobs=false`。
- 新 block 立即发 `clean_jobs=true`。
- 不要每 3–5 秒重发 job，会增加 ASIC 重启 work 和 stale。
- 极快出块链由真实 block/job 自然保活，不必额外刷。

具体 30 秒还是 60 秒按链和矿工矩阵调整，**不存在全行业唯一值**。

CryptoNote 可以支持 XMRig 的 `keepalived` 扩展，响应 `status: KEEPALIVED`；登录响应中也可声明 `keepalive` extension。[XMRig KeepAlive](https://xmrig.com/docs/extensions/keepalive)

## 7.2 clean_jobs 的正确语义

- `true`：以前所有 job 都应被抛弃。
- `false`：新 job 加入，但旧 job 仍可能提交。

使用建议：

| 事件 | clean_jobs |
|---|---|
| 新 prevhash / 新 block | `true` |
| reorg 后新主链 tip | `true` |
| extranonce1 改变 | `true` |
| 同 prevhash 更新交易集 | `false` |
| vardiff 改变并发新 job | 通常 `false`，旧 job 仍按旧 diff 接受 |
| 共识版本/模板规则变化 | 通常 `true` |

现实中 miner 理解并不完全一致：

- 有的每个 notify 都立刻切 work。
- 有的在 `false` 时保留多个 job。
- 有的虽然切新 job，但仍会提交少量旧结果。
- 有的忽略重复 job id。

所以池端必须保存最近若干 job，而不能依赖矿工严格清理。新 block 后可保留一个很短的 network-latency grace 用于分类 stale，但是否计奖由结算规则决定。

---

# 8. worker 名和 password 参数

## 8.1 必须兼容的 worker 形式

优先支持：

- `地址`
- `地址.worker`
- `账户.worker`
- `地址/worker`
- 无 worker 时自动使用 `default`
- CryptoNote 原生 `rigid`
- ethproxy 顶层 `worker`
- Bitcoin 同一 session 多次 `mining.authorize` 不同 worker

`.` 是最常见分隔符，但不要无条件按第一个点切。应先尝试完整地址验证，再根据 coin/account 规则从右侧解析 worker。

内部应分开保存：

```text
account_identity
payout_destination
worker_name
rig_id
raw_login
```

不要把完整 `地址.worker` 当作付款地址。

worker 应限制长度和控制字符，但不要只允许字母数字；现实中常出现：

- `-`
- `_`
- `.`
- `/`
- 邮箱式账户
- Unicode worker 名

建议内部 canonical worker key 使用受控 ASCII；Unicode 只作 display，或直接拒绝并返回清晰错误。

## 8.2 password 常见参数

| 参数 | 含义 | 建议 |
|---|---|---|
| `x` | 占位 password | 必须支持 |
| `d=123` | fixed/suggested difficulty | 必须支持 |
| `diff=123`、`sd=123` | difficulty 方言 | 建议兼容 |
| `md=123` | minimum difficulty | 可兼容，但需文档明确 |
| `c=BTC` | yiimp/zpool 类指定结算币 | 仅 multipool/autoexchange 端口支持 |
| `mc=COIN` | 指定实际 mined coin | 仅明确需要时支持 |
| `rig=...`、`id=...` | rig/worker id | 可映射到 worker metadata |
| `nicehash` | 某些 miner/池的 NiceHash 提示 | dedicated port 优先 |
| `solo`、`m=solo` | solo 模式 | 最好独立端口，不建议 password 动态改变结算模型 |
| `mp=...` | minimum payout | 不建议从 Stratum 持久修改 |
| email | 通知或旧式账户验证 | 不建议作为安全凭证 |

password 在明文 SV1 中不应被视为秘密。

`mp=起付额` 看似方便，但任何能使用矿工地址连接的人都可能改变账户设置。建议：

- 可以解析并提示。
- 不直接持久修改财务设置。
- 只有通过 dashboard/API 强认证后才允许改变起付额。
- 地址即账户的匿名池最多把它当非安全偏好，并设置严格上下限。

解析器建议兼容逗号和分号：

```text
x,d=1000,rig=farm1
d=1000;rig=farm1
```

未知参数忽略并低频记录，不要拒绝登录。重复参数应采用确定规则，例如最后一个生效，同时记录审计告警。

优先级建议：

```text
端口安全下限
> 认证账户策略
> marketplace/ASIC minimum-difficulty
> password d=
> vardiff 默认
```

任何用户请求都不能突破池的最低安全难度。

---

# 9. 代理和一条连接几百台矿机

## 9.1 nonce 空间

### Bitcoin SV1

层级通常是：

```text
pool extranonce1
+ proxy 从 extranonce2 中划出的 child prefix
+ 下游矿机自己的 extranonce counter
```

如果代理后面有数百台设备，至少需要：

- 约 2 字节用于 child id。
- 额外 4 字节左右供下游滚动。
- 再考虑嵌套代理和租赁平台。

因此 proxy/rental 端口常需要 6–8 字节 extranonce2。

### Equihash

在 32-byte nonce 中：

```text
pool nonce1
+ proxy child prefix
+ miner nonce2
```

必须明确每层占多少字节，不能让两层 proxy 自行猜。

### CryptoNote

传统 nonce 只有 4 字节，空间最紧。xmrig-proxy 的 nicehash mode 会用 blob 中的预留字节区分下游矿工。池必须：

- 正确设置 reserved nonce byte。
- 登录响应声明 nicehash 支持。
- 每个上游 proxy session 使用唯一 blob。
- 不修改矿工应滚动的字节。

XMRig Proxy 官方说明其会把大量下游连接压缩成较少的上游连接，并要求 pool/miner 支持 NiceHash 风格 nonce 分配。[XMRig Proxy](https://github.com/xmrig/xmrig-proxy)

### KawPow

64-bit nonce 通过 nonceprefix 分层。NiceHash KawPow 环境中 pool prefix 不宜过长，否则代理继续分配后，下游 miner nonce 空间不足。

## 9.2 session 和 worker

Bitcoin submit 自带 worker name，因此一条连接可以 authorize 多个 worker。池应维护：

```text
session.authorized_workers = set
```

每个 submit 检查 worker 是否在该集合中，并按 worker 统计。

但 difficulty、extranonce 和 job 是连接级的，不能在同一 Bitcoin SV1 连接里给不同 worker 不同 vardiff。

CryptoNote submit 通常只有 session id，没有 worker name。一个 xmrig-proxy 上游连接在池看来就是一个 aggregate worker。不能凭空恢复下游每台矿机的统计。

## 9.3 vardiff 和统计

代理场景下：

- vardiff 按上游 connection/nonce space 计算。
- hashrate 按 accepted difficulty-weighted work 计算。
- 不按 IP 计算。
- 不把总 hashrate平均分给看不到的下游 worker。
- 记录 `agent`、`rig_id`、proxy mode、下游 worker 数量报告，但报告值不能作为结算依据。
- 初始 difficulty 使用历史 session/account hashrate或 proxy 专用端口。
- proxy reconnect 后保留短期 hashrate hint，但分配新的 session/extranonce。

## 9.4 限流

不能设置“一个 IP 每秒最多 10 个 share”这种规则。矿场 NAT、proxy、NiceHash 都会被误伤。

应分成：

- malformed JSON 限流：按 IP。
- 未认证连接限流：按 IP/网段。
- 认证后 submit 限流：按 session、difficulty 和预期工作量。
- PoW CPU 预算：按 account/session。
- 连接数：允许可信代理提高额度。
- block candidate：独立高优先级队列。

duplicate 判断必须包含 session/extranonce epoch，不能跨 proxy session 只按 nonce 去重。

---

# 10. 锄头兼容性回归测试矩阵

## 10.1 测试维度

### 客户端矩阵

至少覆盖：

- XMRig：当前版、前 1–2 个主版本。
- xmrig-proxy：nicehash/simple mode。
- SRBMiner-Multi。
- T-Rex。
- lolMiner。
- Gminer。
- BzMiner。
- cpuminer-opt、cpuminer-multi 主要 fork。
- cgminer、bfgminer。
- Antminer stock、Braiins OS、常见 VNish/LuxOS 等固件。
- NiceHash pool verifier/实际小订单。
- MRR 实际短租。
- Equihash ASIC 固件，例如不同代 Antminer Z 系。
- 你们实际用户提交过问题的旧版本。

闭源矿工版本行为差异很大，所以要按“miner + version + algorithm”记录，不能只写“支持 Gminer”。

### 协议矩阵

- TCP、TLS。
- IPv4、IPv6。
- LF、CRLF。
- JSON 一字节一字节分片。
- 多条 JSON 同一个 packet。
- configure/subscribe/authorize pipeline。
- id 为 number/string/null。
- params 缺失、null、额外可忽略字段。
- unknown method。
- xnonce。
- version rolling。
- fixed diff、vardiff。
- proxy。
- session reconnect。

### Job 状态矩阵

- 初始 job。
- 同 prevhash 更新 template。
- 新 block，clean true。
- reorg。
- vardiff 切换。
- extranonce 切换。
- version mask 切换。
- seed/DAG epoch 切换。
- 相同 job 重发。
- job id 回绕。
- server rolling restart。
- node 暂时无模板。

### Share 矩阵

- 正常有效 share。
- block candidate。
- stale。
- duplicate。
- 同 nonce、不同 extranonce。
- 同 nonce、不同 job。
- old-diff job 的有效 share。
- low difficulty。
- badpow。
- 错误长度。
- 错误大小端。
- 未授权 worker。
- authorize 多 worker。
- 几百条 burst。
- response 乱序。
- submit 到一半断线。
- block 到达与 submit 同时发生。

## 10.2 每个测试必须断言

- miner 不意外断连。
- response id 与 request 一致。
- error schema 与 dialect 一致。
- share 分类正确。
- assigned difficulty 正确。
- 统计 work 正确。
- duplicate 不跨合法 nonce 空间误判。
- clean job 后旧 share 分类正确。
- 没有把协议探测计入封禁。
- p50/p95/p99 response latency。
- block candidate 没被普通验证队列饿死。
- 重启后 session/job 行为符合预期。

## 10.3 没有真机如何模拟怪癖

建立两类测试器。

### Miner emulator

每个 quirk profile 模拟：

- 老 Antminer 只接受固定 subscribe shape。
- 不支持 `mining.configure`。
- configure 后立刻 subscribe。
- 固定 4-byte extranonce2。
- 不接受科学计数法 difficulty。
- 同一个 request id 重复使用。
- authorize/subscribe pipeline。
- 收到 unknown notification 就 close。
- GPU burst submit。
- proxy 多 worker authorize。
- XMRig keepalive。
- ethproxy 轮询 getWork。
- EthereumStratum 回落探测。

### Fake pool 驱动真实 miner

反向测试真实矿工：

- 先发 unknown method。
- 延迟 submit response。
- 连续发 clean false。
- set difficulty 后延迟新 job。
- set extranonce 后使用相同 job id。
- 发送 stale/reject。
- 断线、半开连接。
- TLS 证书轮换。
- 多 endpoint failover。

真实 PoW 可通过：

- 使用已知 header/share test vectors。
- 把测试网络 share target 调得极低。
- 使用 deterministic nonce/solution fixture。
- 对 GPU/ASIC 黑盒测试只验证握手、job parser 和 failover；PoW 正确性由独立向量验证。

### Transcript 回放

对每次生产兼容事故保存脱敏 transcript：

```text
client agent/fingerprint
→ 原始 request
→ pool response
→ job/difficulty/extranonce 状态
→ 最终原因
```

每修一个矿工兼容问题，就增加一个永久回归用例。

模拟器仍无法覆盖：

- ASIC 固件内部 watchdog。
- 芯片 nonce 分配。
- 高温降频后的提交节奏。
- 厂商私有 AsicBoost。
- GPU driver/DAG 边界行为。

因此正式运营后仍应维护少量真实 canary 设备，或租用远程真机实验室。NiceHash/MRR 每次大改前做低预算短时实测。

---

# 11. 2026 年是否值得投入 Stratum V2

## 结论

对你们这个“多币种、CPU/GPU 优先、未来才接 ASIC”的项目：

- **现在要在内部模型中预留 SV2 能力。**
- **不应把原生 SV2 作为第一个版本的核心交付。**
- **一旦准备正式运营 BTC/SHA256 ASIC pool，就值得建设独立 SV2 gateway。**
- **不要试图把 SV2 泛化到 Monero、Equihash、KawPow；它主要是 Bitcoin mining 生态协议。**

## 2026 年实际采用度

截至 2026 年：

- Braiins Pool、DMND 以及若干 solo pool 已有生产 SV2 endpoint。
- Braiins OS、Auradine/FluxOS、Bitaxe、NerdAxe 等已有原生或公开支持。
- SRI 有可运行的 pool、Translator Proxy、Job Declarator 等组件。
- 官方 adoption 页面已经列出多个生产部署。[Stratum V2 adoption](https://stratumprotocol.org/)
- 但 SRI 自己在 2025–2026 roadmap 中也承认，采用速度比预期慢。[SRI 2026 roadmap](https://stratumprotocol.org/blog/sri-roadmap-2026/)
- 2026 年 ANTPOOL、F2Pool、Foundry、MARA Foundation 等加入工作组，说明行业参与度在上升，但“加入工作组”不等于已经开放生产 SV2 endpoint。[SV2 Working Group 公告](https://stratumprotocol.org/blog/new-members/)

没有可靠、公开、可审计的全网 SV2 hashrate 占比，所以无法给出准确百分比。我的判断是：

> SV2 已经走出纯实验阶段，但仍是增长中的少数部署；SV1 仍然是不能放弃的主兼容面。具体份额「不确定」。

## 推荐投入路径

### 阶段 0：现在就做

内部 job/share 模型支持：

- 精确 target。
- channel/session 分离。
- 数字 job id。
- extranonce prefix。
- version/ntime rolling。
- submit sequence。
- worker identity 与 TCP connection 解耦。
- 协议无关的 job/template core。

### 阶段 1：BTC ASIC 上线前

部署独立 SV2 endpoint：

- 优先使用 SRI/reference libraries 或 translator，不从零自行发明。
- 先支持 pool-selected jobs。
- 与 SV1 payout/share ledger 共用后端。
- 单独监控和灰度。
- Braiins OS、FluxOS、SRI tProxy 做互操作测试。

### 阶段 2：有真实需求后

再投入：

- Extended Channels。
- Job Declaration。
- miner-selected template。
- worker-specific tracking extension。
- 大型 farm multiplexing。
- 原生 firmware 合作。

SV2 的实际价值包括：

- authenticated encryption，降低 hashrate hijacking。
- 二进制低开销。
- channel multiplexing。
- 更清晰的 target 和 share acknowledgment。
- 更好的 proxy 架构。
- Job Declaration 带来的模板自主权。

但它不会替你解决：

- altcoin 方言兼容。
- PoW validator。
- payout。
- share 结算。
- 节点模板错误。
- 旧 ASIC 固件。

因此优先级应是：

```text
正确的 SV1/CryptoNote/Equihash/KawPow
> 兼容性测试矩阵
> proxy/NiceHash
> BTC 原生 SV2
> Job Declaration 等高级 SV2 能力
```

最关键的一条总结是：**每个 job 都必须是一个不可变的“协议快照”——包含方言、target、extranonce、version mask、clean epoch 和 coin template。只要仍用 session 当前值验证历史 share，再多的矿工兼容补丁最终都会变成误拒。**
