# 08 · Velkar (VELK) 接入 NTMPool 设计

> 第 12 币 Velkar = **Kaspa(rusty-kaspa) fork + 魔改 VelkarHash**。矿池路线：**接进 pool-core 统一核心**（不用包内 Rust solo bridge——它无 PPLNS/费/打款）。第一期**先矿工端、打款延后**（用户 2026-07-14 拍板）。
> 姊妹文档：[06-dragonx接入设计] [07-midstate接入设计]。算法权威：`_knowledge/algorithms/VelkarHash-velkar-spec.md`。

---

## 0. 状态速览（2026-07-14）

| 部件 | 状态 | 位置 |
|---|---|---|
| VelkarHash 纯 Go hasher + KAT | ✅ **完成**（5850U go test ok，逐字节匹配 pow=000382fb…） | `internal/hasher/velkarhash/velkarhash.go` |
| Kaspa gRPC proto Go stub | ✅ **生成落地**（11391 行，package protowire） | `internal/adapter/velkarrpc/protowire/*.pb.go` |
| gRPC 双向流客户端 + NodeAdapter + Notifier | ✅ **完成 + 真链铁证**（见下） | `internal/adapter/velkarrpc/{client,header,velkarrpc}.go` |
| Kaspa stratum 方言 + ShareHandler | ✅ **完成**（EthereumStratum，见下） | `internal/stratum/dialect_kaspa.go` |
| velkar 作业管理器 | ✅ **完成 + 单测**（Refresh/CurrentJob/HandleSubmit + 难度数学） | `internal/velkarjob/velkarjob.go` |
| family 装配 + `feePercent:5` | ✅ **完成**（registry switch 加 `velkar-rpc`） | `internal/coininstance/family_velkar.go` |
| WalletAdapter 打款 | ⬜ **第一期延后**（先手动 wallet/walletd） | — |
| testnet 联调 | ✅✅ **端到端已通**（Go 矿工连池挖出份额被池接受，见下） | 连 5850U velkard testnet-10 `127.0.0.1:26210` |

**估工作量**（Explore agent，生产级含 live 冒烟）：hasher 1-2d✅ / stratum 2-3d✅ / gRPC 适配 4-7d✅ / 钱包打款 3-6d(延后) / 作业+胶水 2-4d✅ / 联调 3-5d✅ → **矿工端全部完成（打款延后），2 个会话打通**。

---

### 🚨 主网上线准备（2026-07-14，下个窗口 · 主网 15 分钟倒计时）

用户下个窗口两件事：**① 发 velkar 的 NTMminer 挖矿指令；② 编 velkar 矿池二进制，备上传 103.80**（打款等已测完 = 矿工端端到端过）。

**① NTMminer 挖矿命令**（发给矿工/自用）：
```
NTMminer -a velkarhash -o <矿池host>:<port> -u <velkar地址>[.<矿机名>] [-t <线程>]
```
算法别名 velkar/velk/vkh；随 NTMminer **v1.15.0** 上架 www.ntmminer.com/downloads（win 静态+linux）。主网地址前缀 `velkar:`。端口由矿池 config 定。

**② 编 velkar 矿池二进制**（velkar 全纯 Go 无 cgo）：
```bash
# 源树 = 5850U ~/build/pcv/pool-core（含 velkar 全套；连机 C:\ntmbuild\ops\di_ssh.py，DI_PW=123qweQ!）
cd ~/build/pcv/pool-core
go build ./... && go vet ./...                          # 先确认这棵副本能编
CGO_ENABLED=0 go build -o ntmpool-velkar ./cmd/ntmpool  # velkar-only 不需 -tags quark/randomx
```
→ di_ssh get 拉本机 → scp 103.80（照 noctari 款：`/data/velkar/pool/` + systemd + sha 校验 + 旧二进制备份）。

**⚠ 主网未知项（testnet 值不能直接用，查/等用户给）**：
- **主网 velkar 节点**：testnet 是 5850U 单机 gRPC `127.0.0.1:26210`；主网要真节点 + 主网 gRPC 端点（端口可能不同）。主网节点是否已 sync？
- **主网 poolAddress**：`velkar:` 主网地址（testnet 用 pool2 主账户 `velkartest:qzaaslzww48…`）。
- **主网端口**：testnet = P2P 26211 / gRPC 26210 / wRPC-Borsh 17210 / walletd 28110。

**矿池 config 要点**：`adapter=velkar-rpc`、`nodes[0].url=主网 gRPC`、`algo=velkarhash`、**`feePercent=5`**、**`payout.enabled=false`**（accrue-only；WalletAdapter/Kaspa UTXO 签名打款=零先例最难，主网先手动 wallet/walletd）、stratum 端口开 Kaspa EthereumStratum 小数难度 vardiff（`Diff1=2^224-1`，start 5e-4/min 1e-4）。0ms 触发 = gRPC `NotifyNewBlockTemplate`/`NotifyBlockAdded` 推送 + `GetBlockTemplate` 轮询兜底（velkar 节点无 Bitcoin ZMQ）。部署照 [[noctari-ncti-coin]] 款（systemd + 209 端口转发 + config.js 前端注册）。

### ✅✅ 矿工端全栈完成 + 端到端铁证（2026-07-14，5850U）

