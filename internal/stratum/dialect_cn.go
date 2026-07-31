// CryptoNote 方言（XMRig 系 login/job/submit）。docs/04 §2 的实现。
//
// 兼容清单（调研坐实）：
//   - login params：login（地址[.难度][+worker]）/pass/agent/rigid/algo 数组（能力协商）
//   - 响应 result={id, job, status:"OK", extensions:[...]}；job 必须带 algo；
//     RandomX 家族 seed_hash 必须恰好 64 hex
//   - worker 识别顺序：rigid > pass（剔除 "x"/空；worker:email 取 : 前）> login 的 +worker 后缀
//   - 固定难度：login 后缀 .N 或 +N；密码参数 d= 同样生效（与 V1 一致）
//   - keepalived → {"status":"KEEPALIVED"}（XMRig 60s 一发）
//   - 错误 message 用事实标准集合（Unauthenticated/Invalid job id/Duplicate share/
//     Low difficulty share/…），锄头端有匹配逻辑，不要自创
//   - nicehash 分片模式：池钉 nonce 高字节，矿工只滚低 3 字节（login extensions 声明）
//
// 链差异（blob 布局/target 编码/hash 字节序/组块提交）全部下沉到 CNShareHandler
// （internal/cnjob 实现）；本文件只做 wire 协议。
package stratum

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/metrics"
	"github.com/scashcc/ntmpool/internal/minersettings"
	"github.com/scashcc/ntmpool/internal/vardiff"
)

// CNWireJob 下发矿工的 job 全部字段（handler 物化好，dialect 原样上线）。
type CNWireJob struct {
	JobID    string `json:"job_id"`
	Blob     string `json:"blob"`
	Target   string `json:"target"`
	Algo     string `json:"algo"`
	Height   uint64 `json:"height"`
	SeedHash string `json:"seed_hash,omitempty"`
	// BrvaJobMode xmrig-brisvia 专用：任务模式声明。"pplns" = 跳过其 solo
	// coinbase 收款校验（矿池 coinbase 付池地址，缺失该字段任务被拒 code 8）。
	BrvaJobMode string `json:"brva_job_mode,omitempty"`
}

// CNSubmission 一条已解析的 CN submit。
type CNSubmission struct {
	ConnID    uint32
	Address   string
	Worker    string
	UserAgent string
	RemoteIP  string
	JobID     string
	NonceHex  string
	ResultHex string // 矿工声称的 hash（badpow tripwire 的比对对象）
	// Judge 难度归属判定（vardiff 一步 grace 在 dialect 侧的连接状态里）：
	// 输入 share 实际难度，返回 (计权难度, 是否达标)。
	Judge func(shareDiff float64) (creditDiff float64, ok bool)
	Solo  bool
}

// CNConnectionIDAllocator 是 CNShareHandler 的【可选】能力：池化分配连接 tag。
//
// 为什么需要：连接 tag 写在 nonce 字段高位，是矿工之间唯一的区分手段（见
// cnjob.SetConnIDSpace）。窄 tag 的币（BRVA：nonce 4 字节 - 搜索区 3 字节 = tag
// 只有 1 字节 = 256 个取值）若用纯自增序号当 tag，**累计第 257 个连接就与第 1 个
// 撞上**——两个在线矿工拿到逐字节相同的 blob、从 nonce 0 扫出完全相同的 hash 序列，
// 池的有效算力被压成单机，而池侧所有指标（share/算力/分账）全都正常，只有爆块率
// 不随算力增长。⚠ 是【累计】连接数不是并发数：断线重连、vardiff 调档、网络抖动
// 都在累加，繁忙的池一天轻松破 256。
//
// handler 实现本接口时，Serve 用它分配/归还 tag（在线唯一、断开即还、满员拒连）；
// 不实现则回退纯自增（宽 tag 的币如 dragonx 行为不变）。
type CNConnectionIDAllocator interface {
	// AcquireConnectionID 取一个当前在线唯一的 tag；空间满时返回 false。
	AcquireConnectionID() (uint32, bool)
	// ReleaseConnectionID 连接结束时归还 tag 供复用。
	ReleaseConnectionID(connID uint32)
}

