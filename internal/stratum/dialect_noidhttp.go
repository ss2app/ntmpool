// NOID 矿工面（ParanO(1)d）——HTTP JSON-RPC 2.0（jsonrpsee 协议），实现 stratum.Dialect。
//
// 为什么是 HTTP 而不是 TCP stratum：NOID 官方矿工（parano1d-miner）与我们未来的
// NTMminer-noid 都直接对节点发 paranoid_getBlockTemplate / paranoid_submitBlock（可选
// Bearer），零改动即可连池。本方言在【单条已建立的 conn】上用 http.ReadRequest 逐请求
// 解析 HTTP/1.1（keep-alive），从而 100% 白拿 server.go 的全部准入护栏（端口热管理 /
// acceptNew / ban 预检 / PROXY v1+v2 / 每 IP 上限 / 坍缩降级）——Serve 收到的 conn 已是
// 过完这些闸门的 governedConn。
//
// ★协议三坑（施工图 §8 + REPORT 铁律 4/5）：
//  1. 官方 miner 在 submitBlock【成功】后同高度不再挖（extminer last_height 语义）⇒ 池对
//     「普通有效 share」回 JSON-RPC 错误（非 stale），矿工才会继续同高度挖。这条错误回复
//     ★绝不能进 autoban 的 violent 计数（它记为 core.OutcomeAccepted，autoban 只把
//     BadPow/Malformed/Dup 计 violent）——juno 2780962 老锄头被误判 autoban 的同款坑。
//  2. 池对每 worker 下发自己的 difficulty_target_hex（宽松 share target，vardiff 独立），
//     官方矿工照单全收；全网 target 只在池侧判爆块。
//  3. share 池侧先重算（noidp2b）+ 按 (template_id,nonce) 全池去重，达全网 target 才 submit。
//
// 会话状态（vardiff/tag）按【矿工身份】索引（token 或 address.worker），因 HTTP 短连接
// 语义 + keep-alive 混用；autoban 仍按连接（一条 keep-alive conn ≈ 一台矿机）。
package stratum

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/metrics"
	"github.com/scashcc/ntmpool/internal/minersettings"
	"github.com/scashcc/ntmpool/internal/vardiff"
)

// NoidTemplateWire 是 getBlockTemplate 回给矿工的模板字段（difficulty_target_hex 由
// dialect 按 worker vardiff 单独填，不在此结构）。
type NoidTemplateWire struct {
	TemplateID       string
	PowFieldsHex     string // 512 hex；worker tag 已写入 field 10 高 64 位（自研锄头用）
	NonceFieldIndex  int    // 恒 10
	Height           uint64
	ExpiresInSeconds int64
	NTxs             int
}

// NoidSubmission 一条已解析的 submitBlock（handler 池端重算的输入）。
type NoidSubmission struct {
	Address     string
	Worker      string
	UserAgent   string
	RemoteIP    string
	TemplateID  string
	NonceLE     []byte // 16B LE u128
	ShareTarget []byte // 32B LE：该 worker 的宽松 share target（dialect 从 vardiff 算）
	// Judge vardiff 一步 grace 归属：输入 share 实际难度，返回 (计权难度, 是否达标)。
	Judge func(shareDiff float64) (creditDiff float64, ok bool)
	Solo  bool
}

// NoidSubmitResult handler 池端重算结果。
//   - Outcome=OutcomeAccepted：普通有效 share（dialect 回 JSON-RPC 错误让矿工继续挖）。
//   - Outcome=OutcomeBlock：爆块（dialect 回成功 + BlockHash）。
//   - OutcomeLowDiff/Dup/Stale/Malformed：对应错误。
type NoidSubmitResult struct {
	Outcome    core.ShareOutcome
	CreditDiff float64
	BlockHash  string // hex；仅 OutcomeBlock 有值
}

