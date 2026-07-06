// Stratum V1 方言（bitcoin 系）。docs/04 §1 的实现。
//
// 兼容清单（调研坐实）：
//   - subscribe/authorize/notify/set_difficulty/submit 五件套
//   - mining.configure：哪怕全回 false 也必须回（否则接不了 ASIC）
//   - mining.extranonce.subscribe → true（NiceHash/代理刚需）
//   - submit 容忍第 6 参数 version_bits（version-rolling 激活后）
//   - 响应三件套 result+error+id 齐全（NiceHash verificator 硬要求）
//   - 未知 method 回 error 但不断连
//   - job <60s 定时重发（心跳；由 JobManager 驱动广播）
package stratum

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/vardiff"
)

// SubmitResult 是 share 校验结果，回给 stratum 层决定 ok/error 应答。
type SubmitResult struct {
	Outcome core.ShareOutcome
	// CreditDiff 计权难度（vardiff 一步 grace 下可能是 prevDiff）
	CreditDiff float64
}

// ShareHandler 是 stratum 与会计/JobManager 的解耦点。
// 每个币注入一个实现：校验 share（池端重算，badpow tripwire）+ 命中则组块提交。
type ShareHandler interface {
	// Registry 当前币的 job 注册表（stratum 查 job）。
	Registry() *JobRegistry
	// ExtraNonce2Size extranonce2 字节数（extranonce1 由 stratum 每连接分配）。
	ExtraNonce2Size() int
	// HandleSubmit 校验一条提交。en1 是本连接的 extranonce1。
	// requiredDiff 是矿工当前难度；实现内部做池端重算 + 命中检测 + 组块提交 + 记账。
	HandleSubmit(ctx context.Context, sub Submission) SubmitResult
}

// Submission 一条已解析的 mining.submit。
type Submission struct {
	Address      string
	Worker       string
	UserAgent    string
	RemoteIP     string
	JobID        string
	ExtraNonce1  []byte
	ExtraNonce2  []byte
	NTime        uint32
	Nonce        uint32
	VersionBits  uint32 // version-rolling，未用为 0
	RequiredDiff float64
	Solo         bool
}

// V1Dialect 实现 stratum.Dialect。每个币一个实例（持有该币的 ShareHandler）。
type V1Dialect struct {
	coinID  string
	handler ShareHandler
	connSeq atomic.Uint64
}

func NewV1Dialect(coinID string, h ShareHandler) *V1Dialect {
	return &V1Dialect{coinID: coinID, handler: h}
}

func (d *V1Dialect) Name() string { return "stratum1" }

// rpcMsg 是 stratum 行 JSON（请求与响应共用宽松形状）。
type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  any             `json:"error,omitempty"`
}

// v1Conn 每连接状态。
type v1Conn struct {
	d          *V1Dialect
	raw        net.Conn
	w          *bufio.Writer
	wmu        sync.Mutex
	port       config.PortConfig
	extraNonce1 []byte
	vd         *vardiff.State

	address    string
	worker     string
	userAgent  string
	remoteIP   string
	authorized bool
	subscribed bool
	lastJobID  string

	seen sync.Map // jobid:en2:ntime:nonce → 去重
}

func (d *V1Dialect) Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error {
	seq := d.connSeq.Add(1)
	// extranonce1：4 字节，高位放 connId 段，保证每连接 nonce 空间不相交
	en1 := make([]byte, 4)
	en1[0] = byte(seq >> 16)
	en1[1] = byte(seq >> 8)
	en1[2] = byte(seq)
	en1[3] = 0

	vcfg := vardiff.Config{
		StartDiff:      port.Vardiff.StartDiff,
		MinDiff:        port.Vardiff.MinDiff,
		MaxDiff:        port.Vardiff.MaxDiff,
		TargetInterval: time.Duration(port.Vardiff.TargetSeconds * float64(time.Second)),
		RetargetEvery:  time.Duration(port.Vardiff.RetargetMinSec * float64(time.Second)),
	}
	c := &v1Conn{
		d:           d,
		raw:         conn,
		w:           bufio.NewWriter(conn),
		port:        port,
		extraNonce1: en1,
		vd:          vardiff.New(vcfg, time.Now()),
		remoteIP:    remoteHost(conn),
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), 64*1024) // 抗超大行（协议层洪水防护）
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	// 看门狗：授权后若长时间无任何数据往来则断开（半死连接，pitfall C11）
	idle := time.NewTimer(2 * time.Minute)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			return fmt.Errorf("[%s] 连接空闲超时 %s", d.coinID, c.remoteIP)
		case line, ok := <-lines:
			if !ok {
				return nil // 对端关闭
			}
			idle.Reset(2 * time.Minute)
			if strings.TrimSpace(line) == "" {
				continue
			}
			var msg rpcMsg
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue // 非法 JSON 忽略（不断连）
			}
			if err := c.dispatch(ctx, &msg); err != nil {
				return err
			}
		}
	}
}