// CNShareHandler 是 CN 方言与作业管线的解耦点（internal/cnjob 实现）。
type CNShareHandler interface {
	// Algo 本币算法名（login 能力协商 + job.algo）。
	Algo() string
	// LoginExtensions login 响应的 extensions（algo/keepalive [+nicehash 分片模式]）。
	LoginExtensions() []string
	// ConnJob 为一条连接物化当前 job（写连接 tag、清零搜索区、按难度编 target）。
	// ok=false 表示模板未就绪。
	ConnJob(connID uint32, difficulty float64) (CNWireJob, bool)
	// HandleSubmit 校验一条提交（池端重算 + badpow tripwire + 命中检测 + 组块提交 + 记账）。
	HandleSubmit(ctx context.Context, sub CNSubmission) SubmitResult
}

// CNDialect 实现 stratum.Dialect。每个币一个实例。
type CNDialect struct {
	coinID  string
	handler CNShareHandler
	connSeq atomic.Uint64
	conns   sync.Map // *cnConn → struct{}
	banner  Banner   // 协议层自动 ban（可选）

	onAuth func(addr, worker string, p minersettings.PasswordParams)
}

func NewCNDialect(coinID string, h CNShareHandler) *CNDialect {
	return &CNDialect{coinID: coinID, handler: h}
}

// SetAuthHook 注入授权钩子（矿工设置 mp= 等持久化；启动时一次）。
func (d *CNDialect) SetAuthHook(h func(addr, worker string, p minersettings.PasswordParams)) {
	d.onAuth = h
}

// SetAutoBan 注入自动 ban 写入口（启动时一次）。
func (d *CNDialect) SetAutoBan(b Banner) { d.banner = b }

func (d *CNDialect) Name() string { return "cryptonote" }

// ConnCount 当前在连矿工连接数。
func (d *CNDialect) ConnCount() int {
	n := 0
	d.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastJob 新块到达时推最新 job 给所有已登录矿工。
func (d *CNDialect) BroadcastJob() {
	d.conns.Range(func(k, _ any) bool {
		c := k.(*cnConn)
		go func() { _ = c.pushJob() }()
		return true
	})
}

// cnConn 每连接状态。
type cnConn struct {
	d      *CNDialect
	raw    net.Conn
	wmu    sync.Mutex
	port   config.PortConfig
	connID uint32
	sessID string
	vd     *vardiff.State

	mu        sync.Mutex // 保护登录态字段
	address   string
	worker    string
	userAgent string
	remoteIP  string
	loggedIn  bool
	ab        *autoBan
	guard     *connectionGuard

	seen sync.Map // jobid:nonce → 去重
}

func (d *CNDialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	port = config.WithPortDefaults(port)
	seq := d.connSeq.Add(1)
	remoteIP := verifiedClientIP(conn)

	// 连接 tag：handler 支持池化分配就用它（在线唯一、断开归还、满员拒连），
	// 否则回退纯自增。⚠ sessID 始终用自增序号——它是会话标识，复用会让归还后的
	// 新连接与旧会话撞 id；tag 可复用，sessID 不可。
	connID := uint32(seq)
	if alloc, ok := d.handler.(CNConnectionIDAllocator); ok {
		id, got := alloc.AcquireConnectionID()
		if !got {
			// 宁可拒连，也好过发出重复 tag 让两个矿工挖同一段 nonce
			// （那种情况池侧毫无异常，只有爆块率不涨，极难发现）。
			return fmt.Errorf("[%s] CN 连接 tag 空间已满，拒绝新连接 %s", d.coinID, remoteIP)
		}
		connID = id
		defer alloc.ReleaseConnectionID(id)
	}

	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	c := &cnConn{
		d:        d,
		raw:      conn,
		port:     port,
		connID:   connID, // 连接 tag（nonce 高位/分片字节的来源）
		sessID:   strconv.FormatUint(seq, 16),
		vd:       vardiff.New(vcfg, time.Now()),
		remoteIP: remoteIP,
		guard:    newConnectionGuard(port),
	}
	c.ab = newAutoBan(d.banner, conn, d.coinID, port)
	d.conns.Store(c, struct{}{})
	defer d.conns.Delete(c)

	lines := scanLines(ctx, conn, port.MessageMaxBytes)

	idle := time.NewTimer(4 * time.Minute) // XMRig keepalived 60s 一发，4 分钟没动静=半死
	defer idle.Stop()
	handshake := time.NewTimer(port.HandshakeTimeout())
	defer handshake.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return fmt.Errorf("[%s] CN 连接空闲超时 %s", d.coinID, c.remoteIP)
		case <-handshake.C:
			return fmt.Errorf("[%s] CN login 超时 verified_client_ip=%q", d.coinID, c.remoteIP)
		case frame, ok := <-lines:
			if !ok {
				return nil
			}
			if frame.err != nil {
				return fmt.Errorf("[%s] CN 消息读取失败: %w", d.coinID, frame.err)
			}
			line := frame.text
			idle.Reset(4 * time.Minute)
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := c.guard.observe(line, c.authed()); err != nil {
				return fmt.Errorf("[%s] CN 分级校验拒绝: %w", d.coinID, err)
			}
			var msg cnReq
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue // 非法 JSON 忽略（不断连）
			}
			if err := c.dispatch(ctx, &msg); err != nil {
				return err
			}
			if c.authed() {
				if !handshake.Stop() {
					select {
					case <-handshake.C:
					default:
					}
				}
			}
		}
	}
}

