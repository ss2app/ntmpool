// midstate (MDS) 方言：裸 TCP + 换行 JSON。wire 协议逐字段复刻 fork 池
// midstate-pool/src/main.rs stratum mod（含错误串逐字）——NTMminer -a midstate
// 现役发行版不改一个字节就能连（nm_stratum.c:1027 起是矿工侧事实源）。
//
// 协议（fork 池即事实标准，docs/07 §3）：
//   - 首行必须 subscribe：params {address:<hex64>, device:<清洗后≤16字符>}；
//     地址非法直接断连（fork 同款）；非 subscribe 首行 →
//     {"id":…,"error":"expected subscribe first"} 断连。
//   - 成功 → {"id":…,"result":{"job_id":H,"midstate":hex64,"target":hex64}}
//     或暖机 {"id":…,"error":"pool warming up"}。
//   - job 推送（无 id）：{"method":"job","params":{job_id,midstate,target}}
//     ——tip 变化推 + 每次 submit 应答后无条件重推（vardiff 新 target 随推；
//     NTMminer 只在 midstate 真变时丢在飞批次，同 midstate 换 target 不丢工）。
//   - submit：params {job_id:<u64>, nonce:<u64 数字>, final_hash:<hex64 可选>}；
//     应答 {"id":…,"result":{"status":"accepted"|"block"}} 或 {"id":…,"error":串}：
//     "pool warming up" / "stale share (job expired)" / "insufficient PoW for share"
//     / "final_hash mismatch (miner hash != pool recompute)" / "submit missing nonce"
//     / "duplicate share"（我们加的防重放，诚实矿工永不触发）。
//   - 非法 JSON 行 → {"error":"bad json: …"}（无 id，不断连）；
//     未知方法 → {"id":…,"error":"unsupported method: <m>"}；空行忽略。
//
// 链差异（VDF 重算、直付分账、组块）全部下沉到 MidstateShareHandler
// （internal/midjob 实现）；本文件只做 wire 协议。
package stratum

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/metrics"
	"github.com/scashcc/ntmpool/internal/vardiff"
)

// MidstateWireJob 下发矿工的 job（handler 物化好，dialect 原样上线）。
type MidstateWireJob struct {
	JobID    uint64 `json:"job_id"`   // = 模板高度
	Midstate string `json:"midstate"` // hex64 挖矿 hash
	Target   string `json:"target"`   // hex64 share 目标（per-conn vardiff，钳网络）
}

// MidstateSubmission 一条已解析的 midstate submit。
type MidstateSubmission struct {
	Address      string
	Worker       string // = subscribe 的 device 标签（cpu/gpu/自定义机名）
	RemoteIP     string
	JobID        *uint64 // submit 可不带（fork 同款：带了才做 stale 守卫）
	Nonce        uint64
	FinalHashHex string // 可选；带了就做 badpow tripwire
	Judge        func(shareDiff float64) (creditDiff float64, ok bool)
}

// MidstateShareHandler 方言与作业管线的解耦点（internal/midjob 实现）。
type MidstateShareHandler interface {
	// ConnJob 为一条连接物化当前 job；ok=false = 池暖机中。
	ConnJob(difficulty float64) (MidstateWireJob, bool)
	// HandleSubmit 校验一条提交；warming=true = 池无模板（"pool warming up"）。
	HandleSubmit(ctx context.Context, sub MidstateSubmission) (res SubmitResult, warming bool)
}

// MidstateDialect 实现 stratum.Dialect。
type MidstateDialect struct {
	coinID  string
	handler MidstateShareHandler
	conns   sync.Map // *midstateConn → struct{}
	banner  Banner
}

func NewMidstateDialect(coinID string, h MidstateShareHandler) *MidstateDialect {
	return &MidstateDialect{coinID: coinID, handler: h}
}

func (d *MidstateDialect) SetAutoBan(b Banner) { d.banner = b }

func (d *MidstateDialect) Name() string { return "midstate" }

