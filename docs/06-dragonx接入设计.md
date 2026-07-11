# M5-DragonX：dragonx 接入 NTMPool 设计（含真打款）

> 2026-07-10 立项。目标：DragonX 迁 NTMPool，**10 确认即打款**（金库 350 DRGX 垫付，
> coinbase 满 100 确认成熟后 shield 回补金库）。本文=三路侦察（pool-core 接入点 /
> DragonX 规格与魔改 miningcore / NTMminer 客户端与符号前缀）+ 真机核查的合成结论。
> 规格出处：`_knowledge/algorithms/RandomX-dragonx-spec.md`、drg-xmrig `PROTOCOL.md`、
> 魔改 miningcore `mc-dragonx/Blockchain/DragonX/*.cs`、NTMminer `C:\ntmbuild\ntmminer\rx_dragonx.c`。

## 0. 真机已裁定的事实（2026-07-10，103.80.18.140 实查）

- **块头字节布局金锚 PASS**：真链块 #3131000，本地重算 `sha256d(173B)` == 块 hash 逐字节相等。
  - 140B = version(4 LE) | prevHash(32 内部序) | merkleRoot(32 内部序) | finalSaplingRoot(32 内部序) | time(4 LE) | bits(4 LE) | nonce(32)
  - 173B = 140B || `0x20` || rx_hash(32)；PoW 值 = sha256d(173B)，小端解释比 target。
- **GBT 真实形状**：节点直接给 `coinbasetxn`（required=true，data 现成 Sapling coinbase hex，
  coinbasevalue=300000000=3 DRGX）、`target` 64hex BE、`bits`、`height`、`finalsaplingroothash`、
  `transactions[]`(data/hash/fee)。**无 randomxseedhash 字段**（miningcore 也是自算 seed）。
- **seed epoch = interval 1024 / lag 64**（miningcore `DragonXCoinTemplate.cs` 值）：
  `seedHeight = (height-64)/1024*1024`，seed key = `getblockhash(seedHeight)` **display→internal 反转后 32B**。
  链上证据：R 地址两笔 `generate`（3.0001+3.0，3 万+确认）= miningcore 按 1024/64 挖的块被全网接受。
  ⚠ `RandomX-dragonx-spec.md §3` 写的「2048」**是错的**（标准 RandomX 值误带入），CI 真链 KAT（§4）过了就去订正 spec。
- **coinbase 收款**：节点 conf 无 `pubkey=` → coinbasetxn 默认付钱包第三地址 `RL6RzH6z…`(ismine=true)。
  **部署时在 DRAGONX.conf 加 `pubkey=02fb71818898422c9fe6a5a53f77926242bd5e470b31710c99f478666a7ece1a28`**
  钉死到 RWqGF（见 `_knowledge/地址簿.md`）。
- 金库 `zs1qknwvtq…` 余额 **350 DRGX**（垫付资金）；块奖励 3 DRGX/块；难度 ~907K；全网 ~466 KH/s。

## 1. 矿工线协议契约（池必须逐字节满足；NTMminer rx_dragonx.c + drg-xmrig PROTOCOL.md 双源一致）

- CN 方言：`login`(algo:["rx/dragonx"]) / `job` 推送 / `submit` / `keepalived` / `getjob`。
- job 字段：`blob`(280hex=140B) `seed_hash`(64hex=内部序 32B) `target`(**8hex compact-LE u32** 或 16hex LE u64) `job_id` `height`。
- 矿工只滚 blob[108:112] LE u32（从 0 起）；**[112:140] 原样保留** → 池必须 per-connection 在这 28B 里写连接 tag 分 nonce 空间。
- submit：`nonce`=64hex（**完整 32B** [108:140]）、`result`=64hex（**原始 rx_hash**，非 sha256d）、`id`(login rpcId)、`job_id`、`algo`。
- 矿工过滤：`LE_u64(sha256d(173B)[24:32]) < target`。share 与块同一度量。

## 2. 池端校验（双段哈希，miningcore 同款语义）

1. **badpow**：重算 `rx_hash = RandomX_dragonx(seed, blob140含矿工nonce)`，与 submit 的 `result` 逐字节比对。
2. **难度/块**：`pow = sha256d(blob140 || 0x20 || rx_hash)`，小端解释。share diff = **max(powDiff, rxDiff)**
   （miningcore 同款双接受：drg-xmrig/NTMminer 按 pow 过滤、legacy xmrig-hac 按 rx 过滤——rx 真实性已由 badpow
   重算证明，任一达标即计 share；双段哈希两个值本来都有，零额外开销。★2026-07-10 用户确认矿工现役默认锄头
   = 官方 drg-xmrig，兼容它是硬要求）。**块判定只看 pow**（pow 反转即真块 hash，天然免"blob 链先交后记"
   问题，但仍走意图先落库拿节点权威 hash 复核）。
