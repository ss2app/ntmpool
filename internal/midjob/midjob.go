// Package midjob 是 midstate (MDS) 的作业管理器——第四条作业管线
// （与 jobmanager=GBT 系 / cnjob=blob 系 / btc09job 平行）。唯一事实源 docs/07。
//
// midstate 的模板流是「池先分账 → 节点给挖矿 hash」（与 GBT 相反）：
//
//	① /state 变化（height/header_hash/target 任一）→ 重切模板
//	② PPLNS 窗口快照 + 可用 carry（ledger.DirectPlanInputs）→ model-a 分账
//	   （每人金额 2 的幂分解、top-K 限项、尘埃阈值结转、费侧吸收残差凑 Expected）
//	③ POST /block_template（"Coinbase mismatch. Expected: N" → 按 N 重建 ≤4 次）
//	④ job =（height, mining_midstate, target）下发矿工；share=（nonce, final_hash）
//	⑤ 池端全量 VDF 重算（≈150ms/share，有界并发）；badpow tripwire；
//	   命中网络目标 → 直付块（FoundBlock.Direct=分账快照）意图先落库 → /submit_batch
//
// 打款引擎永不开（无池端转账腿）；「打款」= coinbase 直付，confirm 时按快照入账。
package midjob

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"math/bits"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter/midstaterpc"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// nodeIface Manager 对节点的最小依赖（e2e 打桩）。
type nodeIface interface {
	State(ctx context.Context) (midstaterpc.State, error)
	BlockTemplate(ctx context.Context, coinbase []midstaterpc.CoinbaseOut) (*midstaterpc.Template, error)
	SubmitBatch(ctx context.Context, batchTemplate json.RawMessage, nonce uint64, finalHash []byte) error
}

// planner 会计层直付计划输入（accounting.Ledger 子集）。
type planner interface {
	DirectPlanInputs(ctx context.Context, coin string, windowWeight float64) (map[string]float64, map[string]string, error)
}

// SubmitFunc / BlockSink / AcceptedSink 会计层注入（与其余管线同形）。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

// Config 分账策略参数（除 PoolAddress/Decimals 外均可热更）。
type Config struct {
	FeePercent       float64 // 模板分账费率（%）
	MinPayoutUnits   uint64  // 尘埃阈值（units）：credit+carry 低于它 → 结转不进 coinbase
	MaxCoinsPerMiner int     // 每矿工每块最多 2 的幂输出数（top-K 截断，余额结转）
	MaxPayoutMiners  int     // 每块最多付几个矿工（超出按权重截断，结转）
	PplnsFactor      float64 // 窗口 = factor × 网络难度
	PoolAddress      string  // 费/残差收款地址（hex64）
	Decimals         int     // 金额字符串小数位（midstate=9：1 gMDS=1e9 units）
}

type job struct {
	height       uint64
	basis        string // height|header_hash|target —— 重切触发指纹
	midstate     [32]byte
	midstateHex  string
	netTarget    *big.Int
	netTargetHex string
	netDiff      float64
	total        uint64 // E = block_reward + total_fees（Expected 收敛值）
	batch        json.RawMessage
	credits      []core.DirectCredit
}

// Manager 每币一个。实现 coininstance.jobPipe + stratum.MidstateShareHandler。
type Manager struct {
	coinID string
	node   nodeIface
	hsh    hasher.Hasher
	ledger planner

	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu   sync.Mutex
	cfg  Config
	cur  *job
	prev *job // 同高度重切（reorg 换基底）时的上一份——badpow/stale 消歧

	// needRecut 冷启动补切：空分账模板（冷启动/长闲窗口为空 → 全额归费）收到
	// 首个 accepted share 后置位，下一轮 Refresh 同高度重切让矿工进分账。
	// 在飞的一批老工作经 prev-job 消歧记 stale（一次性、可忽略的冷启动代价）。
	needRecut atomic.Bool

	verifySem chan struct{} // VDF 重算有界并发（≈150ms/share，防提交洪峰打爆）
}

var max256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

