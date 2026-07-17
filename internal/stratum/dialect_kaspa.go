// Kaspa 方言（EthereumStratum/1.0.0 系 subscribe/authorize/notify/submit）——Velkar(VELK) 用。
//
// 兼容清单（bridge default_client.rs / client_handler.rs / share_handler.rs 逐行坐实）：
//   - mining.subscribe → result [true,"EthereumStratum/1.0.0"]；params[0]=锄头 app 名（记 user-agent）
//   - mining.extranonce.subscribe → true
//   - mining.authorize params [address[.worker], pass] → OK；随后依次 set_extranonce/set_difficulty/notify
//   - mining.set_extranonce params [extranonceHex, extranonce2Size]（池按连接分配 nonce 高位分片）
//   - mining.set_difficulty params [difficulty]（小数，Diff1=2^224-1 口径）
//   - mining.notify params [jobId, prePowHash(64hex)+timestampLE(16hex)=80hex]
//   - mining.submit params [address[.worker], jobId, nonce]（3）或 [addr,jobId,en2,ntime,nonce]（5，lolMiner 系）
//     nonce=hex；短则池左补 extranonce 拼成 16hex u64（share_handler.rs extranonce 语义）
//
// 链差异（pre_pow_hash 派生 / block target 入哈希 / VelkarHash 重算 / 整块提交）全下沉到
// KaspaShareHandler（internal/velkarjob 实现）；本文件只做 wire 协议。连接层/vardiff/autoban 复用。
package stratum