3. nonce 校验：wire nonce 32B，[0:4]=搜索区，[4:8] 必须 == 池写入的连接 tag，[8:28] 必须 == 模板值（防伪造/跨连接重放）。

## 3. 设计决策

| 决策点 | 结论 | 理由 |
|---|---|---|
| coinbase | **用节点 coinbasetxn 原样**（不自组 Sapling tx） | GBT required=true 给现成的；免 clean-room 实现 Overwinter/Sapling 序列化；共识安全由节点背书。代价=无 coinbaseString 品牌、需钉 `pubkey=`。extranonce 走 nonce[112:140]，不需要动 coinbase（miningcore 也这么干） |
| merkle | coinbasetxn.hash + transactions[].hash（display 反转）跑标准 merkle | 0 交易时 merkle=coinbase hash |
| 双段哈希 | hasher 层新增 **TwoStageKeyedHasher** 可选接口：`(result, pow)` 双返回；cnjob 探测到则 badpow 比 result、难度/块比 pow | 173B 拼装（`||0x20||`）是 rx/dragonx PoW 定义的一部分 → 归 hasher；cnjob 只多一个 if，单哈希链零影响 |
| 库隔离 | 第二份 librandomx（**默认配置=dragonx 常量**，不加 -DRANDOMX_STOCK）+ **drgrx_ 符号前缀**（照抄 NTMminer zkrx-prefix.sh 三步：ld -r 合并 → objcopy --redefine-syms 加前缀 → 删 .group 打散 COMDAT） | pool-core 现有 rx/0 直链 build-stock 原名符号 → 新库必须改名避免撞名/串味（串味=挖废块）。dragonx 与 stock 差 5 个编译期常量，运行时 salt 表达不了 |
| BlobWork 映射 | NonceOffset=108, SearchLen=4, 连接 tag 4B@[112:116]，[116:140]=0；**wire nonce 回显 32B**（cnjob 需支持 wire 长度 > 池滚动区并校验回显） | 对齐 NTMminer/drg-xmrig 只滚 [108:112] 的契约 |
| target 下发 | `TargetCompactLE=true`（8hex compact-LE） | 两家矿工都支持；monero 惯例 |
| seed | 适配器按 1024/64 公式自算 + getblockhash + 反转 → BlobWork.SeedHash | GBT 不给 seed；miningcore 同款 |
| 打款 | 新写 z_* 钱包适配器实现 WalletAdapter：**SendMany 内吞 z_sendmany→opid→轮询 z_getoperationstatus/result→txid**（只查自己的 opid，绝不 drain-all——consolidate 脚本 opid-safe 铁律）；4 参调用 **绝不带第 5 参 privacy policy**（Hush 第 5 参=donation 坑）；只付 zs 地址；分页 ≤50 | 引擎零改动（engine.go 无 RawTxWallet 分支正好走同步 SendMany 语义）；crash 窗口(opid 未解析)v1 接受，守恒冻结兜底，硬化(落库 opid)列 M5.x |
| shield 回补 | 钱包适配器实现 **WalletMaintainer**（z_shieldcoinbase R→vault，opid 轮询，10min 超时先例）+ **引擎侧新增整备调度**（打款锁内先 Maintain 再 payout）——WalletMaintainer/ConsolidationConfig 目前只有定义无接线，dragonx 是第一个用户 | coinbase 100 确认成熟后自动回金库；金库 note 合并仍归主机 consolidate.service（opid-safe 互不抢） |
| 确认数 | `payout.confirmations = 10`（用户拍板）；分类器成熟前 BlockHashAt 逐字节比对防孤块 | 10 深度重组在 466KH/s 链上概率可忽略；垫付模型下 coinbase 成熟(100)与付款解耦 |
| 登录校验 | zs1 前缀 + z_validateaddress（miningcore 同款；t→z 之外的路径共识拒绝） | 矿工收款只能 zs |
| 算力口径 | multiplier=1 分支加 dragonx adapter 名 | zoka 2^32 教训 |

## 4. 金锚/KAT（全部三层，缺一不可）

1. **引擎合成锚**（NTMminer rx_kat 同组，dragonx 配置）：key=32×`0x42`，input=140B `blob[i]=i` →
   `4b6c85964d42800239dcc951ef71f9fd9de1fc5e313578cf56ecaf91ad1f1bef`
2. **外层 sha256d 独立锚**：blob[i]=i (140B)，rxh[i]=(i*7)&0xff (32B)，173B= blob||0x20||rxh →
   pow=`ef00f6d749b1726a4c2c8f3b816736d93a6097a1f4da52aa661a00676f638c85`，pow_value(LE u64 [24:32])=`0x858c636f67001a66`