func New(coinID string, node nodeIface, hsh hasher.Hasher, ledger planner, cfg Config) *Manager {
	if cfg.MaxCoinsPerMiner <= 0 {
		cfg.MaxCoinsPerMiner = 6
	}
	if cfg.MaxPayoutMiners <= 0 {
		cfg.MaxPayoutMiners = 300 // fork 池同款（300×K+费 ≪ 节点 MAX_BATCH_OUTPUTS=10000）
	}
	if cfg.PplnsFactor <= 0 {
		cfg.PplnsFactor = 2
	}
	n := runtime.NumCPU() / 4
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return &Manager{
		coinID: coinID, node: node, hsh: hsh, ledger: ledger, cfg: cfg,
		verifySem: make(chan struct{}, n),
	}
}

// SetCallbacks 注入广播与会计回调。
func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// SetPayoutParams 热更分账参数（admin PATCH payout → coininstance.ApplyPayout 接线）。
// minPayout 为十进制字符串（币），空串 = 不改阈值。下一次模板重切生效，不追溯。
func (m *Manager) SetPayoutParams(feePercent float64, minPayout string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if feePercent >= 0 && feePercent < 100 {
		m.cfg.FeePercent = feePercent
	}
	if minPayout != "" {
		if sat, err := accounting.ParseAmount(minPayout, m.cfg.Decimals); err == nil && sat >= 0 {
			m.cfg.MinPayoutUnits = uint64(sat)
		}
	}
	// 参数变了也要重切模板（新费率随下一个 basis 变化生效即可，不强制）
}

// ---- coininstance.jobPipe ----

// Refresh 拉 /state；(height, header_hash, target) 任一变化才重切模板。
// ⚠绝不同基底无因重切：方言 stale 守卫是 job_id==height，同高度换 midstate
// 会让在飞 share 撞 badpow（消歧靠 prev job，见 HandleSubmit）。
func (m *Manager) Refresh(ctx context.Context, _ bool) error {
	st, err := m.node.State(ctx)
	if err != nil {
		return err
	}
	if st.IsSyncing {
		// 节点补链中：所有 tip 都是已过时高度，暂停模板（上游明确要求；
		// fork 池没做，我们做）。矿工 submit 收 "pool warming up"。
		m.mu.Lock()
		hadJob := m.cur != nil
		m.cur, m.prev = nil, nil
		m.mu.Unlock()
		if hadJob {
			log.Printf("[%s] 节点补链中（is_syncing），模板已暂停", m.coinID)
		}
		return nil
	}
	basis := fmt.Sprintf("%d|%s|%s", st.Height, strings.ToLower(st.HeaderHash), strings.ToLower(st.Target))
	m.mu.Lock()
	if m.cur != nil && m.cur.basis == basis {
		// 冷启动补切：空分账模板 + 已有 accepted share → 同高度重切一次
		if !(len(m.cur.credits) == 0 && m.needRecut.Load()) {
			m.mu.Unlock()
			return nil
		}
	}
	cfg := m.cfg
	m.mu.Unlock()

	_, netDiff, err := parseTargetHex(st.Target)
	if err != nil {
		return fmt.Errorf("[%s] /state target 非法: %w", m.coinID, err)
	}
	// 分账计划输入（窗口快照 + 可用 carry），与 ConfirmBlock 同一 windowWeight 口径
	weights, carryStr, err := m.ledger.DirectPlanInputs(ctx, m.coinID, cfg.PplnsFactor*netDiff)
	if err != nil {
		return fmt.Errorf("[%s] 直付计划输入: %w", m.coinID, err)
	}
	carry := map[string]uint64{}
	for a, s := range carryStr {
		sat, err := accounting.ParseAmount(s, cfg.Decimals)
		if err != nil || sat <= 0 {
			continue
		}
		carry[a] = uint64(sat)
	}

	// "Coinbase mismatch. Expected: N" 重试环（fork 池同款，≤4 次收敛）
	total := st.BlockReward
	var tpl *midstaterpc.Template
	var credits []core.DirectCredit
	for attempt := 0; attempt < 4; attempt++ {
		outputs, cr := planSplit(cfg, st.Height, total, weights, carry)
		t, err := m.node.BlockTemplate(ctx, outputs)
		if err != nil {
			var mm *midstaterpc.MismatchError
			if asMismatch(err, &mm) && mm.Expected != total {
				total = mm.Expected
				continue
			}
			return fmt.Errorf("[%s] block_template: %w", m.coinID, err)
		}
		tpl, credits = t, cr
		break
	}
	if tpl == nil {
		return fmt.Errorf("[%s] block_template: coinbase 总额未收敛（4 次重试）", m.coinID)
	}

	ms, err := hex32(tpl.MiningMidstate)
	if err != nil {
		return fmt.Errorf("[%s] mining_midstate 非法: %w", m.coinID, err)
	}
	tplTarget, tplDiff, err := parseTargetHex(tpl.Target)
	if err != nil {
		return fmt.Errorf("[%s] 模板 target 非法: %w", m.coinID, err)
	}
	j := &job{
		height: st.Height, basis: basis,
		midstate: ms, midstateHex: strings.ToLower(tpl.MiningMidstate),
		netTarget: tplTarget, netTargetHex: strings.ToLower(tpl.Target), netDiff: tplDiff,
		total: total, batch: tpl.BatchTemplate, credits: credits,
	}

	m.mu.Lock()
	if m.cur != nil && m.cur.height == j.height && m.cur.midstate != j.midstate {
		m.prev = m.cur // 同高度换基底（reorg/冷启动补切）：留一步消歧
	} else if m.cur == nil || m.cur.height != j.height {
		m.prev = nil
	}
	m.cur = j
	m.needRecut.Store(false)
	m.mu.Unlock()

	log.Printf("[%s] 模板已重切 height=%d E=%d 分账=%d人 挖矿hash=%s…",
		m.coinID, j.height, j.total, len(credits), j.midstateHex[:16])
	if m.broadcast != nil {
		m.broadcast()
	}
	return nil
}