import (
	"context"
	"encoding/binary"
	"encoding/hex"
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

// KaspaWireJob 共享 job（所有连接同一块模板；extranonce 由方言按连接分配）。
type KaspaWireJob struct {
	JobID      uint64
	PrePowHash []byte // 32B 内部序（方言转 64hex 下发）
	Timestamp  uint64 // 毫秒
	// BlockTargetLE 32B little-endian 的 block target（从 header.bits 解出）。
	// ★VelkarHash 独有：stage4 吃 block target，故矿工必须拿到它才能算对 pow——这是本方言
	// 相对标准 Kaspa stratum(只发 prePow+ts) 的必要扩展（标准 kHeavyHash 不需要，正是
	// VelkarHash 反通用锄头的根源：外部锄头不折 target → 算废块）。job 数据尾部多带 64hex。
	BlockTargetLE []byte
}

// KaspaSubmission 一条已解析的 Kaspa submit（nonce 已由方言拼好完整 u64）。
type KaspaSubmission struct {
	ConnID    uint32
	Address   string
	Worker    string
	UserAgent string
	RemoteIP  string
	JobID     uint64
	Nonce     uint64 // 完整 nonce（extranonce 高位 | 矿工低位）
	Judge     func(shareDiff float64) (creditDiff float64, ok bool)
	Solo      bool
}

// KaspaShareHandler 是 Kaspa 方言与作业管线的解耦点（internal/velkarjob 实现）。
type KaspaShareHandler interface {
	Algo() string
	// CurrentJob 返回当前共享 job；ok=false 表示模板未就绪。
	CurrentJob() (KaspaWireJob, bool)
	// HandleSubmit 校验一条提交（池端重算 VelkarHash + 难度判定 + 命中检测 + 整块提交 + 记账）。
	HandleSubmit(ctx context.Context, sub KaspaSubmission) SubmitResult
}

// extranonceBytes 每连接 extranonce 字节数（nonce 高位分片，2 字节=65536 分区，矿工滚低 6 字节）。
const extranonceBytes = 2

// KaspaDialect 实现 stratum.Dialect。每个币一个实例。
type KaspaDialect struct {
	coinID  string
	handler KaspaShareHandler
	connSeq atomic.Uint64
	conns   sync.Map // *kaspaConn → struct{}
	banner  Banner

	onAuth func(addr, worker string, p minersettings.PasswordParams)
}

func NewKaspaDialect(coinID string, h KaspaShareHandler) *KaspaDialect {
	return &KaspaDialect{coinID: coinID, handler: h}
}

func (d *KaspaDialect) SetAuthHook(h func(addr, worker string, p minersettings.PasswordParams)) {
	d.onAuth = h
}
func (d *KaspaDialect) SetAutoBan(b Banner) { d.banner = b }
func (d *KaspaDialect) Name() string        { return "kaspa" }

func (d *KaspaDialect) ConnCount() int {
	n := 0
	d.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastJob 新块到达时推最新 job 给所有已授权矿工。
func (d *KaspaDialect) BroadcastJob() {
	d.conns.Range(func(k, _ any) bool {
		c := k.(*kaspaConn)
		go func() { _ = c.pushJob() }()
		return true
	})
}

// kaspaConn 每连接状态。
type kaspaConn struct {
	d      *KaspaDialect
	raw    net.Conn
	wmu    sync.Mutex
	port   config.PortConfig
	connID uint32
	enHex  string // 本连接 extranonce（4 hex = 2 字节）
	vd     *vardiff.State

	mu           sync.Mutex
	address      string
	worker       string
	userAgent    string
	remoteIP     string
	loggedIn     bool
	didSubscribe bool
	ab           *autoBan
	guard        *connectionGuard

	seen sync.Map // jobid:nonce → 去重
}

func (d *KaspaDialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	port = config.WithPortDefaults(port)
	seq := d.connSeq.Add(1)
	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	c := &kaspaConn{
		d:        d,
		raw:      conn,
		port:     port,
		connID:   uint32(seq),
		enHex:    fmt.Sprintf("%04x", uint16(seq)), // extranonce = connID 低 16 位
		vd:       vardiff.New(vcfg, time.Now()),
		remoteIP: verifiedClientIP(conn),
		guard:    newConnectionGuard(port),
	}
	c.ab = newAutoBan(d.banner, conn, d.coinID, port)
	d.conns.Store(c, struct{}{})
	defer d.conns.Delete(c)

	lines := scanLines(ctx, conn, port.MessageMaxBytes)

	idle := time.NewTimer(4 * time.Minute)
	defer idle.Stop()
	handshake := time.NewTimer(port.HandshakeTimeout())
	defer handshake.Stop()
	authorizeDeadlineArmed := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return fmt.Errorf("[%s] kaspa 连接空闲超时 %s", d.coinID, c.remoteIP)
		case <-handshake.C:
			return fmt.Errorf("[%s] kaspa 握手/authorize 超时 verified_client_ip=%q", d.coinID, c.remoteIP)
		case frame, ok := <-lines:
			if !ok {
				return nil
			}
			if frame.err != nil {
				return fmt.Errorf("[%s] kaspa 消息读取失败: %w", d.coinID, frame.err)
			}
			line := frame.text
			idle.Reset(4 * time.Minute)
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := c.guard.observe(line, c.authed()); err != nil {
				return fmt.Errorf("[%s] kaspa 分级校验拒绝: %w", d.coinID, err)
			}
			var msg kaspaReq
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
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
			} else if c.subscribed() && !authorizeDeadlineArmed {
				resetTimer(handshake, port.AuthorizeTimeout())
				authorizeDeadlineArmed = true
			}
		}
	}
}

type kaspaReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (c *kaspaConn) dispatch(ctx context.Context, msg *kaspaReq) error {
	switch msg.Method {
	case "mining.subscribe":
		return c.onSubscribe(msg)
	case "mining.extranonce.subscribe":
		return c.reply(msg.ID, true, nil)
	case "mining.authorize":
		return c.onAuthorize(msg)
	case "mining.submit":
		return c.onSubmit(ctx, msg)
	default:
		if len(msg.ID) > 0 {
			return c.replyErr(msg.ID, "Unknown method")
		}
		return nil
	}
}