3. **真链块 KAT（终极裁定，兼裁 seed 规则）**：块 #3131000。**173B 完整头 hex（346 字符，本地 sha256d 已验 PASS，唯一权威串——140B blob=前 280 hex、[280:282]=`20`、solution=后 64 hex，代码里从这一个串派生，别手拆）**：
   `04000000281a06c48ce20f3727e32c78e60df70010bb7c64628849f6a40b3741db000000b9243da497d12f810dee48eea15ea4bb45124b5dc4b89657241c19b402a578ba68e087a00909ce98e77050f16d9be11c048ef0451d037d9ea06811f4f4e62b57e00f516ada0e011e9e020e0001408ffb00000000000000000000000000000000000000000000000020e87d60da9cd2389b691403bb76466d98a520996643871a28be0585778ceb8915`
   - seed（ruleA 1024/64，seedHeight=3130368）display=`000000983fb35d229347a358e044007456d73679801aa698c850a7c0159ebfe1` → 反转 32B 作 key
   - 期望 rx_hash == solution = `e87d60da9cd2389b691403bb76466d98a520996643871a28be0585778ceb8915`
   - 期望 sha256d(173B) 反转 == 块 hash `00000044826c11188c2bab69afe0f6b02772899bf714152fb10e03ee9b0661d0`（此条已本地 PASS）
   - （若 rx 锚失败再试 ruleB seedHeight=3129344 hash=`000000548be77e6b68013f9387506ba68436ba021d929b68ac2efe16207b6d61`——但链上 generate 证据已强烈指向 ruleA）

## 5. 改动清单（pool-core）

| 件 | 类型 | 位置 |
|---|---|---|
| librandomx dragonx 构建 + drgrx_ 前缀脚本 | 新写 | `third_party/randomx`（同一份源，第二个 build 目录）+ `scripts/drgrx-prefix.sh` + ci.yml |
| `rx/dragonx` TwoStageKeyedHasher（badpow=rx、pow=sha256d(173B)，金锚×3） | 新写 | `internal/hasher/dragonxrx/` + `cmd/ntmpool/hashers_randomx.go` 空 import |
| TwoStageKeyedHasher 接口 + cnjob 双段编排 + wire-nonce 回显校验 | 改动 | `internal/hasher/keyed.go`、`internal/cnjob/cnjob.go`、`internal/adapter/blob.go`(加字段) |
| dragonx 节点适配器（GBT→BlobWork+coinbasetxn、SubmitBlob 组块 173B+txs、seed 自算、Status/BlockHashAt/Confirmations） | 新写 | `internal/adapter/dragonxrpc/`（RPC 底座抄 bitcoinrpc） |
| z_* 钱包适配器（SendMany 吞 opid；WalletMaintainer=z_shieldcoinbase） | 新写 | 同包 |
| 整备调度接线（打款锁内 Maintain） | 新写 | `internal/payout/engine.go` |
| `buildDragonXFamily` + switch case `"dragonx-rpc"` + multiplier=1 分支 | 新写/改动 | `internal/coininstance/family_dragonx.go`、`family_bitcoin.go:19`、`coininstance.go:140` |
| e2e：JSON-RPC 假节点(GBT/submitblock 真验) + CN 矿工(双段哈希) + z_* opid 桩 + 孤块路径 | 新写 | `internal/e2e/` |

## 6. 部署清单（Task 6，103.80）

1. DRAGONX.conf 加 `pubkey=02fb7181…1a28` → 重启节点（~10min 回放窗口，错峰）。
2. NTMPool 影子端口（≠5333）连真节点；NTMminer v1.8+ `-a dragonx` 冒烟：badpow=0、vardiff 收敛。
3. 真爆块 → 10 确认 ConfirmBlock → 金库 z_sendmany 垫付到测试 zs → opid→txid→确认全链路核实。
4. shield 腿：等 coinbase 100 确认 → Maintain 自动 R→vault 回补。
5. 切流 5333、退役 miningcore（配置备份已在 `pool-build/*.bak-*`）。
6. 矿池费：rewardRecipients 3% 语义改为 NTMPool feePercent + feeAddress（zs，与金库分离待定）。

## 7. 已知残留 / M5.x 硬化候选

- ~~`blocks_submitted_total` metric 埋点遗漏~~ ✅2026-07-11 核实为**误诊**：埋点自 M4(2658b00)
  就在共用 `blockSink()`（cnjob 路径经 onBlock 走它），真因=metrics 是进程内存态、systemd
  重启即清零，爆块这种低频事件一重启读数就归 0。已修：/metrics 增加**会计层真值 gauge**
  （SetTruthSource 每次抓取现读 PG：`ntmpool_blocks{coin,status}`、fees_accrued/uncollected、
  paid/miner_balance/debts_net，重启不丢，业务总量以这组为准）；`payouts_total` 加 kind 标签
  （payout/fee_collect/fee_sweep 分开计）；admin 单币视图新增 `uncollectedFees` 字段。