func (c *v1Conn) dispatch(ctx context.Context, msg *rpcMsg) error {
	switch msg.Method {
	case "mining.subscribe":
		return c.onSubscribe(msg)
	case "mining.authorize":
		return c.onAuthorize(msg)
	case "mining.configure":
		// version-rolling 等：即便不支持也必须回，避免 ASIC 断连
		return c.reply(msg.ID, map[string]any{"version-rolling": false}, nil)
	case "mining.extranonce.subscribe":
		return c.reply(msg.ID, true, nil)
	case "mining.submit":
		return c.onSubmit(ctx, msg)
	case "mining.suggest_difficulty", "mining.suggest_target":
		return c.reply(msg.ID, true, nil) // 收下但由 vardiff 决定
	default:
		if len(msg.ID) > 0 {
			return c.reply(msg.ID, nil, stratumErr(20, "Unsupported method"))
		}
		return nil // 无 id 的通知类，忽略不断连
	}
}

func (c *v1Conn) onSubscribe(msg *rpcMsg) error {
	// params[0] = user-agent（锄头软件/版本）
	var params []json.RawMessage
	_ = json.Unmarshal(msg.Params, &params)
	if len(params) > 0 {
		var ua string
		if json.Unmarshal(params[0], &ua) == nil {
			c.userAgent = ua
		}
	}
	c.subscribed = true
	en1hex := hex.EncodeToString(c.extraNonce1)
	// result = [[["mining.set_difficulty",sub],["mining.notify",sub]], extranonce1, extranonce2_size]
	sub := [][]string{
		{"mining.set_difficulty", en1hex},
		{"mining.notify", en1hex},
	}
	result := []any{sub, en1hex, c.d.handler.ExtraNonce2Size()}
	if err := c.reply(msg.ID, result, nil); err != nil {
		return err
	}
	// 订阅后立即下发难度 + 当前 job（NiceHash 要求首 job 前先 set_difficulty）
	c.sendDifficulty()
	return c.sendCurrentJob(true)
}

func (c *v1Conn) onAuthorize(msg *rpcMsg) error {
	var params []string
	_ = json.Unmarshal(msg.Params, &params)
	if len(params) < 1 || params[0] == "" {
		return c.reply(msg.ID, false, stratumErr(24, "Unauthorized: empty username"))
	}
	// username 惯例 = 地址.矿工名（按最后一个 . 拆；纯地址无 . 要容错）
	user := params[0]
	if i := strings.LastIndex(user, "."); i >= 0 {
		c.address, c.worker = user[:i], user[i+1:]
	} else {
		c.address, c.worker = user, "default"
	}
	// TODO(M2): 解析 params[1] 密码参数 d=/mp=；校验地址合法性由 ShareHandler/adapter 出
	c.authorized = true
	if err := c.reply(msg.ID, true, nil); err != nil {
		return err
	}
	// 授权后补推一次 job（有些锄头 subscribe/authorize 顺序不同）
	return c.sendCurrentJob(true)
}

