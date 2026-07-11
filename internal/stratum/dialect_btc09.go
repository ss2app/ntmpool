// Bitcoin 09 (09C) 方言：裸 TCP + 换行 JSON（BNT 线路，非标准 stratum V1，
// 非 XMRig CN）。wire 协议逐字段复刻老池 coins/bitcoin09/pool/stratum.go——
// NTMminer -a btc09 现役发行版不改一个字节就能连。
//
// 协议（老池即事实标准）：
//   - login/mining.subscribe/subscribe：params {address|login, worker, pass}；
//     地址可带 ".worker" 后缀；成功 → {"result":{"status":"ok"},"error":null}，
//     随后单独一行推 job。
//   - job 推送（server→client，无 id）：{"method":"job","params":{job_id, height,
//     header_base, header(=alias), target(64hex BE share目标), network_target,
//     difficulty, nonce_start, nonce_end}}。
//   - submit/mining.submit：params {job_id, nonce:<uint64 数字>, hash:<可选 hex>}；
//     应答 result.status = accepted|block，或 error 字符串（注意是字符串不是对象）：
//     "stale job" / "low difficulty share" / "duplicate share" / "bad share"。
//   - ping/keepalived → {"result":{"status":"ok"},"error":null}。
//   - 未知方法 → {"result":null,"error":"unknown method"}（不断连）。
//
// 链差异（难度标尺、nonce 窗口、Argon2id 重算、组块）全部下沉到
// Btc09ShareHandler（internal/btc09job 实现）；本文件只做 wire 协议。
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

// Btc09WireJob 下发矿工的 job 全部字段（handler 物化好，dialect 原样上线）。
type Btc09WireJob struct {
	JobID         string  `json:"job_id"`
	Height        uint64  `json:"height"`
	HeaderBase    string  `json:"header_base"` // 88B header hex；nonce 写 [80:88]
	Header        string  `json:"header"`      // alias（老池双字段下发）
	Target        string  `json:"target"`      // share 目标，64-hex 大端
	NetworkTarget string  `json:"network_target"`
	Difficulty    float64 `json:"difficulty"`
	NonceStart    uint64  `json:"nonce_start"`
	NonceEnd      uint64  `json:"nonce_end"`
}

// Btc09Submission 一条已解析的 09C submit。
type Btc09Submission struct {
	ConnID    uint32
	Address   string
	Worker    string
	UserAgent string
	RemoteIP  string
	JobID     string
	Nonce     uint64
	HashHex   string // 矿工声称的 PoW hash（可选；带了就做 badpow tripwire）
	Judge     func(shareDiff float64) (creditDiff float64, ok bool)
	Solo      bool
}

// Btc09ShareHandler 是 09C 方言与作业管线的解耦点（internal/btc09job 实现）。
type Btc09ShareHandler interface {
	// ValidateAddress 登录地址门禁（base58check v0x09；老池同款，拒绝挖空）。
	ValidateAddress(addr string) error
	// ConnJob 为一条连接物化当前 job（share target 按难度编码 + 私有 nonce 窗口）。
	ConnJob(connID uint32, difficulty float64) (Btc09WireJob, bool)
	// HandleSubmit 校验一条提交（池端重算 + 命中检测 + 组块提交 + 记账）。
	HandleSubmit(ctx context.Context, sub Btc09Submission) SubmitResult
}

// Btc09Dialect 实现 stratum.Dialect。
type Btc09Dialect struct {
	coinID  string
	handler Btc09ShareHandler
	connSeq atomic.Uint64
	conns   sync.Map // *btc09Conn → struct{}
	banner  Banner

	onAuth func(addr, worker string, p minersettings.PasswordParams)
}

func NewBtc09Dialect(coinID string, h Btc09ShareHandler) *Btc09Dialect {
	return &Btc09Dialect{coinID: coinID, handler: h}
}

func (d *Btc09Dialect) SetAuthHook(h func(addr, worker string, p minersettings.PasswordParams)) {
	d.onAuth = h
}

func (d *Btc09Dialect) SetAutoBan(b Banner) { d.banner = b }

func (d *Btc09Dialect) Name() string { return "btc09" }

