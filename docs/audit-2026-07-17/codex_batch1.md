你是资深 Go 工程师，为一个生产多币矿池 NTMPool 做**攻击面/拉黑 IP 护栏**加固。仓库根：`D:\lj2ls\通用锄头+矿池\pool-core`（Go module `github.com/scashcc/ntmpool`）。

## 背景（必读）
先读这三份文件理解需求与真实事故：
- `docs/10-底层审计-差距清单与修复方案.md`（§2 A档 = 本次任务，§1 已达标项=禁改，§4 十条不变量）
- `docs/09-中转机真实IP改造方案.md`（真实事故：中转机 MASQUERADE 抹 IP → autoban 封中转机 → 全体矿工连坐掉线；§5.2 静默无日志=排查地狱）
- `_knowledge/pitfalls/中转机MASQUERADE抹IP-autoban误封全员.md`
再读这些现有代码摸清结构（**它们大多已达标，只允许在其上叠加，不许重写核心逻辑**）：
- `internal/stratum/autoban.go`、`internal/stratum/proxyproto.go`、`internal/stratum/util.go`（remoteHost）
- `internal/banlist/banlist.go`、`internal/config/config.go`
- stratum 连接治理主流程（accept/握手/BanChecker 处，自己 grep 定位）

## 本次要实现（A1+A2+A3+A4+C2，五项）

### A1. banlist 受保护名单 + autoban 爆炸半径护栏 + 坍缩检测（本次核心）
1. `banlist.List` 增「受保护名单」(protected CIDR 列表，从 config 注入)：中转机/节点/admin/VPN 网段。
   - `Ban()` 若 target 命中受保护名单 → **拒绝写入，返回哨兵错误 `ErrProtected`，并打 P0 日志**（谁想 ban 谁、命中哪条保护规则）。受保护名单永远 ban 不进去。
   - 提供 `IsProtected(ip string) bool` 查询。
   - 受保护名单变更走 config（热加已有机制的话接上；没有就先支持启动加载）。
2. autoban（`autoban.go`）触发 ban 前先查 `IsProtected`；命中→不 ban，改为**告警日志 + 该连接单独断开**（坏 share 的连接该断还断，只是不写 IP ban）。
3. **坍缩检测**：在端口/币维度维护「近期不同源 IP 数」与「总连接数」。某端口活跃连接 ≥N（默认 20）且其中 ≥P%（默认 90%）来自同一个源 IP → 判定 **PROXY 链路失效或 NAT 抹 IP**（而非真攻击）→ 自动**临时禁用该端口的 IP-autoban**（进入「仅按连接/会话断」模式）+ P0 告警 + 打点。链路恢复（IP 重新分散）后自动解除。阈值走 config，给合理默认值。

### A2. autoban / 拒连 结构化日志（消灭静默）
- `autoban.go` 里 `_ = a.banner.Ban(...)` 改为：检查返回值 + 打结构化日志 `target/reason/strikes/ttl/coin`。
- BanChecker 拒连处打一行日志（含 verified 真实 IP + reason），**带采样/限频**防日志洪水（每 IP 每分钟最多 N 条）。
- 连接 accept/close 打点（含 verified IP + provenance）。
- 铁律：任何自动 ban/拒绝/降级动作**必须留日志**（这是上次矿工断线数周、日志零线索的根治）。

### A3. verified_client_ip 与 transport_peer_ip 显式分型
- 在连接对象（或其治理上下文）上显式区分三个字段：
  - `TransportPeerIP`：socket 直接对端（可能是中转机）
  - `VerifiedClientIP`：**仅当**直接对端属于受信 edge 且成功解析 PROXY 头时才生成的真实矿工 IP
  - `IPProvenance`：枚举 `DIRECT` / `PROXY_V1` / `PROXY_V2`
- **autoban / maxConnsPerIp / 坍缩检测 只吃 `VerifiedClientIP`**；拿不到 verified（无 PROXY 头或非受信来源）时 → **只按连接/会话断开，绝不按 IP 写 ban**。
- PROXY 解析失败**绝不能退化成「把中转机 IP 当矿工 IP」**——失败就记为链路错误。
- 「受信 edge」判定：`proxyProtocol=required/optional` 且直接对端 `TransportPeerIP` 在受信转发机名单内（可复用 A1 的受保护/受信名单或新增 trusted-forwarder 名单，二选一，说明你的选择）。

### A4. 分级校验 + 有界资源自查补齐（防垃圾数据 DoS）
先 grep 现状，**已有的别重复做**，缺的按 Q4 阈值补（阈值走 config 给默认值）：
- 未 authorize 就发 submit/狂发消息 → 计数超限秒断
- 单条消息字节上限（默认 32KiB，硬上限 64KiB）+ JSON 嵌套深度上限
- 未授权累计字节/消息数上限、握手超时、subscribe 不 authorize 超时断
- 昂贵 PoW 验证（RandomX/Equihash 类）：先做廉价检查（长度/job存在/dup/nonce范围）再进**有界并发队列**，队列满时对低信任连接施加 backpressure 而非无脑 OOM
只补缺口，写清楚你补了哪些、哪些已存在。

### C2. proxyproto 兼容 v2（低风险增量）
- `proxyproto.go` 解析器改为**同时认 v1 文本头和 v2 二进制头**（按首字节/签名嗅探：v2 magic = `\x0D\x0A\x0D\x0A\x00\x0D\x0A\x51\x55\x49\x54\x0A`）。
- 解出的真实 IP 同样只填入 `VerifiedClientIP`。HAProxy 侧继续 send-proxy(v1) 不受影响，未来可切 v2。

## 硬性要求
1. **禁改** docs/10 §1 已达标项的核心逻辑（打款/守恒/孤块/vardiff/金锚等），本次只碰 stratum/banlist/config。
2. 所有新逻辑**带单元测试**（table-driven）：受保护名单拒 ban、坍缩检测触发/解除、verified vs transport 分型、proxyproto v1/v2 解析、分级校验边界。
3. 阈值/名单全部走 config（`internal/config/config.go` 加字段 + JSON tag + 合理默认），不硬编码。
4. 日志用项目现有 log 风格（grep 看现有 `log.Printf` 格式对齐）。
5. 金额/共识无关，纯连接治理层。
6. 若本机有 Go 工具链，跑 `go build ./... && go vet ./... && go test ./internal/stratum/... ./internal/banlist/... ./internal/config/...` 并贴结果；没有 Go 就说明，不要臆造测试通过。
7. 改完输出：改了哪些文件、每个文件干了什么、新增哪些 config 字段及默认值、哪些 A4 项是新补/哪些已存在、以及你无法自测的部分。

现在开始：先读上述文件，再动手。用中文写注释和总结。