// Snapshot 当前 job 的链上视图（coininstance.Network 用）。
func (m *Manager) Snapshot() (height uint64, netDiff float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return 0, 0, false
	}
	return m.cur.height, m.cur.netDiff, true
}

// ---- stratum.MidstateShareHandler ----

// ConnJob 为一条连接物化当前 job：share target = max(vardiff 目标, 网络目标)
// （数值大=更容易；share 绝不比爆块难，fork capped_share_target 同款）。
func (m *Manager) ConnJob(difficulty float64) (stratum.MidstateWireJob, bool) {
	m.mu.Lock()
	j := m.cur
	m.mu.Unlock()
	if j == nil {
		return stratum.MidstateWireJob{}, false
	}
	st := targetForDiff(difficulty)
	if st.Cmp(j.netTarget) < 0 {
		st = j.netTarget
	}
	return stratum.MidstateWireJob{
		JobID:    j.height,
		Midstate: j.midstateHex,
		Target:   hexTarget(st),
	}, true
}

// HandleSubmit 池端全量 VDF 重算一条提交 → tripwire → 难度归属 → 命中检测 → 组块。
// warming=true 表示池无模板（error="pool warming up"，fork 池同款语义）。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.MidstateSubmission) (stratum.SubmitResult, bool) {
	m.mu.Lock()
	j, pj := m.cur, m.prev
	m.mu.Unlock()
	if j == nil {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}, true
	}
	// stale 守卫：submit 带了 job_id 就必须是当前模板高度（fork 池同款）
	if sub.JobID != nil && *sub.JobID != j.height {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}, false
	}

	// 全量 VDF 重算（无捷径，≈150ms）：有界并发，洪峰排队不丢
	select {
	case m.verifySem <- struct{}{}:
	case <-ctx.Done():
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}, false
	}
	h, err := m.hsh.Hash(hasher.MidSeed(j.midstate[:], sub.Nonce))
	<-m.verifySem
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}, false
	}

	// 共识 tripwire：矿工报了 final_hash 就必须与池端重算逐字节一致。
	// 同高度重切（reorg 换基底）的在飞工作是合法老工作 → 消歧成 stale，别污染 badpow。
	if sub.FinalHashHex != "" && !strings.EqualFold(sub.FinalHashHex, hex.EncodeToString(h)) {
		if pj != nil && pj.height == j.height {
			m.verifySem <- struct{}{}
			ph, perr := m.hsh.Hash(hasher.MidSeed(pj.midstate[:], sub.Nonce))
			<-m.verifySem
			if perr == nil && strings.EqualFold(sub.FinalHashHex, hex.EncodeToString(ph)) {
				return stratum.SubmitResult{Outcome: core.OutcomeStale}, false
			}
		}
		return stratum.SubmitResult{Outcome: core.OutcomeBadPow}, false
	}

	hashVal := new(big.Int).SetBytes(h) // 大端严格小于
	shareDiff := diffOfHash(hashVal)

	// 命中网络目标 → 爆块（爆块 share 同样计权）
	if hashVal.Cmp(j.netTarget) < 0 {
		credit, ok := sub.Judge(shareDiff)
		if !ok {
			credit = shareDiff
		}
		if m.onShare != nil {
			m.onShare(ctx, core.Share{
				Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
				RemoteIP: sub.RemoteIP, Difficulty: credit, At: time.Now(),
			})
		}
		m.handleBlock(ctx, j, sub, h)
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: credit}, false
	}

	credit, judged := sub.Judge(shareDiff)
	if !judged {
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}, false
	}
	if m.onShare != nil {
		m.onShare(ctx, core.Share{
			Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
			RemoteIP: sub.RemoteIP, Difficulty: credit, At: time.Now(),
		})
	}
	if len(j.credits) == 0 {
		m.needRecut.Store(true) // 冷启动：窗口有人了，下一轮 Refresh 补切分账
	}
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: credit}, false
}