**新增三部件全部编入 `go build ./...` + `go vet` + 回归测试通过（stratum/coininstance/cnjob 无破坏）：**
- **`internal/velkarjob/velkarjob.go`** — 作业管理器。实现 coininstance 作业管线面（Refresh/Snapshot）+ `stratum.KaspaShareHandler`（Algo/CurrentJob/HandleSubmit）。HandleSubmit 池端权威重算 `velkarhash.CalculatePow(prePow, ts, nonce, **blockTargetLE**)`（⚠target 入 stage4）→ 份额难度判定 → 命中网络 target 爆块 `SubmitSolved` → 记账回调。**无 badpow**（Kaspa submit 只回 nonce，矿工不上报 hash，池自算权威 pow）。难度口径 `Diff1=2^224-1`（`shareDiff=Diff1/pow`），因 pow 256 位而 maxTarget 224 位 → 用**小数难度**（start 5e-4/min 1e-4）。单测 4 路径（Accepted/LowDiff/Stale/Block 暴力找 2^254 target）全过。
- **`internal/stratum/dialect_kaspa.go`** — Kaspa(EthereumStratum/1.0.0) 方言。subscribe→`[true,"EthereumStratum/1.0.0"]`；authorize→OK 后依次 set_extranonce/set_difficulty/notify；extranonce=connID 低 16 位（2 字节分片，矿工滚低 6 字节）；**submit nonce 拼接**（短则左补 extranonce 成 16hex u64，share_handler.rs 语义）。连接层/vardiff/autoban/auth-hook 全复用 CN 方言同款。
- **`internal/coininstance/family_velkar.go`** — 装配 velkarrpc×velkarjob×kaspa 方言；`buildFamily` switch 加 `velkar-rpc`。wallet=nil（accrue-only，`payout.enabled=false` → engine 跳过所有 wallet 调用，实测安全）；classifier=adapter；notifier=gRPC 推送。

**★★VelkarHash stratum 必要扩展（关键决策）**：VelkarHash 的 stage4 吃 block target（`PowState::new` 用 `from_compact_target_bits(bits)`），但标准 Kaspa stratum notify 只发 prePow+ts——**外部锄头拿不到 target → 算废块**（正是反通用锄头根源，bridge 只有 solo GBT cpuminer 能挖）。故本方言 **notify job 数据 = prePow(64hex)+timestampLE(16hex)+blockTargetLE(64hex)=144hex**，把 block target 一并下发。两端（池+未来 NTM 锄头）都控制，此为 VelkarHash 首个真正 stratum 的必要协议扩展。

**★端到端铁证**（`velkar_stratum_live_test.go`，`VELKAR_NODE=127.0.0.1:26210 go test -run TestLiveStratumMine`）：真 velkard 模板 → 起 Kaspa 方言 TCP 服务 → Go 测试矿工完整握手（subscribe/authorize→收 set_extranonce/set_difficulty/notify）→ 解 144hex job → 用**下发的 block target** 暴力算 VelkarHash 找低难度份额（1e-8≈44 次哈希，nonce=0x2c）→ submit → **池端用同一 block target 重算 → 接受份额**。证 wire+velkarjob+VelkarHash **两端逐字节自洽**。

**下一步**：① 生产配置 + 部署 103.80（stratum 端口/vardiff 小数难度调参/`feePercent:5`/`payout.enabled=false`）→ 真矿工引流冒烟；② WalletAdapter 打款（Kaspa UTXO 构造签名 / 驱动 velkarwalletd，最难零先例）；③ 之后做 NTM CPU 锄头（velkar 算法路径，实现同款 144hex job 解析 + block target 入 stage4）→ GPU 锄头。

### ✅ gRPC 适配层完成记录（2026-07-14，5850U）

三文件全部编入 `go build ./...` + `go vet` 通过（go.mod 首次加 `google.golang.org/grpc v1.82.0` + `google.golang.org/protobuf v1.36.11`）：
- **`client.go`** — gRPC 双向流客户端。`RPC.MessageStream` 单条双向流，`supervise` goroutine 独占流生命周期（断→退避重连+重订阅），`callMu` 串行化"请求→响应"靠顺序配对（Kaspa 无 request-id），读循环按 payload 类型分流：response→`respCh`（调用方取），notification→`notifyCh`（Notifier）。类型化封装 getInfo/getDagInfo/getTemplate/submitBlock/getBlock。
- **`header.go`** — 从 `RpcBlockHeader` 算两个共识量：① `prePowHash` 复刻节点 `hash_override_nonce_time(header,0,0)`（BlockHash=blake2b-256 keyed `"BlockHash"` 覆盖头序列化）；② `targetFromBits` 复刻 `Uint256::from_compact_target_bits`（32B LE 喂 `CalculatePow` 的 target 参数——stage4 吃它）。
- **`velkarrpc.go`** — `Adapter`(NodeAdapter) + `VelkarWork`(Raw，非通用 BlobWork：承载 BlockTargetLE) + `velkarNotifier`(0ms 推送) + `SubmitSolved`(深拷模板块回填 nonce/timestamp 整块提交)。

**真链铁证**（`velkarrpc_live_test.go`，`VELKAR_NODE=127.0.0.1:26210 go test -run TestLive`，不依赖任何外部 KAT）：取真链已挖 tip 块（daaScore=1149，id=95cae549…），用本包 Go 代码从其 header 重算——
1. ✅ `headerHash(header, 真nonce, 真ts)` **逐字节 == 链上块 id** → header 序列化字节正确
2. ✅ `prePow + bits→target + CalculatePow(真nonce/ts)` 出 pow=`0000006c…` **≤** target=`000000c6…` → prePow/target/VelkarHash 三者全字节正确
3. ✅ `GetTemplate` 全链路：拿模板→算 pre_pow_hash→coinbase 2.0 VELK→target 解出→jobKey 派生

即：**从节点拉模板、算 pre_pow_hash、算 target、重算 VelkarHash、提交整块**全部就绪且经真链验证，可安全接管挖矿。

---

## 1. 复用 vs 新写清单（Explore agent 报告蒸馏）

pool-core 是**四层正交解耦**（节点适配 × 算法 × 作业管线 × stratum 方言），加 velkar = "再加一个 family"，**核心零改动**。