// NoidShareHandler NOID 方言与作业管线（internal/noidjob）的解耦点。
type NoidShareHandler interface {
	// Algo 本币算法名。
	Algo() string
	// Template 当前 job 的 wire 字段（worker tag 写进 nonce 高位）。ok=false = 无模板。
	Template(workerTag uint64) (NoidTemplateWire, bool)
	// HandleSubmit 池端重算校验（重算 → 去重 → share/block 分类 → 达全网 target 才 submit）。
	HandleSubmit(ctx context.Context, sub NoidSubmission) NoidSubmitResult
}

// NoidDialect 实现 stratum.Dialect。每币一个实例。
type NoidDialect struct {
	coinID  string
	handler NoidShareHandler
	conns   sync.Map // *noidConn → struct{}
	banner  Banner   // 协议层自动 ban（可选）
	onAuth  func(addr, worker string, p minersettings.PasswordParams)

	tagSeq   atomic.Uint64 // worker tag 分配器（u128 高 64 位，撞车概率可忽略）
	sessMu   sync.Mutex
	sessions map[string]*noidSession // port|identity → 会话（vardiff/tag，跨短连接持久）
}

// noidSession 一个矿工身份的持久会话状态（vardiff + nonce tag），跨 HTTP 短连接复用。
type noidSession struct {
	vd       *vardiff.State
	tag      uint64
	lastSeen time.Time
}

const (
	// noidSessionTTL 会话闲置多久淘汰（HTTP 短连接：矿工消失后回收 vardiff 状态）。
	noidSessionTTL = 30 * time.Minute
	// noidSessionMax 会话表软上限，超了触发惰性淘汰。
	noidSessionMax = 20000
	// noidIdleTimeout 单连接读空闲超时（keep-alive 下矿工会周期 poll，超时判半死）。
	noidIdleTimeout = 5 * time.Minute
)

// NewNoidDialect 建 NOID HTTP 矿工面方言。
func NewNoidDialect(coinID string, h NoidShareHandler) *NoidDialect {
	return &NoidDialect{coinID: coinID, handler: h, sessions: map[string]*noidSession{}}
}

// SetAuthHook / SetAutoBan 供 coininstance 接线（authHookable/autoBannable）。
func (d *NoidDialect) SetAuthHook(h func(addr, worker string, p minersettings.PasswordParams)) {
	d.onAuth = h
}
func (d *NoidDialect) SetAutoBan(b Banner) { d.banner = b }

func (d *NoidDialect) Name() string { return "noidhttp" }

// ConnCount 当前在连矿工数。
func (d *NoidDialect) ConnCount() int {
	n := 0
	d.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastJob 新模板到达时的广播钩子。HTTP 无 server→client 推送通道，矿工靠 poll
// getBlockTemplate 取新模板（官方 extminer ~500ms 一 poll）——故本方法为 no-op。
func (d *NoidDialect) BroadcastJob() {}

// session 取/建矿工身份的持久会话（惰性 TTL 淘汰）。
func (d *NoidDialect) session(key string, port config.PortConfig, fixedDiff float64) *noidSession {
	d.sessMu.Lock()
	defer d.sessMu.Unlock()
	now := time.Now()
	if s, ok := d.sessions[key]; ok {
		s.lastSeen = now
		return s
	}
	if len(d.sessions) >= noidSessionMax {
		for k, s := range d.sessions {
			if now.Sub(s.lastSeen) > noidSessionTTL {
				delete(d.sessions, k)
			}
		}
	}
	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	s := &noidSession{
		vd:       vardiff.New(vcfg, now),
		tag:      d.tagSeq.Add(1),
		lastSeen: now,
	}
	if fixedDiff > 0 {
		s.vd.SetFixed(fixedDiff)
	}
	d.sessions[key] = s
	return s
}

// noidConn 单连接状态。
type noidConn struct {
	d        *NoidDialect
	raw      net.Conn
	port     config.PortConfig
	remoteIP string
	ab       *autoBan
}

// Serve 在单条 conn 上跑 HTTP/1.1 keep-alive JSON-RPC 循环。
func (d *NoidDialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	port = config.WithPortDefaults(port)
	c := &noidConn{
		d:        d,
		raw:      conn,
		port:     port,
		remoteIP: verifiedClientIP(conn),
		ab:       newAutoBan(d.banner, conn, d.coinID, port),
	}
	d.conns.Store(c, struct{}{})
	defer d.conns.Delete(c)

	// ctx 取消 → 把读 deadline 拨到过去，解开阻塞的 ReadRequest。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()

	br := bufio.NewReaderSize(conn, 8192)
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(noidIdleTimeout))
		req, err := readHTTPRequest(br)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("[%s] noid HTTP 读取: %w", d.coinID, err)
		}
		keepAlive, closeErr := c.handleRequest(ctx, req)
		if closeErr != nil {
			return closeErr
		}
		if !keepAlive {
			return nil
		}
	}
}