func (d *Btc09Dialect) ConnCount() int {
	n := 0
	d.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastJob 新模板到达时推最新 job 给所有已登录矿工。
func (d *Btc09Dialect) BroadcastJob() {
	d.conns.Range(func(k, _ any) bool {
		c := k.(*btc09Conn)
		go func() { _ = c.pushJob() }()
		return true
	})
}

type btc09Conn struct {
	d      *Btc09Dialect
	raw    net.Conn
	wmu    sync.Mutex
	port   config.PortConfig
	connID uint32
	vd     *vardiff.State

	mu       sync.Mutex
	address  string
	worker   string
	loggedIn bool
	ab       *autoBan
	remoteIP string

	seen sync.Map // jobid:nonce → 去重
}

func (d *Btc09Dialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	seq := d.connSeq.Add(1)
	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	c := &btc09Conn{
		d:        d,
		raw:      conn,
		port:     port,
		connID:   uint32(seq), // nonce 窗口高位（2^20 连接内不重叠）
		vd:       vardiff.New(vcfg, time.Now()),
		remoteIP: remoteHost(conn),
	}
	c.ab = &autoBan{banner: d.banner, ip: c.remoteIP}
	d.conns.Store(c, struct{}{})
	defer d.conns.Delete(c)

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := newLineScanner(conn)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	idle := time.NewTimer(10 * time.Minute) // 老池读 deadline 同款
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return fmt.Errorf("[%s] 09C 连接空闲超时 %s", d.coinID, c.remoteIP)
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			idle.Reset(10 * time.Minute)
			if strings.TrimSpace(line) == "" {
				continue
			}
			var msg btc09Req
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue // 非法 JSON 忽略（不断连，老池同款容忍度）
			}
			if err := c.dispatch(ctx, &msg); err != nil {
				return err
			}
		}
	}
}

type btc09Req struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type btc09LoginParams struct {
	Address string `json:"address"`
	Login   string `json:"login"` // xmrig 风格别名（老池两个都收）
	Worker  string `json:"worker"`
	Pass    string `json:"pass"`
}

type btc09SubmitParams struct {
	JobID string `json:"job_id"`
	Nonce uint64 `json:"nonce"`
	Hash  string `json:"hash"`
}

func (c *btc09Conn) dispatch(ctx context.Context, msg *btc09Req) error {
	switch msg.Method {
	case "login", "mining.subscribe", "subscribe":
		return c.onLogin(msg)
	case "submit", "mining.submit":
		return c.onSubmit(ctx, msg)
	case "ping", "keepalived":
		return c.reply(msg.ID, map[string]any{"status": "ok"}, nil)
	default:
		return c.replyErr(msg.ID, "unknown method")
	}
}

func (c *btc09Conn) onLogin(msg *btc09Req) error {
	var p btc09LoginParams
	_ = json.Unmarshal(msg.Params, &p)
	addr := p.Address
	if addr == "" {
		addr = p.Login
	}
	// 地址可带 ".worker" 后缀（老池同款）
	if i := strings.IndexByte(addr, '.'); i >= 0 {
		if p.Worker == "" {
			p.Worker = addr[i+1:]
		}
		addr = addr[:i]
	}
	if err := c.d.handler.ValidateAddress(addr); err != nil {
		_ = c.replyErr(msg.ID, "invalid 09C address")
		return fmt.Errorf("[%s] 非法登录地址 %q: %w", c.d.coinID, addr, err)
	}
	worker := p.Worker
	if worker == "" {
		worker = "default"
	}

	c.mu.Lock()
	c.address, c.worker, c.loggedIn = addr, worker, true
	c.mu.Unlock()

	// 密码参数（d= 固定难度 / mp= 等，与 V1/CN 同款语义）
	if p.Pass != "" {
		pp := minersettings.ParsePassword(p.Pass)
		if pp.FixedDiff > 0 {
			c.vd.SetFixed(pp.FixedDiff)
		}
		if c.d.onAuth != nil {
			c.d.onAuth(addr, worker, pp)
		}
	}

	if err := c.reply(msg.ID, map[string]any{"status": "ok"}, nil); err != nil {
		return err
	}
	return c.pushJob()
}