func (c *v1Conn) onSubmit(ctx context.Context, msg *rpcMsg) error {
	if !c.authorized {
		return c.reply(msg.ID, nil, stratumErr(24, "Unauthorized worker"))
	}
	// params = [worker, jobID, en2, ntime, nonce, (version_bits)]
	var p []string
	if err := json.Unmarshal(msg.Params, &p); err != nil || len(p) < 5 {
		return c.reply(msg.ID, nil, stratumErr(20, "Malformed submit"))
	}
	en2, err1 := hex.DecodeString(p[2])
	ntime, err2 := parseHexU32(p[3])
	nonce, err3 := parseHexU32(p[4])
	if err1 != nil || err2 != nil || err3 != nil {
		return c.reply(msg.ID, nil, stratumErr(20, "Malformed submit fields"))
	}
	var vbits uint32
	if len(p) >= 6 {
		vbits, _ = parseHexU32(p[5])
	}

	// 去重键（每连接）
	dedup := p[1] + ":" + p[2] + ":" + p[3] + ":" + p[4]
	if _, dup := c.seen.LoadOrStore(dedup, struct{}{}); dup {
		return c.reply(msg.ID, nil, stratumErr(22, "Duplicate share"))
	}

	sub := Submission{
		Address:      c.address,
		Worker:       c.worker,
		UserAgent:    c.userAgent,
		RemoteIP:     c.remoteIP,
		JobID:        p[1],
		ExtraNonce1:  c.extraNonce1,
		ExtraNonce2:  en2,
		NTime:        ntime,
		Nonce:        nonce,
		VersionBits:  vbits,
		RequiredDiff: c.vd.Current(),
		Solo:         c.port.Mode == "solo",
	}
	res := c.d.handler.HandleSubmit(ctx, sub)

	switch res.Outcome {
	case core.OutcomeAccepted, core.OutcomeBlock:
		c.vd.OnAccepted(time.Now())
		if err := c.reply(msg.ID, true, nil); err != nil {
			return err
		}
		c.maybeRetarget()
		return nil
	case core.OutcomeStale:
		return c.reply(msg.ID, nil, stratumErr(21, "Job not found (stale)"))
	case core.OutcomeLowDiff:
		return c.reply(msg.ID, nil, stratumErr(23, "Low difficulty share"))
	case core.OutcomeBadPow:
		// 共识 tripwire：矿工声称的 hash 与池端重算不符
		return c.reply(msg.ID, nil, stratumErr(20, "Bad PoW (recompute mismatch)"))
	default:
		return c.reply(msg.ID, nil, stratumErr(20, "Rejected"))
	}
}

func (c *v1Conn) maybeRetarget() {
	if !c.port.Vardiff.Enabled {
		return
	}
	if _, changed := c.vd.MaybeRetarget(time.Now()); changed {
		c.sendDifficulty()
		// 难度变更后随下一个 job 生效（这里主动补一个非 clean 的 job）
		_ = c.sendCurrentJob(false)
	}
}

func (c *v1Conn) sendDifficulty() {
	_ = c.notify("mining.set_difficulty", []any{c.vd.Current()})
}

// sendCurrentJob 组当前 job 的 mining.notify 并发送。
func (c *v1Conn) sendCurrentJob(clean bool) error {
	if !c.subscribed {
		return nil
	}
	j, ok := c.d.handler.Registry().Current()
	if !ok {
		return nil // 还没有 job（模板未就绪）
	}
	c.lastJobID = j.ID
	prevStratum, err := btcwork.PrevHashStratum(j.PrevHashBE)
	if err != nil {
		return nil
	}
	branch := make([]string, len(j.MerkleBranch))
	for i, b := range j.MerkleBranch {
		branch[i] = hex.EncodeToString(b)
	}
	params := []any{
		j.ID,
		prevStratum,
		hex.EncodeToString(j.Coinbase.Coinb1),
		hex.EncodeToString(j.Coinbase.Coinb2),
		branch,
		u32hex(j.Version),
		u32hex(j.Bits),
		u32hex(j.NTime),
		clean || j.CleanJobs,
	}
	return c.notify("mining.notify", params)
}

// ---- 底层 I/O ----

func (c *v1Conn) reply(id json.RawMessage, result any, errObj any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return c.writeJSON(rpcMsg{ID: id, Result: result, Error: errObj})
}

func (c *v1Conn) notify(method string, params []any) error {
	pb, _ := json.Marshal(params)
	return c.writeJSON(rpcMsg{ID: json.RawMessage("null"), Method: method, Params: pb})
}

func (c *v1Conn) writeJSON(m rpcMsg) error {
	// 保证 result+error+id 三件套齐全（NiceHash verificator 硬要求）：
	// 响应类（有 Result 或 Error）显式补 null。
	b, err := marshalStratum(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return c.w.Flush()
}

func stratumErr(code int, msg string) []any {
	return []any{code, msg, nil}
}
