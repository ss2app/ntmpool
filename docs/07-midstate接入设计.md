# 07 — midstate (MDS) 接入设计【唯一事实源】

> 2026-07-11 开工。用户拍板：**只做 PPLNS，coinbase 直付（爆块就到账），无池端转账腿**。
> 现役 fork 池（/opt/midstate/pool，stratum 3333）继续跑到 NTMPool 验证通过再切流
> （照 dragonx/btc09「引流-对拍-切流」三步先例）。
>
> 事实源出处：fork 池 `midstate-pool/src/main.rs`（103.80 /opt/midstate/pool/ctx，
> 已逐行通读）+ 官方节点 v3.5.1 源（103.80 /root/mref）+ NTMminer nm_stratum.c
> MID 段 / mid_core.c / mid_kat.c。本文档钉死全部契约，后续实现以此为准。

## 0. 一句话架构

第四条作业管线：`midstaterpc 适配器 × midvdf 哈希器 × midjob 作业管理 ×
dialect_midstate 方言 × 会计「直付块」扩展`。**打款引擎 enabled=false 永不开**
（没有打款腿），但 classify（确认/孤块）照跑——直付块的入账就发生在 ConfirmBlock。

## 1. 算法（midvdf，纯 Go clean-room）

PoW = 标准 BLAKE3 迭代（官方 `core/extension.rs create_extension`）：

```
x = BLAKE3(mining_midstate[32] ‖ nonce_le8)     // 40 字节短消息
重复 1_000_000 次: x = BLAKE3(x)                 // 32 字节短消息
final_hash = x；有效 = final_hash < target（32B 大端逐字节严格小于）
```

- `EXTENSION_ITERATIONS = 1_000_000`（core/types.rs:719；=100 的是 fast-mining 测试 cfg）。
- ≤64B 消息的 BLAKE3 = 单次压缩（flags = CHUNK_START|CHUNK_END|ROOT），
  照公开 spec 手写 compress（IV/消息置换表/G 函数），行为对齐 NTMminer mid_core.c
  ——零依赖、无泛型 hasher 开销（1M 次迭代对调用开销敏感）。
- **金锚**：① NTMminer mid_kat.c 的 6 个跨实现锚（midstate=0x00..0x1f，nonce 含
  high-32-bit/边界，期望值由 Rust 官方 blake3 genanchor.rs 跑出）逐字节比对；
  ② 真链块 KAT（见 §5）。注册 hasher.Register，进 SelfTestAll 启动门禁。
- Hasher.Hash(input) 契约：input = 40 字节（midstate‖nonce_le8），返回 final_hash。
  迭代数可注入（测试用小值），生产固定 1M。
- **验一条 share = 完整 1M 次重算（无捷径），纯 Go 预估 200~400ms/share**。
  防打爆：vardiff 12s/share/矿工 + midjob 内有界验证并发（channel 信号量，
  超载排队不丢）。fork 池同款问题（Rust 也要 ~50ms），vardiff 就是为此存在。

## 2. 节点 HTTP RPC 契约（midstaterpc 适配器）

节点 = 纯官方 v3.5.1（8545，容器 172.30.0.10）。全部无状态短连接（C9 铁律）。

| 端点 | 用途 | 要点 |
|---|---|---|
| GET /state | 状态轮询 | `{height, target(hex64), block_reward(u64 units), header_hash(hex64), is_syncing, ...}`；**is_syncing=true 时暂停模板**（上游明说；fork 池没做，我们做） |
| POST /block_template | 拿模板 | 请求 `{"coinbase":[{address:hex64, value:u64, salt:hex64},…]}`；**每个 value 必须非零 2 的幂**；总额必须 == block_reward+total_fees，不符回 4xx `{"error":"Coinbase mismatch. Expected: <N>"}` → 解析 N 重建重试 ≤4 次（fork 池同款）；200 回 `{mining_midstate(hex64), target(hex64), batch_template(完整 Batch JSON), total_fees, block_reward}` |
| POST /submit_batch | 交块 | body = batch_template 原样 + `extension` 替换为 `{"nonce":<u64>,"final_hash":[32 个字节数组]}`（fork 池 `recomputed.to_vec()` 同款编码）；200 `{accepted:true}`；⚠「Block validation timed out」是已知假阴性（块可能已落地）——留给分类器按链归位，不当失败重交 |
| GET /block/{h} | 孤块分类 | 返回该高度 Batch JSON（witness 已剥）；**块身份比对 = batch.extension.final_hash**（我们记录的块 hash 就是 hex(final_hash)，见 §4） |
| POST /scan | 对账（后备） | `{addresses:[hex64], start_height, end_height}`，≤1 万高度/次；查费地址实收核 §6 守恒外部一致性 |