// cnReq CN 行 JSON 请求。
type cnReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type cnLoginParams struct {
	Login string   `json:"login"`
	Pass  string   `json:"pass"`
	Agent string   `json:"agent"`
	RigID string   `json:"rigid"`
	Algo  []string `json:"algo"`
}

type cnSubmitParams struct {
	ID     string `json:"id"` // 会话 id（login 响应发下去的）
	JobID  string `json:"job_id"`
	Nonce  string `json:"nonce"`
	Result string `json:"result"`
}

func (c *cnConn) dispatch(ctx context.Context, msg *cnReq) error {
	switch msg.Method {
	case "login":
		return c.onLogin(msg)
	case "submit":
		return c.onSubmit(ctx, msg)
	case "keepalived":
		return c.reply(msg.ID, map[string]any{"status": "KEEPALIVED"}, nil)
	case "getjob":
		if !c.authed() {
			return c.replyErr(msg.ID, "Unauthenticated")
		}
		if job, ok := c.d.handler.ConnJob(c.connID, c.vd.Current()); ok {
			return c.reply(msg.ID, job, nil)
		}
		return c.replyErr(msg.ID, "No job available")
	default:
		if len(msg.ID) > 0 {
			return c.replyErr(msg.ID, "Unsupported method")
		}
		return nil
	}
}