func (c *kaspaConn) onSubscribe(msg *kaspaReq) error {
	// params[0] = 锄头 app 名（user-agent）
	var params []json.RawMessage
	_ = json.Unmarshal(msg.Params, &params)
	if len(params) > 0 {
		var app string
		if json.Unmarshal(params[0], &app) == nil && app != "" {
			c.mu.Lock()
			c.userAgent = app
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.didSubscribe = true
	c.mu.Unlock()
	return c.reply(msg.ID, []any{true, "EthereumStratum/1.0.0"}, nil)
}

func (c *kaspaConn) onAuthorize(msg *kaspaReq) error {
	var params []json.RawMessage
	if err := json.Unmarshal(msg.Params, &params); err != nil || len(params) < 1 {
		return c.replyErr(msg.ID, "Unauthenticated")
	}
	var login, pass string
	_ = json.Unmarshal(params[0], &login)
	if len(params) >= 2 {
		_ = json.Unmarshal(params[1], &pass)
	}
	if login == "" {
		return c.replyErr(msg.ID, "Unauthenticated")
	}

	address, worker, fixedDiff := parseKaspaLogin(login, pass)
	c.mu.Lock()
	c.address, c.worker, c.loggedIn = address, worker, true
	c.mu.Unlock()

	if fixedDiff > 0 {
		c.vd.SetFixed(fixedDiff)
	}
	if pass != "" {
		pp := minersettings.ParsePassword(pass)
		if pp.FixedDiff > 0 {
			c.vd.SetFixed(pp.FixedDiff)
		}
		if c.d.onAuth != nil {
			c.d.onAuth(address, worker, pp)
		}
	}

	// 授权成功应答，随后 set_extranonce → set_difficulty → notify
	if err := c.reply(msg.ID, true, nil); err != nil {
		return err
	}
	if err := c.sendExtranonce(); err != nil {
		return err
	}
	return c.pushJob()
}

func (c *kaspaConn) onSubmit(ctx context.Context, msg *kaspaReq) error {
	if !c.authed() {
		if err := c.guard.unauthorized("submit"); err != nil {
			return err
		}
		return c.replyErr(msg.ID, "Unauthenticated")
	}
	var params []json.RawMessage
	if err := json.Unmarshal(msg.Params, &params); err != nil || len(params) < 3 {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "Malformed submit")
	}
	// jobId = params[1]（字符串或数字）；nonce = 3 参时 params[2]，5+ 参时 params[4]
	jobID, err := parseJobID(params[1])
	if err != nil {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "Bad job id")
	}
	nonceIdx := 2
	if len(params) >= 5 {
		nonceIdx = 4
	}
	var nonceHex string
	_ = json.Unmarshal(params[nonceIdx], &nonceHex)
	if nonceHex == "" {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "Bad nonce")
	}
	nonce, err := assembleNonce(nonceHex, c.enHex)
	if err != nil {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "Bad nonce")
	}

	dedup := strconv.FormatUint(jobID, 16) + ":" + strconv.FormatUint(nonce, 16)
	if _, dup := c.seen.LoadOrStore(dedup, struct{}{}); dup {
		return c.rejectSubmit(msg.ID, core.OutcomeDup, "Duplicate share")
	}

	c.mu.Lock()
	addr, worker, ua := c.address, c.worker, c.userAgent
	c.mu.Unlock()

	sub := KaspaSubmission{
		ConnID:    c.connID,
		Address:   addr,
		Worker:    worker,
		UserAgent: ua,
		RemoteIP:  c.remoteIP,
		JobID:     jobID,
		Nonce:     nonce,
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
		if err := c.reply(msg.ID, true, nil); err != nil {
			return err
		}
		c.maybeRetarget()
		return nil
	case core.OutcomeStale:
		return c.rejectSubmit(msg.ID, res.Outcome, "Job not found")
	case core.OutcomeLowDiff:
		return c.rejectSubmit(msg.ID, res.Outcome, "Low difficulty share")
	case core.OutcomeDup:
		return c.rejectSubmit(msg.ID, res.Outcome, "Duplicate share")
	default:
		return c.rejectSubmit(msg.ID, res.Outcome, "Malformed submit")
	}
}

func (c *kaspaConn) rejectSubmit(id json.RawMessage, outcome core.ShareOutcome, message string) error {
	metrics.ShareResult(c.d.coinID, outcome)
	err := c.reply(id, nil, map[string]any{"code": -1, "message": message})
	if c.ab.record(outcome) {
		return fmt.Errorf("[%s] %s 自动 ban（恶意提交占比超阈值）", c.d.coinID, c.remoteIP)
	}
	return err
}

// maybeRetarget vardiff 调档：抬/降后必须推新 difficulty + job，否则矿工旧难度 share 被拒。
func (c *kaspaConn) maybeRetarget() {
	if !c.port.Vardiff.Enabled {
		return
	}
	if _, changed := c.vd.MaybeRetarget(time.Now()); changed {
		_ = c.pushJob()
	}
}

// sendExtranonce 下发本连接 extranonce（授权后一次）。
func (c *kaspaConn) sendExtranonce() error {
	en2Size := 8 - len(c.enHex)/2
	return c.notify("mining.set_extranonce", []any{c.enHex, en2Size})
}