// handleBlock 直付块提交：块身份 = hex(final_hash)（本地即链上真身份——midstate
// 的 header_hash 不含 extension，final_hash 才每解唯一，/block/{h} 直接可比）。
// FoundBlock.Direct = 模板分账快照 → RecordBlock 落库（意图先落库）→ /submit_batch。
func (m *Manager) handleBlock(ctx context.Context, j *job, sub stratum.MidstateSubmission, h []byte) {
	nonce := sub.Nonce
	batch := j.batch
	submit := func(ctx context.Context) (string, error) {
		if err := m.node.SubmitBatch(ctx, batch, nonce, h); err != nil {
			// ⚠「Block validation timed out」是已知假阴性（节点可能已 apply+广播）
			// —— 返回错误让 blockSink 留 submitting 交分类器按链归位，绝不重交。
			return "", err
		}
		return hex.EncodeToString(h), nil
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: hex.EncodeToString(h),
		Finder: sub.Address, Worker: sub.Worker,
		Reward:  accounting.FormatAmount(int64(j.total), m.decimals()),
		NetDiff: j.netDiff,
		Status:  core.BlockPending, FoundAt: time.Now(),
		Direct: j.credits,
	}
	if fb.Direct == nil {
		fb.Direct = []core.DirectCredit{} // 空分账也必须标记直付（全额归费，账实一致）
	}
	_ = m.onBlock(ctx, fb, "", submit)
}

func (m *Manager) decimals() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Decimals
}

// ---- model-a 直付分账（分账数学只在这一处，docs/07 §6）----

// planSplit 把 total（=E）切成 coinbase 输出 + 分账快照：
//
//	D = E × (1 − fee%)；c_i = floor(D × w_i/Σw)；raw_i = c_i + carry_i
//	pay_i = topK(raw_i)（≥ 尘埃阈值、按权重截前 MaxPayoutMiners、预算 ≤E 封顶）
//	费输出 = E − Σpay（吸收费率+取整+结转，decompose 成 2 的幂 → PoolAddress）
//
// 不变量：Σ所有输出 == total；每个输出非零 2 的幂。
func planSplit(cfg Config, height, total uint64, weights map[string]float64, carry map[string]uint64) ([]midstaterpc.CoinbaseOut, []core.DirectCredit) {
	feeBps := uint64(cfg.FeePercent * 100)
	if feeBps > 10000 {
		feeBps = 10000
	}
	// 精确整数：total ≤ 2^49 时 total×10^4 不溢 u64（midstate 块奖励 2^30 量级）
	distributable := total * (10000 - feeBps) / 10000

	sumW := 0.0
	for _, w := range weights {
		sumW += w
	}

	type cand struct {
		addr   string
		w      float64
		credit uint64
	}
	cands := make([]cand, 0, len(weights)+len(carry))
	for a, w := range weights {
		var c uint64
		if sumW > 0 {
			c = uint64(float64(distributable) * w / sumW) // 与账本分账同款 float 比例 floor
		}
		cands = append(cands, cand{addr: a, w: w, credit: c})
	}
	for a := range carry {
		if _, in := weights[a]; !in {
			cands = append(cands, cand{addr: a}) // 离场矿工的结转也有机会兑付
		}
	}
	sort.Slice(cands, func(i, k int) bool {
		if cands[i].w != cands[k].w {
			return cands[i].w > cands[k].w
		}
		return cands[i].addr < cands[k].addr
	})

	var outputs []midstaterpc.CoinbaseOut
	var credits []core.DirectCredit
	saltIdx := uint64(0)
	emit := func(addr string, denoms []uint64) {
		for _, d := range denoms {
			outputs = append(outputs, midstaterpc.CoinbaseOut{
				Address: addr, Value: d,
				Salt: hex.EncodeToString(hasher.MidSalt(height, saltIdx)),
			})
			saltIdx++
		}
	}

	budget := total // Σpay 绝不能超过本块可分总额（carry 大户一次兑付的封顶）
	paidMiners := 0
	for _, c := range cands {
		raw := c.credit + carry[c.addr]
		var pay uint64
		if paidMiners < cfg.MaxPayoutMiners && raw > 0 && raw >= cfg.MinPayoutUnits {
			pay = topKPow2(raw, cfg.MaxCoinsPerMiner)
			if pay > budget {
				pay = topKPow2(budget, cfg.MaxCoinsPerMiner)
			}
			if pay > 0 {
				paidMiners++
				budget -= pay
			}
		}
		if c.credit == 0 && pay == 0 {
			continue // 离场矿工结转未达阈值：不记快照（结转原样留在余额）
		}
		emit(c.addr, decomposePow2(pay))
		credits = append(credits, core.DirectCredit{
			Address: c.addr,
			Credit:  accounting.FormatAmount(int64(c.credit), cfg.Decimals),
			Paid:    accounting.FormatAmount(int64(pay), cfg.Decimals),
		})
	}

	// 费/残差（= E − Σpay，含费率+取整+尘埃结转；budget 即剩余）
	emit(cfg.PoolAddress, decomposePow2(budget))
	return outputs, credits
}