**✅ 直接复用（算法无关）：**
| 部件 | 文件 |
|---|---|
| stratum 连接层（端口热管理/vardiff/ban/PROXY/限流/autoban） | `internal/stratum/server.go`, `proxyproto.go`, `autoban.go`, `internal/vardiff/` |
| 打款引擎（fee/soloFee/feeCollect/feeSweep/守恒对账/孤块 debts/状态机） | `internal/payout/engine.go`, `payout.go` |
| PPLNS 会计（内存/PG，孤块/恢复） | `internal/accounting/` |
| **5% 费率**（`feePercent:5`，热更新 admin `ApplyPayout`→`SetParams`） | `internal/config/config.go` |
| 区块生命周期回调 blockSink/shareSink | `internal/coininstance/family_bitcoin.go` |
| Notifier fan-in + 去重（Height,Hash）+ 2s 轮询兜底 + 失联检测 | `internal/coininstance/coininstance.go`（startLoops/refreshOnce） |
| core 类型（ShareOutcome/Share/FoundBlock/TipEvent） | `internal/core/types.go` |
| 热配置/管理 API/多实例/算力/metrics/notify/脱敏 API | `internal/config`,`admin`,`api`,`hashrate`,`metrics`,`notify` |

**🆕 新写：**
| 部件 | 难度 | 位置 |
|---|---|---|
| VelkarHash hasher | 易✅完成 | `internal/hasher/velkarhash/` |
| Kaspa gRPC 适配（双向流/template/submit/BlockAdded） | **难** | `internal/adapter/velkarrpc/`（+proto stub✅） |
| Kaspa 钱包打款（UTXO 构造签名 / walletd）—第一期延后 | 最难零先例 | `internal/adapter/velkarrpc/`（钱包侧） |
| Kaspa stratum 方言 + KaspaShareHandler | 中 | `internal/stratum/dialect_kaspa.go` |
| velkar 作业管理器（模板→job/extranonce/重算/装配/提交） | 中 | `internal/velkarjob/` |
| family 装配 + buildFamily 分支 + 算力 multiplier=1 | 少量胶水 | `internal/coininstance/family_velkar.go` + `family_bitcoin.go` switch |
| go.mod 加 grpc/protobuf 依赖（**破极简依赖惯例**，首次） | ⚠ | `go.mod`（现仅 pgx + x/crypto） |

---

## 2. gRPC 双向流客户端 + NodeAdapter（最大工程）✅ 已实现（下方为落地规格，与 `client.go`/`velkarrpc.go` 一致）

**velkar gRPC 形态**（`internal/adapter/velkarrpc/protowire/`）：
- package `protowire`，`VelkardMessage` = 巨型 oneof（承载所有请求/响应/通知）
- service `RPC.MessageStream(stream VelkardMessage) returns (stream VelkardMessage)` = **单条双向流**（不是普通 unary！）
- 矿池要的 payload 编号：`GetBlockTemplateRequest/Response`(1005/1006)、`SubmitBlockRequest/Response`(1003/1004)、`NotifyNewBlockTemplateRequest/Response`(1081/1082)+`NewBlockTemplateNotification`(1083)、`NotifyBlockAddedRequest/Response`(1007/1008)+`BlockAddedNotification`(1009)、`GetInfoRequest/Response`(1063/1064，含 is_synced)

**实现要点：**
1. 建**一条**双向流 `MessageStream`，起一个 goroutine 读 stream、按 payload 类型分流：response→按序配对回调（Kaspa 无 request id，靠**顺序**配对，或每类型一个 pending chan）；notification→喂 Notifier。发送侧串行化（一个 sendMu）。
2. 实现 `adapter.NodeAdapter`：
   - `Status()` → GetInfo（is_synced/version）
   - `GetTemplate(payAddress)` → GetBlockTemplate（**节点造好含 coinbase 的 RpcBlock，池不重建 coinbase/merkle**——取 header 算 pre_pow_hash、解 nonce/timestamp 回填整块 submit，比 Bitcoin GBT 简单）
   - `SubmitBlock(block)` → SubmitBlock
3. 实现 `adapter.Notifier`（BlockAdded 或 NewBlockTemplate 流）→ 插进 coininstance 现成 fan-in（**这是 velkar 的 0ms 主推送通道，velkar 无 Bitcoin ZMQ**）。启动时发 NotifyNewBlockTemplate/NotifyBlockAdded 订阅。
4. 重连/失联处理（流断重建）。