func (c *cnConn) onLogin(msg *cnReq) error {
	var p cnLoginParams
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.Login == "" {
		return c.replyErr(msg.ID, "Unauthenticated")
	}
	// algo 能力协商：矿工给了列表但不含本币算法 → 明确拒绝
	// （不拒的话矿工拿到不认识的 job.algo 会自行断线换池，报错更快定位）。
	ours := c.d.handler.Algo()
	if len(p.Algo) > 0 {
		found := false
		for _, a := range p.Algo {
			if strings.EqualFold(a, ours) {
				found = true
				break
			}
		}
		if !found {
			return c.replyErr(msg.ID, fmt.Sprintf("Unsupported algorithm %q (pool runs %s)", p.Algo[0], ours))
		}
	}

	address, worker, fixedDiff := parseCNLogin(p.Login, p.Pass, p.RigID)

	c.mu.Lock()
	c.address, c.worker, c.userAgent, c.loggedIn = address, worker, p.Agent, true
	c.mu.Unlock()

	// 密码参数（d=/mp=，与 V1 同款语义）；login 后缀难度优先级低于显式 d=
	if fixedDiff > 0 {
		c.vd.SetFixed(fixedDiff)
	}
	if p.Pass != "" {
		pp := minersettings.ParsePassword(p.Pass)
		if pp.FixedDiff > 0 {
			c.vd.SetFixed(pp.FixedDiff)
		}
		if c.d.onAuth != nil {
			c.d.onAuth(address, worker, pp)
		}
	}

	job, ok := c.d.handler.ConnJob(c.connID, c.vd.Current())
	if !ok {
		return c.replyErr(msg.ID, "No job available (pool starting)")
	}
	result := map[string]any{
		"id":         c.sessID,
		"job":        job,
		"status":     "OK",
		"extensions": c.d.handler.LoginExtensions(),
	}
	return c.reply(msg.ID, result, nil)
}

func (c *cnConn) onSubmit(ctx context.Context, msg *cnReq) error {
	if !c.authed() {
		if err := c.guard.unauthorized("submit"); err != nil {
			return err
		}
		return c.replyErr(msg.ID, "Unauthenticated")
	}
	var p cnSubmitParams
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.JobID == "" || p.Nonce == "" {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "Malformed submit")
	}
	if p.ID != "" && p.ID != c.sessID {
		return c.replyErr(msg.ID, "Unauthenticated")
	}
	dedup := p.JobID + ":" + strings.ToLower(p.Nonce)
	if _, dup := c.seen.LoadOrStore(dedup, struct{}{}); dup {
		return c.rejectSubmit(msg.ID, core.OutcomeDup, "Duplicate share")
	}

	c.mu.Lock()
	addr, worker, ua := c.address, c.worker, c.userAgent
	c.mu.Unlock()

	sub := CNSubmission{
		ConnID:    c.connID,
		Address:   addr,
		Worker:    worker,
		UserAgent: ua,
		RemoteIP:  c.remoteIP,
		JobID:     p.JobID,
		NonceHex:  p.Nonce,
		ResultHex: p.Result,
		Judge:     func(d float64) (float64, bool) { return c.vd.Judge(d, time.Now()) },
		Solo:      c.port.Mode == "solo",
	}
	release, ok := acquirePowSlot(ctx, c.d.coinID, c.port)
	if !ok {
		return c.replyErr(msg.ID, "Server busy (verification queue full)")
	}
	res := c.d.handler.HandleSubmit(ctx, sub)
	release()

	switch res.Outcome {
	case core.OutcomeAccepted, core.OutcomeBlock:
		metrics.ShareResult(c.d.coinID, res.Outcome)
		c.ab.record(res.Outcome)
		c.vd.OnAccepted(time.Now())
		if err := c.reply(msg.ID, map[string]any{"status": "OK"}, nil); err != nil {
			return err
		}
		c.maybeRetarget()
		return nil
	case core.OutcomeStale:
		return c.rejectSubmit(msg.ID, res.Outcome, "Invalid job id")
	case core.OutcomeLowDiff:
		return c.rejectSubmit(msg.ID, res.Outcome, "Low difficulty share")
	case core.OutcomeDup:
		return c.rejectSubmit(msg.ID, res.Outcome, "Duplicate share")
	case core.OutcomeBadPow:
		// 共识 tripwire：矿工声称 hash 与池端重算不符
		return c.rejectSubmit(msg.ID, res.Outcome, "Bad hash (recompute mismatch)")
	default:
		return c.rejectSubmit(msg.ID, res.Outcome, "Malformed submit")
	}
}

// rejectSubmit 拒绝应答 + 自动 ban 记账；触发 ban 时断开连接。
func (c *cnConn) rejectSubmit(id json.RawMessage, outcome core.ShareOutcome, message string) error {
	metrics.ShareResult(c.d.coinID, outcome)
	err := c.replyErr(id, message)
	if c.ab.record(outcome) {
		return fmt.Errorf("[%s] %s 自动 ban（恶意提交占比超阈值）", c.d.coinID, c.remoteIP)
	}
	return err
}