// httpRequest 一条已读取的 HTTP 请求（只保留分发所需字段）。
type httpRequest struct {
	method    string
	authz     string
	body      []byte
	keepAlive bool
}

// readHTTPRequest 用 http.ReadRequest 解析一条 HTTP/1.1 请求 + 读满 body。
// 复用 stdlib 的健壮解析（请求行/头/Content-Length/chunked），我们只手写响应。
// ⚠必须读满并关闭 body，否则下一条请求的解析会错位。
func readHTTPRequest(br *bufio.Reader) (*httpRequest, error) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, err
	}
	defer req.Body.Close()
	body, err := io.ReadAll(io.LimitReader(req.Body, int64(config.HardMessageMaxBytes)))
	if err != nil {
		return nil, err
	}
	return &httpRequest{
		method:    req.Method,
		authz:     req.Header.Get("Authorization"),
		body:      body,
		keepAlive: !req.Close, // http.ReadRequest 已按协议版本 + Connection 头定 Close
	}, nil
}

// handleRequest 分发一条 JSON-RPC over HTTP 请求。返回 (是否 keep-alive, 需断开的错误)。
func (c *noidConn) handleRequest(ctx context.Context, req *httpRequest) (bool, error) {
	// 攻击面护栏：JSON 嵌套深度（body 已受 HardMessageMaxBytes 限长）。
	if err := validateJSONDepth(req.body, c.port.JSONMaxDepth); err != nil {
		_ = c.writeResult(nil, nil, rpcErr(-32600, "invalid request"))
		return false, fmt.Errorf("[%s] noid JSON 深度超限: %w", c.d.coinID, err)
	}

	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(req.body, &rpc); err != nil || rpc.Method == "" {
		_ = c.writeResult(req, nil, rpcErr(-32700, "parse error"))
		return req.keepAlive, nil
	}

	token := bearerToken(req.authz)
	address, worker, fixedDiff := parseCNLogin(token, "", "")
	if token == "" {
		address, worker = c.remoteIP, "default" // 无 token 兜底：用真实 IP 当身份
	}
	sessKey := fmt.Sprintf("%d|%s", c.port.Port, token)
	if token == "" {
		sessKey = fmt.Sprintf("%d|ip:%s", c.port.Port, c.remoteIP)
	}
	sess := c.d.session(sessKey, c.port, fixedDiff)

	switch rpc.Method {
	case "paranoid_getBlockTemplate":
		return c.onGetTemplate(rpc.ID, req, sess)
	case "paranoid_submitBlock":
		return c.onSubmit(ctx, rpc.ID, rpc.Params, req, sess, address, worker)
	case "paranoid_getChainInfo", "paranoid_blockCount":
		// 有的矿工会探链高——不是池的职责，礼貌拒绝（不断连）。
		return req.keepAlive, c.writeResult(req, nil, rpcErr(-32601, "method not served by pool (mine via getBlockTemplate/submitBlock)"))
	default:
		return req.keepAlive, c.writeResult(req, nil, rpcErr(-32601, "method not found"))
	}
}