func (d *MidstateDialect) ConnCount() int {
	n := 0
	d.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// BroadcastJob 新模板到达时推最新 job 给所有已订阅矿工。
func (d *MidstateDialect) BroadcastJob() {
	d.conns.Range(func(k, _ any) bool {
		c := k.(*midstateConn)
		go func() { _ = c.pushJob() }()
		return true
	})
}

type midstateConn struct {
	d    *MidstateDialect
	raw  net.Conn
	wmu  sync.Mutex
	port config.PortConfig
	vd   *vardiff.State

	mu         sync.Mutex
	address    string
	worker     string
	subscribed bool
	ab         *autoBan
	guard      *connectionGuard
	remoteIP   string

	seen sync.Map // jobid:nonce → 去重（fork 池没有；防重放双计，诚实矿工永不触发）
}

func (d *MidstateDialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	port = config.WithPortDefaults(port)
	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	c := &midstateConn{
		d: d, raw: conn, port: port,
		vd:       vardiff.New(vcfg, time.Now()),
		remoteIP: verifiedClientIP(conn),
		guard:    newConnectionGuard(port),
	}
	c.ab = newAutoBan(d.banner, conn, d.coinID, port)
	d.conns.Store(c, struct{}{})
	defer d.conns.Delete(c)

	lines := scanLines(ctx, conn, port.MessageMaxBytes)

	idle := time.NewTimer(10 * time.Minute)
	defer idle.Stop()
	handshake := time.NewTimer(port.HandshakeTimeout())
	defer handshake.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return fmt.Errorf("[%s] midstate 连接空闲超时 %s", d.coinID, c.remoteIP)
		case <-handshake.C:
			return fmt.Errorf("[%s] midstate subscribe 超时 verified_client_ip=%q", d.coinID, c.remoteIP)
		case frame, ok := <-lines:
			if !ok {
				return nil // 矿工 EOF
			}
			if frame.err != nil {
				return fmt.Errorf("[%s] midstate 消息读取失败: %w", d.coinID, frame.err)
			}
			line := frame.text
			idle.Reset(10 * time.Minute)
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := c.guard.observe(line, c.isSubscribed()); err != nil {
				return fmt.Errorf("[%s] midstate 分级校验拒绝: %w", d.coinID, err)
			}
			var msg midstateReq
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				// fork 同款：坏 JSON 回错误行但不断连
				_ = c.writeJSON(map[string]any{"error": fmt.Sprintf("bad json: %v", err)})
				continue
			}
			if err := c.dispatch(ctx, &msg); err != nil {
				return err
			}
			if c.isSubscribed() {
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

type midstateReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type midstateSubscribeParams struct {
	Address string `json:"address"`
	Device  string `json:"device"`
}

type midstateSubmitParams struct {
	JobID     *uint64 `json:"job_id"`
	Nonce     *uint64 `json:"nonce"`
	FinalHash string  `json:"final_hash"`
}

func (c *midstateConn) dispatch(ctx context.Context, msg *midstateReq) error {
	// fork 同款状态机：首行必须 subscribe
	if !c.isSubscribed() && msg.Method != "subscribe" {
		_ = c.replyErr(msg.ID, "expected subscribe first")
		return fmt.Errorf("[%s] %s 未订阅先发 %q", c.d.coinID, c.remoteIP, msg.Method)
	}
	switch msg.Method {
	case "subscribe":
		return c.onSubscribe(msg)
	case "submit":
		return c.onSubmit(ctx, msg)
	default:
		return c.replyErr(msg.ID, "unsupported method: "+msg.Method)
	}
}

// sanitizeDevice 自报硬件标签清洗（fork sanitize_device 同款）：
// alnum/-/_、≤16 字符、小写；空 → "cpu"。仅统计展示，不进共识。
func sanitizeDevice(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 16 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	if b.Len() == 0 {
		return "cpu"
	}
	return b.String()
}

func (c *midstateConn) onSubscribe(msg *midstateReq) error {
	var p midstateSubscribeParams
	_ = json.Unmarshal(msg.Params, &p)
	// 地址必须 64-hex（32 字节 MSS/WOTS 地址）；非法直接断连（fork anyhow bail 同款）
	addr := strings.ToLower(strings.TrimSpace(p.Address))
	if !isHex64(addr) {
		return fmt.Errorf("[%s] %s subscribe 地址非法（须 64-hex）", c.d.coinID, c.remoteIP)
	}
	device := sanitizeDevice(p.Device)

	c.mu.Lock()
	c.address, c.worker, c.subscribed = addr, device, true
	c.mu.Unlock()

	// 初始 job 作为 subscribe result；无模板 → 暖机错误（fork 同款）
	job, ok := c.d.handler.ConnJob(c.vd.Current())
	if !ok {
		return c.replyErr(msg.ID, "pool warming up")
	}
	return c.reply(msg.ID, job)
}

func (c *midstateConn) onSubmit(ctx context.Context, msg *midstateReq) error {
	var p midstateSubmitParams
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.Nonce == nil {
		// fork 同款错误串（nonce 必带；final_hash/job_id 可选）
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "submit missing nonce")
	}
	if p.FinalHash != "" && !isHex64(strings.ToLower(p.FinalHash)) {
		return c.rejectSubmit(msg.ID, core.OutcomeMalformed, "bad final_hash")
	}
	// (job,nonce) 连接内去重（防重放双计）
	jid := "-"
	if p.JobID != nil {
		jid = strconv.FormatUint(*p.JobID, 10)
	}
	dedup := jid + ":" + strconv.FormatUint(*p.Nonce, 10)
	if _, dup := c.seen.LoadOrStore(dedup, struct{}{}); dup {
		return c.rejectSubmit(msg.ID, core.OutcomeDup, "duplicate share")
	}

	c.mu.Lock()
	addr, worker := c.address, c.worker
	c.mu.Unlock()

	sub := MidstateSubmission{
		Address:      addr,
		Worker:       worker,
		RemoteIP:     c.remoteIP,
		JobID:        p.JobID,
		Nonce:        *p.Nonce,
		FinalHashHex: p.FinalHash,
		Judge:        func(d float64) (float64, bool) { return c.vd.Judge(d, time.Now()) },
	}
	release, ok := acquirePowSlot(ctx, c.d.coinID, c.port)
	if !ok {
		return c.replyErr(msg.ID, "server busy (verification queue full)")
	}
	res, warming := c.d.handler.HandleSubmit(ctx, sub)
	release()
	if warming {
		return c.replyErr(msg.ID, "pool warming up")
	}

	switch res.Outcome {
	case core.OutcomeAccepted, core.OutcomeBlock:
		metrics.ShareResult(c.d.coinID, res.Outcome)
		c.ab.record(res.Outcome)
		c.vd.OnAccepted(time.Now())
		status := "accepted"
		if res.Outcome == core.OutcomeBlock {
			status = "block"
		}
		if err := c.reply(msg.ID, map[string]any{"status": status}); err != nil {
			return err
		}
	case core.OutcomeStale:
		if err := c.rejectSubmit(msg.ID, res.Outcome, "stale share (job expired)"); err != nil {
			return err
		}
	case core.OutcomeLowDiff:
		if err := c.rejectSubmit(msg.ID, res.Outcome, "insufficient PoW for share"); err != nil {
			return err
		}
	case core.OutcomeBadPow:
		// 共识 tripwire：矿工 final_hash 与池端 VDF 重算不符（fork 同款错误串）
		if err := c.rejectSubmit(msg.ID, res.Outcome, "final_hash mismatch (miner hash != pool recompute)"); err != nil {
			return err
		}
	default:
		if err := c.rejectSubmit(msg.ID, res.Outcome, "bad share"); err != nil {
			return err
		}
	}

	// fork 同款：每次 submit 应答后无条件重推 job（同高度、最新 vardiff target）。
	// 矿工 nonce 自播种绝不重叠 → retarget 立即生效无 duplicate 风险
	// （btc09 的 pendingDifficulty 顾虑在这里不存在）。
	if c.port.Vardiff.Enabled {
		_, _ = c.vd.MaybeRetarget(time.Now())
	}
	return c.pushJob()
}

func (c *midstateConn) rejectSubmit(id json.RawMessage, outcome core.ShareOutcome, message string) error {
	metrics.ShareResult(c.d.coinID, outcome)
	err := c.replyErr(id, message)
	if c.ab.record(outcome) {
		return fmt.Errorf("[%s] %s 自动 ban（恶意提交占比超阈值）", c.d.coinID, c.remoteIP)
	}
	return err
}

// pushJob 推送当前 job（tip 变化广播 / submit 应答后）。fork 同款无 id 推送。
func (c *midstateConn) pushJob() error {
	if !c.isSubscribed() {
		return nil
	}
	job, ok := c.d.handler.ConnJob(c.vd.Current())
	if !ok {
		return nil // 暖机中不推（矿工重连/submit 时会收到暖机错误）
	}
	return c.writeJSON(map[string]any{"method": "job", "params": job})
}

func (c *midstateConn) isSubscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribed
}

// ---- 响应/底层 I/O（fork 格式：成功 {"id",…,"result":…}，失败 {"id",…,"error":串}）----

func (c *midstateConn) reply(id json.RawMessage, result any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(map[string]any{"id": id, "result": result})
}

func (c *midstateConn) replyErr(id json.RawMessage, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(map[string]any{"id": id, "error": message})
}

func (c *midstateConn) writeJSON(v any) error {
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

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