**mining_midstate 的真身**：= `compute_header_hash(candidate_header)`——节点把
coinbase 折进状态、锁定 timestamp 后算出的 **header hash**（node.rs finish_template；
不是 post_tx_midstate，上游注释点名 web 矿工栽过）。池对它**只做透传**，不重算。

**模板重切时机**：/state 的 (height, header_hash, target) 任一变化才重切。
⚠绝不同高度无因重切：方言 stale 守卫是 job_id==height，同高度重切会让在飞 share
撞上新 midstate 被误判 badpow。链停摆期 timestamp 变陈旧是已知边界（fork 池同款，
接受；「停摆期 target mismatch」疑云由 header_hash/target 变化检测覆盖大半）。

## 3. 矿工方言（dialect_midstate，逐字段复刻 fork 池）

行分隔 JSON 裸 TCP（NTMminer `-a midstate` 现役发行版零改动，nm_stratum.c:1027 起）：

```
矿工→池  {"id":1,"method":"subscribe","params":{"address":"<hex64>","device":"cpu"}}
池→矿工  {"id":1,"result":{"job_id":H,"midstate":"<hex64>","target":"<hex64>"}}
         或 {"id":1,"error":"pool warming up"}
池→矿工  {"method":"job","params":{"job_id":H,"midstate":"…","target":"…"}}   (tip 变化/submit 后推)
矿工→池  {"id":N,"method":"submit","params":{"job_id":H,"nonce":<u64 数字>,"final_hash":"<hex64>"}}
池→矿工  {"id":N,"result":{"status":"accepted"|"block"}} | {"id":N,"error":"<原因>"}
```

逐条语义（= fork 池 stratum mod 行为，错误串逐字复刻）：
- 首行必须 subscribe，否则回 `{"id":…,"error":"expected subscribe first"}` 断连；
  address 必须 64-hex（32B），非法直接断连（fork 池 anyhow bail 同款）。
  device 清洗：alnum/-/_、≤16 字符、小写、空→"cpu"（仅统计展示）。
- job_id = 模板高度。submit 的 job_id 若带且 ≠ 当前高度 → `"stale share (job expired)"`。
- nonce 缺失 → `"submit missing nonce"`；final_hash 可选，带了必须与池端重算逐字节
  一致，否则 badpow tripwire → `"final_hash mismatch (miner hash != pool recompute)"`。
- 难度不够（含一步 grace 后仍不够）→ `"insufficient PoW for share"`（lowdiff，良性）。
- 非法 JSON 行 → `{"error":"bad json: …"}`（无 id，不断连）；未知方法 →
  `{"id":…,"error":"unsupported method: <m>"}`；空行忽略。
- **每次 submit 应答后无条件重推一次 job**（同高度、最新 vardiff target）——fork 池
  同款；NTMminer 只在 midstate 真变时丢弃在飞批次，同 midstate 换 target 不丢工。
- **(job,nonce) 连接内去重**（fork 池没有，我们加，防重放双计；诚实矿工永不触发）
  → `"duplicate share"`。
- **矿工 nonce 自播种**（时间种子起点，无池端窗口分区）→ 不做 nonce 窗口校验。

**vardiff**：internal/vardiff 本来就是从这套 fork 池设计蒸馏的（一步 grace/限频/
lowdiff 分类三件套）。差异于 btc09：**retarget 后立即重推 job**（新 target 随
submit-ack 后的 job 推送下发；矿工 nonce 不重叠，无 duplicate 风险——btc09 的
pendingDifficulty 顾虑不存在）。参数对齐 fork 池：target 12s、EMA 0.3、死区 1.8、
限频=target 周期；难度标尺见 §4。share target 渲染 = max256/diff 截断 64-hex
大端（连续值，不量化到 bits——NTMminer 只做 `final_hash < target` 比较，兼容）；
**target 钳制在网络 target**（share 绝不比爆块难，fork capped_share_target 同款）。