// onGetTemplate 下发当前模板，difficulty_target_hex = 该 worker 的宽松 share target。
func (c *noidConn) onGetTemplate(id json.RawMessage, req *httpRequest, sess *noidSession) (bool, error) {
	tw, ok := c.d.handler.Template(sess.tag)
	if !ok {
		// 无模板（暖机 / 单飞行槽制备中）：回错误让矿工稍后重取（含 stale 触发重取）。
		return req.keepAlive, c.writeResultID(id, req.keepAlive, nil, rpcErr(-32000, "pool warming up (no template yet), retry"))
	}
	shareTarget := targetLE32(sess.vd.Current())
	result := map[string]any{
		"template_id":           tw.TemplateID,
		"pow_fields_hex":        tw.PowFieldsHex,
		"nonce_field_index":     tw.NonceFieldIndex,
		"difficulty_target_hex": hex.EncodeToString(shareTarget[:]),
		"height":                tw.Height,
		"expires_in_seconds":    tw.ExpiresInSeconds,
		"n_txs":                 tw.NTxs,
	}
	return req.keepAlive, c.writeResultID(id, req.keepAlive, result, nil)
}

// onSubmit 校验一条 submitBlock。响应语义见文件头 §协议三坑 1。
func (c *noidConn) onSubmit(ctx context.Context, id json.RawMessage, params json.RawMessage, req *httpRequest, sess *noidSession, address, worker string) (bool, error) {
	tid, nonceHex, ok := parseNoidSubmitParams(params)
	if !ok {
		return c.rejectSubmit(id, req, core.OutcomeMalformed, "invalid params (expect [template_id, nonce_hex])")
	}
	nonceLE, err := hex.DecodeString(strings.TrimSpace(nonceHex))
	if err != nil || len(nonceLE) != 16 {
		return c.rejectSubmit(id, req, core.OutcomeMalformed, "nonce must be 16-byte (32 hex) LE")
	}

	shareTarget := targetLE32(sess.vd.Current())
	now := time.Now()
	sub := NoidSubmission{
		Address:     address,
		Worker:      worker,
		RemoteIP:    c.remoteIP,
		TemplateID:  tid,
		NonceLE:     nonceLE,
		ShareTarget: shareTarget[:],
		Judge:       func(diff float64) (float64, bool) { return sess.vd.Judge(diff, now) },
		Solo:        c.port.Mode == "solo",
	}

	release, ok := acquirePowSlot(ctx, c.d.coinID, c.port)
	if !ok {
		return req.keepAlive, c.writeResultID(id, req.keepAlive, nil, rpcErr(-32000, "server busy (verification queue full)"))
	}
	res := c.d.handler.HandleSubmit(ctx, sub)
	release()

	switch res.Outcome {
	case core.OutcomeBlock:
		metrics.ShareResult(c.d.coinID, core.OutcomeBlock)
		c.ab.record(core.OutcomeBlock) // 非 violent
		sess.vd.OnAccepted(now)
		sess.vd.MaybeRetarget(now) // 新难度随矿工下次 poll getBlockTemplate 生效
		// ★爆块 = 成功：官方 miner 见成功后同高度不再挖（正确，块已被池提交节点）。
		return req.keepAlive, c.writeResultID(id, req.keepAlive, res.BlockHash, nil)

	case core.OutcomeAccepted:
		// ★普通有效 share：回 JSON-RPC 错误（非 stale）让官方 miner 继续同高度挖。
		// ★记 core.OutcomeAccepted → autoban 不计 violent（关键隔离）。
		metrics.ShareResult(c.d.coinID, core.OutcomeAccepted)
		c.ab.record(core.OutcomeAccepted) // 非 violent
		sess.vd.OnAccepted(now)
		sess.vd.MaybeRetarget(now)
		return req.keepAlive, c.writeResultID(id, req.keepAlive, nil,
			rpcErr(-32001, "share accepted, below network target — keep mining"))

	case core.OutcomeStale:
		metrics.ShareResult(c.d.coinID, core.OutcomeStale)
		c.ab.record(core.OutcomeStale) // 非 violent
		return req.keepAlive, c.writeResultID(id, req.keepAlive, nil,
			rpcErr(-32002, "stale template (id unknown or superseded), refetch"))

	case core.OutcomeLowDiff:
		return c.rejectSubmit(id, req, core.OutcomeLowDiff, "low difficulty share")
	case core.OutcomeDup:
		return c.rejectSubmit(id, req, core.OutcomeDup, "duplicate share")
	default:
		return c.rejectSubmit(id, req, core.OutcomeMalformed, "malformed submit")
	}
}