**★逐行参考（Rust bridge，`D:\cs\velkar-poolowners-testnet\source\bridge\src\`）：**
- `velkarapi.rs` — gRPC 调用序列（`get_block_template`/`submit_block`/双向流建立/通知）**逐行照抄语义**
- `mining_state.rs`, `hasher.rs` — prePowHash 提取 / 区块装配
- pool-core 样板：`internal/adapter/dragonxrpc/dragonxrpc.go`+`longpoll.go`（RPC×blob 杂交 + Notifier 先例）、`internal/adapter/bitcoinrpc/longpoll.go`（Notifier 样板）、`internal/adapter/adapter.go`（NodeAdapter/WalletAdapter/Notifier 接口）、`internal/adapter/blob.go`（BlobWork/BlobSubmitter，velkar 用 blob 归一化模型）

**go.mod 加依赖**（在 5850U `go mod tidy`）：`google.golang.org/grpc` + `google.golang.org/protobuf`（proto stub 已 import 它们）。

---

## 3. 后续：stratum 方言 + 作业管理器 + family 装配

**stratum 方言**（`internal/stratum/dialect_kaspa.go`，照 `dialect_cn.go`）：
- `mining.subscribe`/`authorize`/`set_extranonce`/`notify(jobId, prePowHash words, timestamp)`/`submit(nonce)`；处理 extranonce、大端 jobHash
- 定义 `KaspaShareHandler` 接口（照 `CNShareHandler`）解耦 wire 与作业/PoW
- 参考 bridge `stratum_server.rs`/`share_handler.rs`/`stratum_listener.rs`（wire + vardiff 逐行）
- 连接层（server.go）100% 复用；`coininstance.go` 按 `map[string]Dialect` 装配，零侵入核心

**作业管理器**（`internal/velkarjob/`，照 `internal/cnjob/cnjob.go`）：
- `HandleSubmit`：① 用池侧 tag 重建 candidate → ② **池端重算 VelkarHash**（`velkarhash.CalculatePow(prePow, ts, nonce, target)`，**⚠必须把 block target 传进去**——stage4 吃 target，这是与 blob"target 只比较不入哈希"的唯一差别）→ ③ 与矿工声称 result 逐字节比对（不符=`OutcomeBadPow` tripwire）→ ④ `sub.Judge` 判 share 难度 → ⑤ 命中网络 target 爆块 SubmitBlock → ⑥ 记账回调 onShare/onBlock
- `core.ShareOutcome` 分类、vardiff Judge、badpow tripwire、回调机制全复用

**family 装配**（`internal/coininstance/family_velkar.go`，照 `family_bitcoin.go`）：
- 串 velkarrpc adapter + velkarjob + dialect_kaspa + velkarhash；算力 multiplier=1
- `family_bitcoin.go` 顶部 buildFamily 的 `switch cfg.Adapter` 加 velkar 分支
- 币配置 `feePercent:5`；poolAddress≠feeAddress 启动强校验

---

## 4. 环境坐标（下次会话直接用）

- **5850U（工厂主编译机，192.168.100.143 / di / 123qweQ!）**：Go 1.26.5（`/usr/local/bin/go`），GOPROXY=goproxy.cn。连机 `C:\ntmbuild\ops\di_ssh.py`（run/put/get，`DI_PW` 环境变量）。
  - ⚠ **操作铁律**：`PYTHONUTF8=1 PYTHONIOENCODING=utf-8`（否则 GBK 控制台遇 unicode 崩）；**长命令（含 sleep）易 RC=-1（电信线路瞬断）→ 用 `setsid nohup bash script > /tmp/x.log 2>&1 & echo started` 服务器端跑 + 短命令 poll**；`MSYS_NO_PATHCONV=1`；不暴力重试 SSH。
  - pool-core 测试副本 `~/build/pool-core-velkar/pool-core/`（本机 tar 推）；velkar 源码 `~/build/velkar/source/`。
  - 跑 hasher 自检：`cd ~/build/pool-core-velkar/pool-core && PATH=$PATH:/usr/local/go/bin GOPROXY=https://goproxy.cn,direct go test ./internal/hasher/velkarhash/`
- **velkard 单机 testnet-10 节点**：5850U 上仍在跑（`--testnet --nodnsseed --enable-unsynced-mining`，gRPC 127.0.0.1:**26210** / P2P 26211 / wRPC 17210）。**联调时矿池连 26210 gRPC**。数据 `~/velkar-data/`。重启：`~/build/velkar/source/target/release/velkard --testnet --nodnsseed --enable-unsynced-mining --utxoindex --yes --appdir=/home/di/velkar-data --rpclisten=127.0.0.1:26210 --disable-upnp`
- **测试钱包 pool2**（密码 `Velkar#pool123`，testnet-only）：矿池地址可用主账户 `velkartest:qzaaslzww48jdzysktamxfphq8gu2lq2gslesq9x0eyngmrjsnr92t40ua0vr`；driver 见本会话 expect 脚本（select 无参进菜单选序号、send 前等余额同步）。
- **本机无 Go**；GPU 锄头开发在本机 3070Ti msys64。

---

## 5. 风险 / 待澄清

- ⚠ **浮点共识点**：hasher 矩阵 rank 判定用 f64（Go 默认不 FMA，与节点 -ffp-contract=off 一致）；KAT 已过，联调时用真链多块对拍加固。
- ⚠ **go.mod 破极简依赖**：加 grpc/protobuf 是首次（`internal/zmqsub/zmqsub.go` 注释解释了极简铁律）——确认后再加。
- 打款（第一期延后）：Kaspa UTXO 交易构造签名是零先例最难块；可选驱动 `velkarwalletd`（`D:\cs\velkar-poolowners-testnet\source\wallet\daemon\proto\velkarwalletd.proto`）而非手搓交易。
- 三重触发在 velkar = **gRPC NewBlockTemplate/BlockAdded 推送(0ms) + 2s 轮询兜底**（无 ZMQ，Kaspa 系不需要）。

---

## 6. ✅ 生产部署记录（2026-07-15，主网全栈 LIVE）

**已上线**（详见记忆 `velkar-velk-coin.md` 顶部「主网生产全栈 LIVE」段，坐标不重复）：
- 节点 `velkard-mainnet`（103.80 systemd，`--velkarnet`，gRPC `127.0.0.1:16110`，已同步）。
- 池 `ntmpool-velkar`（103.80 systemd，`/data/velkar/pool/`，PG `ntmpool_velkar`，accrue-only，feePercent 5，stratum 5511 pplns/5512 solo）。config 本地 `coins/velkar/pool/config.json`。
- 钱包 poolmain（poolAddress `velkar:qzc9afy2…` / feeAddress `velkar:qr3dazgg…`，助记词/密码/钱包文件 `coins/velkar/secrets/`）。
- 端口转发 209（`velkar-stratum-forward.sh`）+ 前端 www.ntmminer.com/velkar。
- 冒烟：本地 NTMminer v1.16.0 挖主网 velkarhash，池接受份额（209 H/s）。

---

## 7. ✅✅ 自动打款 —— testnet 端到端闭环全打通（2026-07-15 下午收官）