// pushJob 推送 set_difficulty + mining.notify（授权后、新块广播、vardiff 调档）。
func (c *kaspaConn) pushJob() error {
	if !c.authed() {
		return nil
	}
	job, ok := c.d.handler.CurrentJob()
	if !ok {
		return nil
	}
	// set_difficulty 用不带指数的十进制（小数难度，防锄头不认 1e-05）
	diffRaw := json.RawMessage(strconv.FormatFloat(c.vd.Current(), 'f', -1, 64))
	if err := c.notify("mining.set_difficulty", []any{diffRaw}); err != nil {
		return err
	}
	// job 数据 = prePowHash(64hex) + timestampLE(16hex) + blockTargetLE(64hex) = 144hex
	// （末段 blockTargetLE 是 VelkarHash stratum 扩展，见 KaspaWireJob.BlockTargetLE 注释）。
	var tsLE [8]byte
	binary.LittleEndian.PutUint64(tsLE[:], job.Timestamp)
	jobData := hex.EncodeToString(job.PrePowHash) + hex.EncodeToString(tsLE[:]) + hex.EncodeToString(job.BlockTargetLE)
	return c.notify("mining.notify", []any{strconv.FormatUint(job.JobID, 10), jobData})
}

func (c *kaspaConn) authed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

func (c *kaspaConn) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.didSubscribe
}

// ---- 响应/底层 I/O ----

func (c *kaspaConn) reply(id json.RawMessage, result any, errObj any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(map[string]any{"id": id, "result": result, "error": errObj})
}

func (c *kaspaConn) replyErr(id json.RawMessage, message string) error {
	return c.reply(id, nil, map[string]any{"code": -1, "message": message})
}

func (c *kaspaConn) notify(method string, params []any) error {
	return c.writeJSON(map[string]any{"id": nil, "method": method, "params": params})
}

func (c *kaspaConn) writeJSON(v any) error {
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

// ---- 解析工具 ----

// parseKaspaLogin login = 地址[.worker][+难度] / 地址.worker；worker：pass > +worker > .worker 后缀。
func parseKaspaLogin(login, pass string) (address, worker string, fixedDiff float64) {
	address = login
	if i := strings.IndexByte(address, '+'); i >= 0 {
		suffix := address[i+1:]
		address = address[:i]
		if d, err := strconv.ParseFloat(suffix, 64); err == nil && d > 0 {
			fixedDiff = d
		} else if suffix != "" {
			worker = suffix
		}
	}
	// 地址.worker（Kaspa 地址本体不含 '.'）
	if i := strings.IndexByte(address, '.'); i >= 0 {
		if worker == "" {
			worker = address[i+1:]
		}
		address = address[:i]
	}
	if w := workerFromPass(pass); w != "" {
		worker = w
	}
	if worker == "" {
		worker = "default"
	}
	return address, worker, fixedDiff
}

// parseJobID jobId 可为字符串或数字。
func parseJobID(raw json.RawMessage) (uint64, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return strconv.ParseUint(s, 10, 64)
	}
	var n uint64
	if json.Unmarshal(raw, &n) == nil {
		return n, nil
	}
	return 0, fmt.Errorf("job id 非字符串/数字")
}

// assembleNonce 拼完整 nonce：矿工回短串则左补 extranonce（高位）+ 零填充（share_handler.rs 语义）；
// 回完整 16hex 则取低 64 位。返回大端 hex 解出的 u64。
func assembleNonce(nonceHex, enHex string) (uint64, error) {
	nonceHex = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(nonceHex)), "0x")
	if nonceHex == "" {
		return 0, fmt.Errorf("空 nonce")
	}
	en2Len := 16 - len(enHex) // 矿工搜索区 hex 长度
	var full string
	switch {
	case len(nonceHex) <= en2Len:
		pad := en2Len - len(nonceHex)
		full = enHex + strings.Repeat("0", pad) + nonceHex
	case len(nonceHex) >= 16:
		full = nonceHex[len(nonceHex)-16:] // 完整 nonce：取低 16hex
	default:
		full = strings.Repeat("0", 16-len(nonceHex)) + nonceHex
	}
	return strconv.ParseUint(full, 16, 64)
}