// maybeRetarget vardiff 调档。CN 没有 set_difficulty，难度随 job target 走——
// 但绝不对同一 blob 立即重推 job：矿工会重置 nonce 起点重找到同样的解 →
// Duplicate share（zoka live 冒烟实测）。新难度挂起，随下一个真新模板的 job
// 下发（pendingDifficulty 语义，docs/04 §1；blob 链模板 ≤15s 一换，等得起）。
func (c *cnConn) maybeRetarget() {
	if !c.port.Vardiff.Enabled {
		return
	}
	// vardiff 抬/降难度后【必须】把新 target 推给矿工，否则矿工仍在旧难度上挖，
	// 池按新（更高）Current 判定 → 旧难度 share 全被拒「Low difficulty share」。
	// （2026-07-13 noctari 实测：漏推新 job → 6.22MH/s 矿工 2/3 share 被拒。）
	if _, changed := c.vd.MaybeRetarget(time.Now()); changed {
		_ = c.pushJob()
	}
}

// pushJob 推送当前 job（登录后、新块广播、vardiff 调档时）。
func (c *cnConn) pushJob() error {
	if !c.authed() {
		return nil
	}
	job, ok := c.d.handler.ConnJob(c.connID, c.vd.Current())
	if !ok {
		return nil
	}
	return c.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "job",
		"params":  job,
	})
}

func (c *cnConn) authed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

// ---- 响应/底层 I/O ----

func (c *cnConn) reply(id json.RawMessage, result any, errObj any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(map[string]any{
		"id": id, "jsonrpc": "2.0", "result": result, "error": errObj,
	})
}

func (c *cnConn) replyErr(id json.RawMessage, message string) error {
	return c.reply(id, nil, map[string]any{"code": -1, "message": message})
}

func (c *cnConn) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err = c.raw.Write(append(b, '\n'))
	return err
}

// parseCNLogin 解析 login 字符串 + worker 识别顺序（docs/04 §2）。
// login = 地址[.难度][+worker]；worker：rigid > pass > +worker 后缀。
func parseCNLogin(login, pass, rigid string) (address, worker string, fixedDiff float64) {
	address = login
	// +worker 或 +难度 后缀
	if i := strings.IndexByte(address, '+'); i >= 0 {
		suffix := address[i+1:]
		address = address[:i]
		if d, err := strconv.ParseFloat(suffix, 64); err == nil && d > 0 {
			fixedDiff = d
		} else if suffix != "" {
			worker = suffix
		}
	}
	// .难度 后缀（仅数字才剥离——地址本体不含 '.'，但要容错别把 worker 当难度）
	if i := strings.LastIndexByte(address, '.'); i >= 0 {
		suffix := address[i+1:]
		if d, err := strconv.ParseFloat(suffix, 64); err == nil && d > 0 {
			fixedDiff = d
			address = address[:i]
		} else if suffix != "" && worker == "" {
			worker = suffix
			address = address[:i]
		}
	}
	// rigid 一等公民 > pass > +worker
	if rigid != "" {
		worker = rigid
	} else if w := workerFromPass(pass); w != "" {
		worker = w
	}
	if worker == "" {
		worker = "default"
	}
	return address, worker, fixedDiff
}

// workerFromPass 从 pass 提取 worker（剔除 "x"/空/参数串；worker:email 取 : 前）。
func workerFromPass(pass string) string {
	if pass == "" || pass == "x" {
		return ""
	}
	// 含 k=v 参数（d=8192,mp=21 之类）的密码不是 worker 名
	if strings.ContainsAny(pass, "=") {
		return ""
	}
	if i := strings.IndexByte(pass, ':'); i >= 0 {
		pass = pass[:i]
	}
	return pass
}
