# 08 · Velkar (VELK) 接入 NTMPool 设计

> 第 12 币 Velkar = **Kaspa(rusty-kaspa) fork + 魔改 VelkarHash**。矿池路线：**接进 pool-core 统一核心**（不用包内 Rust solo bridge——它无 PPLNS/费/打款）。第一期**先矿工端、打款延后**（用户 2026-07-14 拍板）。
> 姊妹文档：[06-dragonx接入设计] [07-midstate接入设计]。算法权威：`_knowledge/algorithms/VelkarHash-velkar-spec.md`。

---

## 0. 状态速览（2026-07-14）

| 部件 | 状态 | 位置 |
|---|---|---|
| VelkarHash 纯 Go hasher + KAT | ✅ **完成**（5850U go test ok，逐字节匹配 pow=000382fb…） | `internal/hasher/velkarhash/velkarhash.go` |
| Kaspa gRPC proto Go stub | ✅ **生成落地**（11391 行，package protowire） | `internal/adapter/velkarrpc/protowire/*.pb.go` |
| gRPC 双向流客户端 + NodeAdapter + Notifier | 🔨 **下一步（最大工程）** | `internal/adapter/velkarrpc/`（待写） |
| Kaspa stratum 方言 + ShareHandler | ⬜ 待做 | `internal/stratum/dialect_kaspa.go`（新） |
| velkar 作业管理器 | ⬜ 待做 | `internal/velkarjob/`（新，照 cnjob） |
| family 装配 + `feePercent:5` | ⬜ 待做 | `internal/coininstance/family_velkar.go`（新）+ 改 registry switch |
| WalletAdapter 打款 | ⬜ **第一期延后**（先手动 wallet/walletd） | — |
| testnet 联调 | ⬜ 待做 | 连 5850U velkard testnet-10 |

**估工作量**（Explore agent，生产级含 live 冒烟）：hasher 1-2d✅ / stratum 2-3d / gRPC 适配 4-7d / 钱包打款 3-6d(延后) / 作业+胶水 2-4d / 联调 3-5d → 约 **2-3 周先上矿工端**（打款延后）。

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

## 2. 下一步：gRPC 双向流客户端 + NodeAdapter（最大工程）

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