> **一句话（最新）**：两个 bug 都已定位修复 + 隔离 testnet 端到端铁证闭合。**打款真到矿工地址链上（55.1 TVELK 全非 coinbase）**。真因不是 §7.2 猜的「wrpc runtime」，而是 **walletd 漏了 `wallet.start()`**；打款不触发的第二个 bug 是 **DAG 链撞上 classify 的 `BlockHashAt` 高度比对**（velkar 无 height→hash）。详见下方 **§7.7 收官记录**。§7.2/§7.3 的诊断与「下一步」已被 §7.7 取代（保留作排查轨迹）。剩余：**Kaspa storage mass 小额输出偶发打款失败**（非致命，引擎容错正常，见 §7.7）。

> **⚠ 历史一句话（2026-07-15 上午，已被上面取代）**：打款代码已写完编译过、node+pool+miner 跑通爆块；当时以为「唯一卡点 = connect_call 挂死是 wrpc-client async runtime 问题」——**方向对（确实是 runtime 没驱动 connect），但根因判错**：真因是 walletd 从没调 `wallet.start()`（见 §7.7），`with_block_async_connect(false)` 是错的修法。

### 7.1 ✅ 已实现（全部编译通过 `go build ./...`+`go vet`；Rust `cargo build` 通过）

**A. walletd 加多输出 `SendMany` RPC（Rust patch，守打款铁律 C2 一笔多付）**
- 源树 = 主网 `velkar-core-main`（本地 `C:\ntmbuild\velkar-core-main`，5850U worktree `~/build/velkar-fastmature`）。walletd bin = **`velkar-walletd`**（crate `wallet/daemon`）。
- 改 `wallet/daemon/proto/velkarwalletd.proto`：加 `rpc SendMany(SendManyRequest) returns (SendManyResponse)` + `SendManyOutput{toAddress,amount}` / `SendManyRequest{outputs[],password,from[],feePolicy}` / `SendManyResponse{txIDs[]}`。
- 改 `wallet/daemon/src/main.rs`：加 `async fn send_many`（复用 send 的 create→sign→broadcast，destination 换成 `PaymentDestination::PaymentOutputs(PaymentOutputs{outputs})`）。build.rs 自动 regen tonic，无需手跑 protoc。
- ⚠ walletd 单输出 `Send` 无 SendMany，wallet-core 底层 `PaymentOutputs` 原生支持多输出，超 mass 自动拆多笔 → txIDs 返回全部。
- ⚠ **walletd `password` 是「校验」非「解锁」**：空 → 用启动密码 → **池侧传空 password，钱包密钥只在 walletd 启动参数（铁律④）**。

**B. velkar WalletAdapter（Go，gRPC 驱动 walletd）** = `internal/adapter/velkarwallet/velkarwallet.go`
- 实现 `adapter.WalletAdapter`：`SpendableBalance`→GetBalance.available；`SendMany`→walletd SendMany（多输出一笔，传空 password + from=[poolAddress]）；`TxConfirmations`→节点 mempool 信号。
- Go gRPC stub = `internal/adapter/velkarwallet/pb/*.pb.go`（protoc 从 velkarwalletd.proto 生成，package pb）。
- 确认追踪辅助 = `internal/adapter/velkarrpc/mempool.go`（新文件，`Adapter.TxInMempool` 用节点 `GetMempoolEntry`：在池=待确认/离池=已入块。Kaspa 无 txindex 的真链近似，不伪造数据）。

**C. family 接线** = `internal/coininstance/family_velkar.go`：`cfg.Wallet.URL != ""` 才建 `velkarwallet.New(...,node)`（node 作 mempool 探针），否则 wallet=nil accrue-only。

**D. 二进制** = 5850U `~/build/pcv/pool-core/ntmpool-velkar`（`CGO_ENABLED=0 go build -o ntmpool-velkar ./cmd/ntmpool`，27MB，编译过）。

### 7.2 🚨 卡点：velkar-walletd `connect_call` 挂死（精确诊断）