func (c *btc09Conn) onSubmit(ctx context.Context, msg *btc09Req) error {
	if !c.authed() {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "bad share")
	}
	var p btc09SubmitParams
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.JobID == "" {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "bad share")
	}
	dedup := p.JobID + ":" + strconv.FormatUint(p.Nonce, 10)
	if _, dup := c.seen.LoadOrStore(dedup, struct{}{}); dup {
		return c.rejectSubmit(msg.ID, core.OutcomeDup, "duplicate share")
	}

	c.mu.Lock()
	addr, worker := c.address, c.worker
	c.mu.Unlock()

	sub := Btc09Submission{
		ConnID:   c.connID,
		Address:  addr,
		Worker:   worker,
		RemoteIP: c.remoteIP,
		JobID:    p.JobID,
		Nonce:    p.Nonce,
		HashHex:  p.Hash,
		Judge:    func(d float64) (float64, bool) { return c.vd.Judge(d, time.Now()) },
		Solo:     c.port.Mode == "solo",
	}
	res := c.d.handler.HandleSubmit(ctx, sub)

	switch res.Outcome {
	case core.OutcomeAccepted, core.OutcomeBlock:
		metrics.ShareResult(c.d.coinID, res.Outcome)
		c.ab.record(res.Outcome)
		c.vd.OnAccepted(time.Now())
		status := "accepted"
		if res.Outcome == core.OutcomeBlock {
			status = "block"
		}
		if err := c.reply(msg.ID, map[string]any{"status": status}, nil); err != nil {
			return err
		}
		c.maybeRetarget()
		return nil
	case core.OutcomeStale:
		return c.rejectSubmit(msg.ID, res.Outcome, "stale job")
	case core.OutcomeLowDiff:
		return c.rejectSubmit(msg.ID, res.Outcome, "low difficulty share")
	case core.OutcomeDup:
		return c.rejectSubmit(msg.ID, res.Outcome, "duplicate share")
	case core.OutcomeBadPow:
		// 共识 tripwire：矿工声称 hash 与池端 Argon2id 重算不符
		return c.rejectSubmit(msg.ID, res.Outcome, "bad share")
	default:
		return c.rejectSubmit(msg.ID, res.Outcome, "bad share")
	}
}

func (c *btc09Conn) rejectSubmit(id json.RawMessage, outcome core.ShareOutcome, message string) error {
	metrics.ShareResult(c.d.coinID, outcome)
	err := c.replyErr(id, message)
	if c.ab.record(outcome) {
		return fmt.Errorf("[%s] %s 自动 ban（恶意提交占比超阈值）", c.d.coinID, c.remoteIP)
	}
	return err
}

// maybeRetarget vardiff 调档。不对同一 job 立即重推（矿工会从 nonce_start 重找
// 同解 → duplicate share）；新难度挂起，随下一个新模板的 job 下发
// （pendingDifficulty 语义，与 CN 方言一致；poolnode 模板 ≤45s 一换，等得起）。
func (c *btc09Conn) maybeRetarget() {
	if !c.port.Vardiff.Enabled {
		return
	}
	_, _ = c.vd.MaybeRetarget(time.Now())
}

// pushJob 推送当前 job（登录后、新模板广播时）。老池格式：无 id、无 jsonrpc 字段。
func (c *btc09Conn) pushJob() error {
	if !c.authed() {
		return nil
	}
	job, ok := c.d.handler.ConnJob(c.connID, c.vd.Current())
	if !ok {
		return nil
	}
	return c.writeJSON(map[string]any{
		"method": "job",
		"params": job,
	})
}

func (c *btc09Conn) authed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

// ---- 响应/底层 I/O（老池格式：error 是字符串或 null，不是对象）----

func (c *btc09Conn) reply(id json.RawMessage, result any, errStr any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(map[string]any{"id": id, "result": result, "error": errStr})
}

func (c *btc09Conn) replyErr(id json.RawMessage, message string) error {
	return c.reply(id, nil, message)
}

func (c *btc09Conn) writeJSON(v any) error {
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
