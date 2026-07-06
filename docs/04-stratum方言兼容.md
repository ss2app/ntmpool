# Stratum 方言兼容矩阵（M1/M3 实现依据）

> 来源：2026-07-06 协议调研（规范原文 + 各锄头/池源码逐行核实）。02-架构设计 §3 的展开。

## 0. 方言分流：按连接后第一条消息判定

| 第一条消息 | 方言 | 典型锄头 |
|---|---|---|
| `mining.configure` | Stratum V1 + BIP310（ASIC） | Antminer/bmminer、新版 cgminer |
| `mining.subscribe` 且 params[1]=="EthereumStratum/1.0.0" | ES/1.0.0（NiceHash 系） | T-Rex(stratum2)、lolMiner(ETHV1)、TeamRedMiner |
| `mining.subscribe`（其他） | V1 或 kawpow（按端口/币区分） | cgminer、cpuminer-opt、SRBMiner、kawpowminer |
| `login`（params 为对象） | CryptoNote（XMRig 系） | XMRig 及全部衍生 |
| `eth_submitLogin` | ethproxy | Gminer 默认(ethash)、lolMiner ETHPROXY |
| `mining.hello` | ES/2.0.0 —— **EIP-1571 已 Stagnant，不做** | 仅 ethminer 系 |

实现优先级：**stratum1（M1）→ cryptonote（M3）→ ntm 自有（M3）→ kawpow / ES1.0.0（接 GPU 币时按需，M3+）**。

## 1. Stratum V1 必须实现的协议面

- `mining.subscribe`：params[0] = **user-agent**（原串存档）；响应 `[[订阅对], extranonce1, extranonce2_size]`。
- `mining.authorize`：params[0] = `钱包地址.矿工名`（按**最后一个** `.` 拆；纯地址无 `.` 要容错——T-Rex/lolMiner 都踩过截断 bug）；params[1] = 密码参数。
- `mining.notify` 9 参数；`clean_jobs=true` 仅新块时（同高度补发/定时重发用 false，避免无谓打断）。
- `mining.set_difficulty`：规范语义「从下一个 job 生效」→ 池侧做法 = **难度变更挂起（pendingDifficulty），随下一个 notify 一起发**（NOMP/miningcore 同款）。
- `mining.submit` 5 参数；**version-rolling 激活后有第 6 参数 `version_bits`**（BIP310 原文坐实），解析器必须容忍。
- `mining.configure`：**哪怕全部回 false 也必须回**（ASIC 第一条就发它，不理/断连=接不了 ASIC）。version-rolling 是 overt AsicBoost 基础，现代 SHA256 ASIC 几乎都发。
- `mining.extranonce.subscribe` → `true` + `mining.set_extranonce`（**NiceHash/代理刚需**）；语义：新 extranonce1 从下一个 notify 起用，「即使 job_id 相同也必须切换」。
- 响应三件套 `result`+`error`+`id` 必须齐全——NiceHash verificator 因 miningcore 少个 `"error":null` 直接判不合格。
- 未知 method 回 error 但**不断连**（ethminer 系靠逐级回落探测方言）。
- **job 定时重发 <60s**（NOMP 默认 55s）：V1 没有标准 ping，job 流就是事实心跳，锄头 ~1 分钟收不到 job 就判池死。

### 密码字段参数（分隔符同时容忍 `,` 和空格）

`d=`/`sd=` 起始/固定难度、`mp=`（我们自有：最低起付额）、`ID=`/`n=` worker 兜底；兼容 `c=`/`mc=`（zergpool 系付币/挖币，我们忽略不报错）。LuckPool 式 `d=16384S`（S=纯静态）可选。

### 锄头 user-agent 格式实录（解析必须容错）

`cgminer/4.10.0`、`cpuminer/2.5.1`、`cpuminer-opt-3.x.x-<arch><os>`（连字符！）、`SRBMiner-MULTI/1.0.8`、`teamredminer/0.7.6`、`bmminer/2.0.0`、`BzMiner-v11.1.0`。
建议正则 `^([A-Za-z0-9._+-]+?)[/ -]v?(\d[\w.]*)` 提取 名字+版本，失败就存原串。T-Rex/Gminer/lolMiner 确切串上自己池抓一次 params[0] 锁定。

## 2. CryptoNote 方言（XMRig 系）