## 4. 难度标尺 / 算力口径 / 块身份

- 难度标尺 = `diff = (2^256−1) / target`（连续，multiplier=1）：1 难度 ≈ 1 次 VDF。
  份额权重、netDiff、PPLNS 窗口（`pplnsFactor × netDiff`）、hashrate（ext/s）全同标尺。
  fork 池 `2^(bits−4)` 权重与本标尺成正比，分账语义一致。
- vardiff 起始难度 ≈ 2^15=32768（fork SHARE_DIFFICULTY_BITS=15），min 16(≈2^4)，
  max 上不封顶（钳网络难度）。
- **全网算力：无真值口径 → API 省略**（照 blob 链先例；绝不用难度反推，铁律 C7）。
- **块身份 = hex(final_hash)**。midstate 的 header_hash 不含 extension（同模板两个
  nonce 解 header_hash 相同），final_hash 才每解唯一；/block/{h} 直接可比。
  RecordBlock 占位 hash 就用它（本地即知链上真身份，同 btc09 先例免 UpdateBlockHash）。
- Confirmations(hash,h) = 链上 /block/{h}.extension.final_hash == hash ?
  (state.height − h + 1) : −1。确认数配置默认 8（无成熟期但防浅 reorg）。

## 5. 真链 KAT（终极锚，离线可跑）

选一个**纯 coinbase 空块**（全链 ~88% 是空块）嵌入 testdata：
1. 从 /block/{h} 与 /block/{h-1} 取 batch 与父块；
2. 本地重放：`post_tx_midstate = fold(prev_midstate, coinbase coin_ids…, state_root)`
   （coin_id = compute_coin_id(address,value,salt)，fold = hash_concat 链，全 BLAKE3）；
3. `mining_hash = blake3(prev_header_hash ‖ post_tx_midstate ‖ state_root ‖
   timestamp_le8 ‖ target)`；
4. `midvdf(mining_hash, nonce) == final_hash` 逐字节 && `< target`。

一次性钉死：coin_id/折叠/header hash/VDF 整条挖矿共识路径 vs 真主链。
（此重放代码只活在测试里；生产池对 mining_midstate 纯透传。）

## 6. ★核心新概念：coinbase 直付分账（会计「直付块」扩展）

### 与 fork 池的关键差异

fork 池 = 回合制清账（share 表累加、爆块清零）≠ PPLNS。平移后换成 **NTMPool
标准 PPLNS 窗口**（用户拍板），分账时点从「确认时」前移到「**模板构建时**」：

### 模板时（midjob.planSplit，分账数学只在这一处）

```
E  = block_reward + total_fees（"Expected: N" 重试后收敛值）
D  = E × (1 − feePercent/100)                     // feePercent=5（热参数）
窗口快照 w_i（ledger.DirectPlanInputs：PPLNS 窗口 + 可用 carry）
c_i = floor(D × w_i / Σw)                          // 该块 PPLNS 应得（credit）
p_i = quantize(c_i + carry_i)                      // 实付（coinbase 输出）
      quantize = 取二进制展开的最高 K 位（K=maxCoinsPerMiner 默认 6），
                 且 p_i < minPayoutUnits（默认 2^20）→ p_i = 0（防尘埃 UTXO 轰炸，
                 15eef1fa 两万碎币惨案的教训）；按权重排序取前 maxPayoutMiners=300
费输出 = E − Σp_i（吸收 5%+取整+尘埃 carry；decompose 成 2 的幂 → PoolAddress）
每输出 salt = blake3(height_le8 ‖ index_le8)（fork derive_salt 同款）
输出总数守恒校验：Σ所有输出 == E；总输出数 ≤ 300×K+64 ≪ MAX_BATCH_OUTPUTS=10000
```

carry_i = 该地址可用结转 = balance_i − 在飞预留（status∈{submitting,pending} 直付块
的 Σmax(p−c,0)）− debts_i，floor 0。**在飞预留防同一笔 carry 被连续两个模板双付**。

### 账本扩展（Mem/PG 双实现 + conformance 钉死）

- `core.FoundBlock` 加 `Direct []DirectCredit{Address, Credit, Paid}`（十进制字符串）。
- RecordBlock：Direct 非空即存分账快照（PG：block_credits 行提前到 record 时写入，
  新增 `paid NUMERIC` 列，幂等 ADD COLUMN）。