// topKPow2 取 v 二进制展开的最高 k 位之和（≤v；限制每矿工输出数，防尘埃 UTXO 轰炸
// ——15eef1fa 两万碎币惨案的教训）。
func topKPow2(v uint64, k int) uint64 {
	var out uint64
	for i := 0; i < k && v != 0; i++ {
		top := uint64(1) << (63 - bits.LeadingZeros64(v))
		out += top
		v -= top
	}
	return out
}

// decomposePow2 二进制分解（升序，节点 decompose_value 同款）。
func decomposePow2(v uint64) []uint64 {
	var parts []uint64
	for bit := uint64(1); v != 0; bit <<= 1 {
		if v&1 == 1 {
			parts = append(parts, bit)
		}
		v >>= 1
	}
	return parts
}

// ---- 难度/目标（连续标尺：diff = (2^256−1)/target，1 难度 ≈ 1 次 VDF）----

func parseTargetHex(s string) (*big.Int, float64, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, 0, fmt.Errorf("target 非 32 字节 hex: %q", s)
	}
	t := new(big.Int).SetBytes(b)
	if t.Sign() <= 0 {
		return nil, 0, fmt.Errorf("target 为零")
	}
	d, _ := new(big.Float).Quo(new(big.Float).SetInt(max256), new(big.Float).SetInt(t)).Float64()
	return t, d, nil
}

func diffOfHash(hashVal *big.Int) float64 {
	if hashVal.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(new(big.Float).SetInt(max256), new(big.Float).SetInt(hashVal)).Float64()
	return q
}

// targetForDiff 难度 → 256-bit 目标（有理数除法，btc09job 同款）。
func targetForDiff(d float64) *big.Int {
	if d <= 0 {
		d = 1
	}
	num := new(big.Int).Mul(max256, big.NewInt(1_000_000))
	den := big.NewInt(int64(d * 1_000_000))
	if den.Sign() <= 0 {
		den = big.NewInt(1)
	}
	t := num.Div(num, den)
	if t.Cmp(max256) > 0 {
		t = new(big.Int).Set(max256)
	}
	return t
}

// hexTarget 256-bit 目标 → 固定 64-hex 大端。
func hexTarget(t *big.Int) string {
	b := t.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return hex.EncodeToString(out)
}

func hex32(s string) ([32]byte, error) {
	var a [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return a, fmt.Errorf("非 32 字节 hex: %q", s)
	}
	copy(a[:], b)
	return a, nil
}

func asMismatch(err error, out **midstaterpc.MismatchError) bool {
	return errors.As(err, out)
}