- `login` params：`login`（地址[.难度][+worker]）、`pass`、`agent`（**锄头+版本+OS**，XMRig 必发）、`rigid`（矿工名一等公民，仅 `--rig-id` 非空才有）、`algo` 数组（能力协商）。
- 响应 `result={id:<会话id>, job:{...}, status:"OK", extensions:[...]}`；extensions 全集 `algo/nicehash/connect/tls/keepalive`。
- **job 必须带 `algo` 字段**（如 `rx/0`），矿工不支持会断线换池；RandomX 家族 `seed_hash` 必须恰好 64 hex，缺失即拒。
- target 编码：8 hex = 32-bit compact(LE)，16 hex = 完整 64-bit target(LE)；难度 ≈ 2^64/target64。
- `keepalived` → `{"status":"KEEPALIVED"}`（XMRig 60s 一发）。
- **worker 识别顺序：`rigid` > `pass`（剔除 "x"/空；`worker:email` 取 `:` 前）> login 的 `+worker` 后缀**；固定难度解析 login 后缀 `.N` 或 `+N`。
- 错误 message 事实标准集合：`Unauthenticated / Invalid job id / Duplicate share / Block expired / Low difficulty share / IP Address currently banned`；成功统一 `{"status":"OK"}`。
- **NiceHash CN 修订**：nonce 最高字节保留给上游，矿工只滚低 3 字节；**池下发 blob 的 nonce 区必须置零**（非零会误触发 XMRig 的 nicehash 模式）。

## 3. kawpow / ES1.0.0（GPU 币按需，M3+）

- kawpow：V1 骨架 + `mining.set_target`（256-bit hex，非 set_difficulty）+ 7 参数 notify（jobId, headerHash, seedHash, shareTarget, clean, height, bits）+ 5 参数 submit（user.worker, jobId, 完整 8 字节 nonce, headerHash, **mixHash**——池可免跑完整 ProgPoW 验证）。
- ES/1.0.0：subscribe params[1] 协议串；难度用 **Bitcoin pdiff 定义**；notify `[job_id, seedhash, headerhash, clean]`（**seed 在 header 前**，与 ethproxy 相反，经典坑）；extranonce ≤3 字节做高位前缀，submit 只交 minernonce。两条池侧义务：**首个 job 前必须先 set_difficulty**；clean=true 矿工清队列。
- ethproxy：协议不上报 agent（唯一缺口）；worker 取请求**顶层** `"worker"` 字段或 login params[0] 的 `wallet.worker`；push 用 id:0。

## 4. vardiff 业界口径（与 internal/vardiff 对照）

- NOMP/miningcore 同构：环形缓冲平均间隔、方差带 ±30% 内不动、距上次 retarget ≥ retargetTime 才调、maxDelta/maxJump 限单次调幅——与我们的 EMA+死区+限频同构。
- **previousDifficulty grace 容差系数业界一致为 0.99**（`shareDiff/difficulty < 0.99` 才走 prev 判定）——与 midstate「一步 grace」修法完全同构，M1 实现时把 0.99 容差加进 Judge。
- 池侧不立发难度：pendingDifficulty 随下一个 job 下发（§1）。
- 静态难度三通道：端口固定 diff / 密码 `d=` / CN 系 login 后缀。

## 5. NiceHash / 租赁算力兼容清单（新币冷启动利器）

1. extranonce.subscribe + set_extranonce（§1）。
2. 难度下限：SHA256ASICBOOST/SCRYPT 要求池难度 ≥8 且 **extranonce2_size ≥4**；接入前跑 NiceHash pool verificator 实测当下值。
3. JSON 严格三件套（§1）。
4. 首 job 前必发 set_difficulty；clean_jobs 语义正确。
5. CN 系 nonce 高字节保留（§2）。

## 6. 多端口惯例 + 锄头重连行为（池侧配合）

- 端口模板（业界惯例）：每币 3 端口起步——低难度 CPU/ARM 档 + vardiff 主端口 + 高起始难度矿场/NiceHash 档；SOLO/SSL 按需加（2Miners：SSL=原端口+10000）。TLS 自签证书业界普遍接受（xmrig-proxy 内置自签+指纹钉扎）。
- 锄头 failover 行为：cgminer 默认 FAILOVER（主池恢复自动切回；`client.reconnect` 只允许同域名，裸 IP 拒绝）；T-Rex 600s 试回主池、10 连拒重连；**lolMiner 连续 3 个「本地验证通过却被池拒」的 share 就弃池**——池侧误拒直接把矿机赶跑，一步 grace 不只是体验问题，是留住矿机的问题。
- 池侧义务：job <60s 重发（心跳）、CN keepalived 响应、TCP keepalive + 空闲清理、误拒率盯紧。

## 7. 待真机补证

T-Rex/Gminer/lolMiner 逐字 agent 串（上自己池打印一次锁定）、zergpool `sd=` 语法（官网 403）、NiceHash 现行 per-algo 难度表（跑 verificator）、bmminer 实际固件版本串。