- opid 落库 + Recover 按 opid 归位 txid（闭合 z_sendmany crash 窗口）。
- z_sendmany 分页（当前 >45 收款人 fail-fast；真矿工多了再做）。
- ~~ZMQ 新块通知~~ ✅2026-07-11 孤块率修复三件套（commits 7a46bb9/0a4442e，生产已部署验证）：
  ①**GBT longpoll**（`dragonxrpc.LongPollNotifier`，挂等请求链头一动即回，比 ZMQ 少一次拉取 RTT；
  节点不支持时哨兵退出防忙轮询）——生产实测真实新块逐块「新块推送(longpoll)→模板已即时刷新」，
  节点重启断连自愈；②**纯 Go ZMQ**（`internal/zmqsub` 手写 ZMTP 3.0 SUB 零依赖，nodes[].zmq 配置即挂，
  家族无关）——⚠dragonxd 官方 release **未编 ZMQ**（strings 零 zmq 符号，conf 配 zmqpub 也不监听），
  该通道对 dragonx 静默待命，对其他币/换二进制后即用；③**节点入站 P2P**（listen=1+maxconnections=64+
  externalip+compose 映射 21768，公网可达实测）——出站硬上限 ~8 导致仅 7 peer 收块/传块都慢，
  是孤块的另一半根因。coininstance 落地 docs/02 Notifier 谱系：多通道并存 (Height,Hash) 去重取最先，
  2s 轮询兜底永远保留。
- ~~epoch 订正~~ ✅2026-07-10 CI 真链 KAT 绿后已订正 spec §3（1024/64 钉死）。
- ~~max(powDiff,rxDiff) 双接受~~ ✅已实现（用户确认矿工现役默认 drg-xmrig，legacy 兼容顺手做了）。
- ~~stale 洪峰~~ ✅已修（JobKey 高度去重，49%→0%，commit 9af48b4；沉淀 pitfall
  `blob链-job按JobKey去重-别让无关字节换工.md`）。

## 8. 实施记录（2026-07-10，代码全部落地 + CI 绿）

commits：`22af0df`（1/4 哈希器+前缀库+金锚）、`84157df`（2/4 管线+适配器+family+e2e）、
`9af48b4`（3/4 stale 修复 JobKey）。
- CI randomx job：双库构建（stock 原名 + dragonx drgrx_ 前缀）成功，`-tags randomx` 全量测试绿
  = **三层金锚全过 → seed 1024/64 终极裁定 + 无串味 + 引擎锚复现**。
- 主 job：真链块 #3131000 组头/序列化纯 Go 金锚 + dragonx 形状 cnjob 单测 + e2e
  （share→爆块(假节点真验)→10确认→PPLNS→z_sendmany(opid 轮询)→shield 回补 + 孤块不误打款
  + 同高度竞争块 1 confirm 2 orphan）全绿。

## 9. ★真实生产验证（2026-07-10，103.80，Task 6 收官）

部署 systemd `ntmpool-drg.service`（stratum 5333，PG=dragonx-postgres 的 ntmpool 库）→ 退役 miningcore
（`docker stop dragonx-pool`，可回滚）→ **真实矿工引流**（中转机 122.10.119.40 `iptables DNAT` 5 端口
→池 5333，回滚脚本 `/root/drgx-divert-revert.sh`，验完已回滚）。铁证（节点 + PG 独立核实，非日志转述）：
- **badpow=0**：11 个真实矿工（官方 drg-xmrig）连上，池端 rx/dragonx 重算与官方锄头逐字节一致
  = 核心共识正确性的生产铁证。
- **真爆块**：块 3131180 上主链（hash `00000050…d6fc7b`，节点 getblock 确认）。
- **同高度双爆块正确处理**（真实场景撞上）：PG blocks 表 3131180 一 confirmed 一 orphaned，
  另一块 submitblock 被拒 inconclusive → 判孤块，绝不双份入账。
- **10 确认 z_sendmany 真打款**：txid `2b4aea3679841d90…67bfadc`，金库出账 199.8999 note →
  矿工 `zs18g77w…`(finder) 1.8248 + `zs1j3s95c…` 1.0661 + 找零 197.0089 回金库（z_viewtransaction 铁证）。
- **stale 修复实测**：切修复版后 49%→0%，有效算力翻倍。
部署坐标/运维/回滚详见记忆 [[dragonx-drgx-coin]]、[[pool-server-103-80-18-140]]。