- ConfirmBlock（直付块，忽略传入 feePercent，用快照）：
  - `totalPaid += Σp`、`paidByAddr[i] += p_i`（真付出去了，txid=块 hash 写 payments
    展示行，矿工自查 recentPayments 直接可见「已随块直付」）
  - `balances[i] += c_i − p_i`（正=尘埃结转；负=carry 兑付回收；正增量先抵 debts）
  - `totalFees += E − Σc_i`（计提口径）
  - 守恒自检：E = Σp + Σ(c−p) + (E−Σc) ✓ Reconcile delta=0
- OrphanBlock（已确认的直付块回滚）：上述三项整体反转。**coinbase 没上链=谁都没
  拿到，无 debts、无追缴**——比垫付孤块干净得多。未确认直付块孤掉 = 无账务动作。
- **费地址是 carry 的链上代管方**：链上费地址累计实收 = totalFees + Σbalances
  （+debts 修正）。外部对账（/scan 费地址）核这条恒等式。
- Solo 不做（用户拍板只 PPLNS；直付 solo 需要每连接一份模板，架构上另一回事）。

### 为什么费侧吸收而不是「不够就不切模板」

模板必须随时可切（矿工不能等），而窗口/carry 是连续变量——把凑整残差
全部推给费输出是唯一能让「每个输出 2 的幂 + 总额精确 == E」两约束同时成立
且不伤矿工的解（fork 池同款思路，我们把误差记回 balances 不吞）。

## 7. 拼装（family_midstate）

- `adapter="midstate-rpc"`，`algo="midvdf"`，decimals=0（units 就是最小单位，
  无小数；金额字符串=整数 units。网页展示 gMDS=1e9 units 是前端口径）。
- PoolAddress = 费/残差收款地址（现役 `2190f894…`，pool.redb MSS 地址，
  NTMPool 不持钥，提现走既有 consolidate SOP）；FeeAddress 留空（无 sweep 腿）。
- payout.Enabled=false 永不开；Confirmations=8；feePercent=5；pplnsFactor=2。
- WalletAdapter = stub（SpendableBalance="0"，SendMany 报错）——classify 用不到钱包。
- 端口 dialect="midstate"，vardiff {start 32768, min 16, target 12s}。
- HashPSSource=nil；notifier 无（1.5~2s /state 轮询兜底已够，fork 池同款泊松）。
- midjob 实现 jobPipe（Refresh/Snapshot）+ 方言 handler 接口；
  Refresh 里做 §6 模板流（含 Expected 重试）；blockSink 走 family 共用（意图先落库,
  hash=final_hash 本地即真身份）。

## 8. 部署/切流清单（代码收官后，用户在场执行）

1. 5850U 出 linux 二进制（GitHub 封停期过渡编译机）→ 103.80 新 systemd
   `ntmpool-mds`（PG 库 ntmpool_mds@ntmpool-pg，stratum 13333 内网试跑）。
2. 影子期：真节点模板冒烟 + NTMminer -a midstate 直连打 share，对 fork 池
   （3333 照跑）对拍 badpow=0；**首个真块 = 直付契约的终极验证**（coinbase
   被节点收下 = 2 的幂/总额/字节序全对）。
3. 切流：209 转发落地端口翻到 NTMPool（照 zoka/btc09 先例，回滚=翻回）。
4. fork 池退役前把 shares 表余量清账（最后一块直付或人工补付），矿工无感。
5. 网页 ntmminer.com 加 midstate 注册表项（等切流后）。

## 9. 风险与边界（明知接受）

- 链停摆（80% 鲸鱼 cddc777c 停挖）期间模板 timestamp 变陈旧：fork 池同款行为，
  链恢复即自愈；若复现「target mismatch」按 §2 的 header_hash/target 变化检测排查。
- 深 reorg（>8 确认）翻已确认直付块：balances 反转可致负余额（未来 credit 抵扣），
  与全行业池同风险级别，Reconcile 可见。
- Go VDF 验证吞吐（~3-5 share/s/核）：矿工数 ×12s vardiff 下限有裕量；
  真不够再上 SIMD/asm 或 cgo（性能优化不进本期）。
- submit_batch「validation timed out」假阴性：不重交、留分类器归位（memory 教训）。