**症状**：walletd 启动 → 打开钱包 OK → **`connect_call`(连节点 wRPC ws://…:17410) 永不返回**，进程活着但 **strace 15s 零网络系统调用**（TCP 已建立但从不发 WebSocket upgrade）→ gRPC server 永不 `serve` → 28110 不监听 → 池打款调用全失败。

**已排除**（逐一验证）：
- ❌ network_id：`--network testnet`→`NetworkId::from_str("testnet")` 失败静默回退 Mainnet；**必须 `--network testnet-10`**（已确认修正，network_id 正确）。
- ❌ resolver：`Wallet::try_new(...,None,...)` 去掉 `Resolver::default()`——仍挂死。
- ❌ wRPC 编码：wallet-core `wallet/core/src/wallet/mod.rs:146` 用 `WrpcEncoding::Borsh`，节点 `--rpclisten-borsh=17410` 也是 Borsh，**匹配**。
- ❌ 节点 is_synced：单机 testnet 无 peer→`is_synced=false`（`protocol/mining/src/rule_engine.rs:139` testnet 要求 peer）。已 patch 该行去掉 Testnet（`!matches!(net_type, Mainnet)`）让隔离网报 synced——但卡点在 connect **之前** sync 那步，此 patch 不影响 connect 挂死（sync patch 是对的，接好 connect 后仍需要它，别删）。

**定位手段**：在 walletd `main.rs` 加了 `eprintln!("VKDBG: ...")` 标记（`init_logger(None,"info")` 此 fork 零产出，logger 失效，用 eprintln 绕过）。最后打印 `VKDBG: wallet opened, connecting wrpc=...` → 卡在 `connect_call`。

**结论**：walletd（此 pool-owners 包的**自写** gRPC daemon，非标准 kaspa-cli）用裸 `tokio::runtime::new_multi_thread()`，wRPC 客户端（workflow-rpc/workflow-websocket 用 workflow-core executor）的 connect 任务未被 poll → 挂死。**是 walletd 运行时问题，不是打款代码。⚠ 同一 walletd 二进制在主网大概率也挂 → 必须先解决才能上自动打款。**

### 7.3 🎯 下一步（下个窗口按序试）

1. **【最可能/最先试】非阻塞 connect**：`ConnectRequest` 有 `with_block_async_connect(bool)`（`wallet/core/src/api/message.rs:83`，默认 true）。walletd `main.rs` 的 `connect_call(ConnectRequest::default()....)` 加 `.with_block_async_connect(false)`。若 WS 其实已连、只是「已连接」阻塞信号没投递，改非阻塞 → server 起来 + wRPC 后台连 → 打款操作能用。重编 walletd 试。
2. 若①不行：对比 **velkar-cli**（`cli/src/main.rs` 用 workflow-terminal 起 runtime，2026-07-14 testnet 打款就是用它成功的）怎么建 runtime/executor，把它的方式搬进 walletd main。或 `cargo build -p velkar-cli` 起来验证 velkar-cli 能连（隔离它是 walletd 特有还是 wallet-core 通病）。
3. 若 walletd 路线走不通：备选 = pool-core 直接构造 PSKT（`wallet/pskt` crate = `velkar-wallet-pskt`）或 shell out velkar-cli 的 `pskb` 命令签名（docs 评估：更复杂，路线优先级低）。

### 7.4 联调环境（5850U，随时可复活，见记忆 [[velkar-velk-coin]]「自动打款」段）
- **fast-maturity worktree** `~/build/velkar-fastmature`（git branch `vk-fastmature`，主网源码零污染）。patch 清单：coinbase_maturity 30→2（`consensus/core/src/config/params.rs`）、钱包成熟 30→2（`wallet/core/src/utxo/settings.rs`）、**testnet genesis bits `0x1e7fffff`→`0x1f5fffff`**（`genesis.rs`，argon2 慢挖必须降难度，否则 131k 哈希/块太慢；devnet 同款；节点运行时不校验 genesis hash 故改 bits 不用重算 hash）、rule_engine testnet 免 peer synced、walletd main.rs 加 VKDBG 标记 + 去 resolver（调试用，上主网前回滚，只留 SendMany patch）。
- **节点仍在跑**：5850U `velkard`（fast-maturity）gRPC `127.0.0.1:26410` / wRPC-Borsh `127.0.0.1:17410` / P2P 26411，appdir `/home/di/velkar-fm-data`（链已挖到 height 20+，持久化，walletd 调试可直接连）。旧 testnet 节点 26210 未动。
- **测试 config** `/home/di/velkar-fmtest/config.json`（内存会计无需 PG、payout.enabled=true、confirmations 4、interval 20s、minPayout 0.1、feeCollect off、poolAddress=pool2 主账户 `velkartest:qzaaslzww48…`、feeAddress+miner=pool2 acct2 `velkartest:qpdc2s0pj8…`）。
- **钱包** `~/.velkar/pool2.wallet`（密码 `Velkar#pool123`，testnet-only）；⚠`~/.velkar/poolmain.wallet`=主网生产钱包**测试绝不碰**。
- **复活命令**（3 条短命令，避免长 sleep RC=-1）：
  ```bash
  # 1) 节点(若停了): setsid nohup ~/build/velkar-fastmature/target/release/velkard --testnet --nodnsseed --enable-unsynced-mining --utxoindex --yes --appdir=/home/di/velkar-fm-data --listen=0.0.0.0:26411 --rpclisten=127.0.0.1:26410 --rpclisten-borsh=127.0.0.1:17410 --disable-upnp
  # 2) walletd(改 block_async_connect 后重编): setsid nohup ~/build/velkar-fastmature/target/release/velkar-walletd --listen 127.0.0.1:28110 --wrpc ws://127.0.0.1:17410 --network testnet-10 --wallet pool2 --password 'Velkar#pool123' --allow-unsynced
  # 3) 池+矿工: cd /home/di/velkar-fmtest; setsid nohup ~/build/pcv/pool-core/ntmpool-velkar -config config.json -payouts=true ; setsid nohup ~/build/ntmminer/NTMminer -a velkarhash -o 127.0.0.1:5511 -u velkartest:qpdc2s0pj8kts4j0ukh27k4wc43v084854e8s0us0kmhn77eke5654us67f0c.fmrig -t 4
  ```
  验证：walletd `ss -tlnp|grep 28110` 监听 = connect 修好；池 `curl 127.0.0.1:4412/api/pools` 看 totalBlocks 涨 = 出块；打款看 `grep 打款 /tmp/vk-fm-pool.log` + walletd SendMany + 矿工 acct2 链上到账（walletd GetExternalSpendableUTXOs）+ 对账 delta=0。

### 7.5 ⚠ 重要订正：coinbase 成熟 = **30 块（≈5min）**，不是 1000 DAA
- 主网源码双证：共识 `consensus/core/src/config/params.rs:193` `coinbase_maturity: 30`（注释「mature after 30 blocks ≈ 5min」）；钱包 `wallet/core/src/utxo/settings.rs:47` mainnet coinbase 成熟 30 DAA / stasis 15 / user tx 10。记忆里「1000 DAA」来自 Discord 传闻，与 shipped 源码不符。
- 影响：现网池 `config.json` `confirmations: 1000` **过保守**（等 1000 块才打款，实际 30 就能花，不是 bug 但慢）。上自动打款时可下调到 ~40（>30 成熟即可，留余量）。

### 7.6 主网上线清单（connect 修好、testnet 铁证过后）
- velkar-core-main walletd 源**回滚 VKDBG 标记 + 恢复 resolver**，但**必须保留两个 patch：① SendMany RPC ② `wallet.start()`（§7.7 的真正修复，漏了它 connect 必挂死，主网同样）** → 5850U 编主网 `velkar-walletd`。
- `velkard-mainnet.service` ExecStart 加 `--rpclisten-borsh=127.0.0.1:17110`（当前只开 gRPC 16110）→ `systemctl restart`。
- 103.80 跑 walletd 载 poolmain（`--network mainnet --wallet poolmain --password <coins/velkar/secrets>`，⚠密钥在生产机=安全权衡，可只打款时起或独立机）。
- 池 config 加 `"wallet":{"url":"127.0.0.1:28110"}` + `payout.enabled=true` + `confirmations` ~40 + 重编 ntmpool-velkar 部署 → 真矿工到起付额验证闭环 + 对账 delta=0。

### 7.7 ✅✅ 收官记录（2026-07-15 下午，testnet 端到端打款闭环全打通）

**两个独立 bug，逐一定位修复，隔离 testnet（5850U fast-maturity）端到端铁证闭合。**

**Bug ① walletd `connect_call` 挂死 = 漏了 `wallet.start()`（不是 wrpc runtime）**
- 真因：`velkar-walletd/src/main.rs` 在 `wallet_open_call` 后直接 `connect_call`，**从没调 `wallet.start()`**。kaspa wallet-core 的 `connect_call`（`wallet/core/src/wallet/api.rs:82`）需要两个后台服务在跑：(a) `rpc_client.start()` 才会发 WebSocket upgrade（= strace「TCP 建立但零 I/O」的真相），(b) `utxo_processor.start()` spawn 的 task 监听 `RpcState::Connected` 才会触发 connect_call 里 `receiver.recv()` 等的 connection signaler（`utxo/processor.rs:708/717`）。`wallet.start()`（`mod.rs:597`）恰好启动这三样。对照组 velkar-cli 明确调了 `self.wallet.start()`（`cli/src/cli.rs:248`，注释「wallet starts rpc and notifier」）。
- 修复：main.rs 在 open 之后、connect 之前加 `wallet.start().await?;`。日志立即从卡在「connecting wrpc=…」→ 走通「wallet.start() done → connected → gRPC server on 28110」，28110 开始监听。
- ⚠ `with_block_async_connect(false)`（§7.3 原计划第一步）是**错的方向**，救不了 utxo_processor 那半边。

**Bug ② 打款永不触发 = DAG 链撞死在 classify 的高度比对**
- 真因：`payout/engine.go` 的 `classify()` 对每个成熟块调 `node.BlockHashAt(height)` 做「主链 hash 逐字节比对」（防超发铁律，Bitcoin 类线性链的做法）。velkar 是 Kaspa DAG，**无 height→hash 索引**，`BlockHashAt` 是个无条件报错的 stub → `if err != nil { continue }` → 块永远卡 pending，永不 CONFIRMED，永不打款，且 continue 无日志=完全静默（现象：`totalBlocks>0` 但 `totalConfirmedBlocks=0`、打款 loop 零输出）。
- 修复（3 文件，守孤块判定不弱化）：
  1. `adapter/adapter.go`：新增 sentinel `var ErrNoHeightIndex`。
  2. `adapter/velkarrpc/velkarrpc.go`：`BlockHashAt` 返回 `adapter.ErrNoHeightIndex`；`Confirmations` 增强——读 `verboseData.isChainBlock`，块存在但**不在 selected parent chain**（红块/被甩块，coinbase 不被接受）→ 返回 -1 当孤块。**把 DAG 主链判定内建进 Confirmations**。
  3. `payout/engine.go` classify：`errors.Is(err, adapter.ErrNoHeightIndex)` 时跳过高度比对，信任 Confirmations。其他链（bitcoin/dragonx/midstate 有 height→hash）零影响。
- 前提已实链验证（探针 `velkar_probe_test.go`）：velkar fork 的 `GetBlock.verboseData.isChainBlock` **正确填充**（tip 回溯 12 块全 isChainBlock=true，conf 随 DAA 递增）。

**端到端铁证（5850U fast-maturity：coinbase 成熟 2 块 / genesis 易难度 / 10s 块）**
- 池日志：块 175-182 全 `→ CONFIRMED，PPLNS 分账` → `打款 batch=2 txid=dbce0c…` / `batch=3 txid=a39c1b…`；walletd `Submitting/Submitted to rpc`。
- API：`blocks 11 / confirmed 8 / orphaned 0 / totalPaid 15.2`。
- **到账铁证（探针 `velkar_balance_probe_test.go` 查 GetUtxosByAddresses）**：矿工地址 `velkartest:qpdc2s0pj8…` 收到 **55.10 TVELK，11 笔全非 coinbase**（= 真从池打款来的）；池地址全 coinbase 0 笔非 coinbase。

**⚠ 遗留：Kaspa storage mass（KIP-9）偶发打款失败（非致命）**
- 现象：`batch=1 sendmany 失败: Storage mass exceeds maximum（人工核对，绝不自动重发）`，batch=2/3 成功。
- 机理：KIP-9 storage mass ∝ 输出数 / 输出金额（**小额输出 mass 极高**）。testnet fast 出块的极小额 PPLNS 分账触发；walletd `send_many` 走 PSKB generator（`accounts_pskb_create/sign/broadcast`），generator 理应把大 bundle 自动拆多 tx（broadcast 返回多 txid），但单笔仍超 mass 时报错。
- 容错正常：失败批 `RefundPayout` 退回余额、下轮重组批（不同 UTXO 组合）成功——不丢币、不自动重发（守铁律 C4）。
- 主网风险较低（minPayout 0.1 过滤极小输出），但真矿工多、批量大时需观察。优化方向（未做）：池侧遇 mass 错误把批次拆更小重试 / 调 generator fee_policy / 抬 minPayout。

**代码落地**
- Go 3 文件（本地 canonical 已改 + 5850U `~/build/pcv/pool-core` 已同步）：`adapter.go` / `velkarrpc.go` / `engine.go`。`go build ./...`+`go vet`+`go test ./internal/payout/` 全绿。新二进制 `~/build/pcv/pool-core/ntmpool-velkar` sha `cf0c91f4…`。
- Rust 1 文件（5850U worktree `~/build/velkar-fastmature/wallet/daemon/src/main.rs`，原文件备份 `.bak-prestart`）：加 `wallet.start()`。`cargo build --release -p velkar-walletd` 通过，二进制 `target/release/velkar-walletd`。
- 探针 test 2 个（仅 5850U，未进 canonical）：`velkar_probe_test.go`（isChainBlock）/`velkar_balance_probe_test.go`（GetUtxosByAddresses 到账）。

**🎯 上主网前（用户确认后）**：① 主网源 `velkar-core-main` walletd 同样加 `wallet.start()`（连 SendMany patch 一起，回滚 VKDBG/resolver 调试标记）→ 编主网 `velkar-walletd`；② 主网 ntmpool-velkar 用含本 3 修复的树重编（velkarhash.go 主网算法already）；③ 部署 103.80（见 §7.6）+ confirmations 调 ~40（成熟 30，§7.5）+ payout.enabled=true。

### 7.8 ✅ storage mass 打磨（2026-07-15，用户选「先打磨再上主网」；testnet 深挖收官）

**结论先行：velkar 打款的第一要务是把 minPayout 定够高（建议 ≥ 0.5，实操用 20 VELK 最干净）。** storage mass 与 UTXO 碎片都是「输出金额太小」引发的，抬高起付额从根上消除。

**根因（KIP-9 storage mass）**：单笔 tx storage mass = `C·(Σ1/output_sompi − |I|²/Σinput)`，`C=STORAGE_MASS_PARAMETER=SOMPI_PER_VELKAR*10_000=1e12`，单笔上限 `MAXIMUM_STANDARD_TRANSACTION_MASS=100_000`（velkar-core `consensus/core/src/constants.rs` + wallet-core `tx/mass.rs`）。矿池打款=大 coinbase 拆小额矿工付款，是 storage mass 重灾区：
- 单个 **0.1 VELK** 输出 storage mass ≈ C/1e7 = **100_000 = 恰好上限**，**加上找零输出就超 → 拆到单笔也发不出去**（实测 20×0.1 拆 20 单批全被拒，ErrNotBroadcast 全部安全退回）。
- 单个 **0.5 VELK**：storage mass ≈ 20_000，一笔可拼几个。
- 单个 **20 VELK**：storage mass ≈ **500**，一笔轻松放 10+ 个，PlanBatches 根本不拆。

**实测铁证（fast-maturity，持续挖矿、walletd 重扫 UTXO）**：`cmd/vkpaytest`（walletd NewAddress×N → PlanBatches → 逐子批 SendMany）：
- 20×0.1 → PlanBatches 拆 20 单批 → 全 storage mass 拒 → ErrNotBroadcast 全退回（不冻结）。
- 20×0.5 → 拆 5 批各 4 输出（旧 budget 90k）→ 一度返回 txid 但 **未上链**（4 输出+找零实际超 100k，节点拒/或链式 orphan）→ 暴露 budget 漏算找零。
- **10×20 VELK → 1 批 10 输出 → SendMany 成功 → 探针查链上：10/10 地址真收到 200 VELK 全非 coinbase，txid 已离 mempool（打包上链）**。✅

**两类边界坑（都由「低额+高频+出块不稳」触发，minPayout 20 全规避）**：
1. **storage mass 超限**：输出太小。修=PlanBatches 按 storage mass 贪心装箱（`velkarwallet.PlanBatches`）+ ErrNotBroadcast 退回；budget 从 90_000 调保守到 **50_000**（给找零输出预留一半空间）。但 <~0.15 VELK 的输出结构性无法打款，budget 救不了 → 靠 minPayout。
2. **orphan / 未确认交易链**：打款太频繁 + 出块慢时，后一笔花了前一笔还没上链的找零 UTXO → 节点 `is an orphan where orphan is disallowed` 拒绝。walletd 重启（重扫链上已确认 UTXO）可清幻影。生产靠：minPayout 高（交易少、找零少）+ 打款间隔 > 确认时间 + 稳定出块。

**零钱合并**：Kaspa wallet-core generator **自动做 compound transaction**（打款需很多小 UTXO 时先合并再付，broadcast 返回多 txid，`velkarwallet.SendMany` line 128 已处理多 txid）。**不需手动合并**。高 minPayout 下 UTXO 集自然健康。

**⚠ 未硬化（低额场景才触发，主网用 20 VELK 不碰；记为待办）**：
- walletd 对「交易进 mempool 后被淘汰/orphan」与「真上链」无法区分（Kaspa 无 txindex，`velkarwallet.TxConfirmations` 注释已承认）——低额链式交易若被淘汰，池可能误判已确认。对成熟 coinbase 打款正常不触发。
- 若将来要支持低额起付（<0.5），需给 walletd 加「broadcast 后轮询确认 / 花 UTXO 前校验父交易已确认」。

**代码落地（本次新增/改）**：`adapter.go`（+ErrNotBroadcast、+BatchPlanner 接口）、`velkarwallet.go`（+PlanBatches 装箱、+storage mass 哨兵、budget 50k）、`engine.go`（payout 拆 payoutOneBatch 分子批 + ③ ErrNotBroadcast 安全退回）。单测 `velkarwallet_planbatches_test.go`（装箱/并集/不超预算）+ payout/accounting 回归全绿。新二进制 `ntmpool-velkar` sha `9b1f05d0`。探针 `cmd/vkpaytest` + `velkar_balance_probe_test.go`(读地址文件查到账) + `velkar_txid_probe_test.go`(查 mempool)（仅 5850U）。

**主网 config 建议**：`minPayout ≥ 20`（或至少 0.5）、`confirmations ~40`、`payout.intervalSeconds` 别太密（留找零确认时间，如 ≥60s）。