// rejectSubmit 拒绝应答 + autoban 记账；触发 ban 时断开连接。
func (c *noidConn) rejectSubmit(id json.RawMessage, req *httpRequest, outcome core.ShareOutcome, message string) (bool, error) {
	metrics.ShareResult(c.d.coinID, outcome)
	if err := c.writeResultID(id, req.keepAlive, nil, rpcErr(-32003, message)); err != nil {
		return false, err
	}
	if c.ab.record(outcome) {
		return false, fmt.Errorf("[%s] %s 自动 ban（恶意提交占比超阈值）", c.d.coinID, c.remoteIP)
	}
	return req.keepAlive, nil
}

// ---- 响应写出（手写 HTTP/1.1，JSON-RPC 错误也用 HTTP 200，jsonrpsee 同款）----

func (c *noidConn) writeResult(req *httpRequest, result any, errObj *rpcError) error {
	keep := req != nil && req.keepAlive
	return c.writeResultID(nil, keep, result, errObj)
}

func (c *noidConn) writeResultID(id json.RawMessage, keepAlive bool, result any, errObj *rpcError) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	env := map[string]any{"jsonrpc": "2.0", "id": id}
	if errObj != nil {
		env["error"] = errObj
	} else {
		env["result"] = result
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	conn := header200(payload, keepAlive)
	_ = c.raw.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, werr := c.raw.Write(conn)
	return werr
}

// header200 拼 HTTP/1.1 200 响应（Content-Length + keep-alive）。
func header200(body []byte, keepAlive bool) []byte {
	connHdr := "close"
	if keepAlive {
		connHdr = "keep-alive"
	}
	head := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: %s\r\n\r\n", len(body), connHdr)
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	out = append(out, body...)
	return out
}

// rpcError JSON-RPC 2.0 error 对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcErr(code int, msg string) *rpcError { return &rpcError{Code: code, Message: msg} }

// ---- 解析工具 ----

// bearerToken 从 Authorization 头取 Bearer token。
func bearerToken(authz string) string {
	authz = strings.TrimSpace(authz)
	const p = "bearer "
	if len(authz) >= len(p) && strings.EqualFold(authz[:len(p)], p) {
		return strings.TrimSpace(authz[len(p):])
	}
	return ""
}

// parseNoidSubmitParams 解析 submitBlock 参数：位置数组 [template_id, nonce_hex] 或
// 对象 {template_id, nonce_hex}（jsonrpsee 两种都可能）。
func parseNoidSubmitParams(params json.RawMessage) (templateID, nonceHex string, ok bool) {
	var arr []string
	if err := json.Unmarshal(params, &arr); err == nil {
		if len(arr) >= 2 && arr[0] != "" && arr[1] != "" {
			return arr[0], arr[1], true
		}
		return "", "", false
	}
	var obj struct {
		TemplateID string `json:"template_id"`
		NonceHex   string `json:"nonce_hex"`
	}
	if err := json.Unmarshal(params, &obj); err == nil && obj.TemplateID != "" && obj.NonceHex != "" {
		return obj.TemplateID, obj.NonceHex, true
	}
	return "", "", false
}

// targetLE32 难度 → 32B 小端 share target（floor(Diff1/diff)，256-bit LE，le256_lt 直用）。
// 与 noidp2b.Check 的 target 口径一致：hex(targetLE) = difficulty_target_hex 下发矿工。
func targetLE32(diff float64) [32]byte {
	t := cnwork.TargetFromDiff(diff) // big.Int，floor(Diff1/diff)，≥1
	if t.Cmp(maxTarget256) > 0 {
		t = maxTarget256
	}
	be := t.Bytes() // 大端
	if len(be) > 32 {
		be = be[len(be)-32:]
	}
	var out [32]byte
	for i := 0; i < len(be); i++ {
		out[i] = be[len(be)-1-i] // BE → LE
	}
	return out
}

var maxTarget256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
