// Package velkarjob 是 Velkar(VELK，Kaspa 系) 的作业管理器：
// gRPC 模板 → 共享 job（pre_pow_hash + timestamp + block target）→ 池端重算 VelkarHash
// 校验份额 → 命中网络 target 爆块整块提交 → 记账回调。
//
// 与 internal/cnjob（blob 系）平行，同样实现 coininstance 的作业管线面（Refresh/Snapshot）
// 与 stratum 方言的解耦面（stratum.KaspaShareHandler）。velkar 的三点特殊：
//   - 单条共享 job：所有连接挖同一个 block 模板（coinbase 付矿池），靠 nonce 空间分片
//     （extranonce = nonce 高位，由 Kaspa 方言按连接分配）区隔，池端 PPLNS 分账。
//   - VelkarHash 的 stage4 吃 block target（从 header.bits 解出，非份额 target）——池端重算
//     必须把 work.BlockTargetLE 传进 velkarhash.CalculatePow，这是 velkar 独有点。
//   - 无 badpow tripwire：Kaspa stratum submit 只回 nonce，矿工不上报 hash，池自算权威 pow
//     无"矿工声称值"可比对；等价校验 = pow 是否达 share/网络难度（lowdiff / block）。
package velkarjob

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/velkarhash"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// velkarDiff1 = 2^224 - 1（Velkar/Kaspa 的 maxTarget，crypto hasher.rs::MAX_TARGET 28 字节全 F）。
// 份额难度语义与锄头一致：target(D) = Diff1/D，share 达标 ⟺ pow ≤ Diff1/D ⟺ Diff1/pow ≥ D。
// ⚠ 因 pow 是 256 位而 maxTarget 只 224 位，难度 1 ≈ 2^32 次哈希 → velkar 池用小数难度
// （start≈5e-4/min≈1e-4），vardiff 全程用 float，与 NTM 锄头同一 Diff1 口径即自洽。
var velkarDiff1 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 224), big.NewInt(1))

// nodeIface Manager 对节点的最小依赖（便于测试打桩）。
type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	// SubmitSolved 回填 nonce/timestamp 整块提交，返回链上权威块 id。
	SubmitSolved(ctx context.Context, work *velkarrpc.VelkarWork, nonce uint64) (blockHash string, err error)
}

// SubmitFunc / BlockSink / AcceptedSink 会计层注入（与 cnjob/jobmanager 同形）。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

// velkarJob 一个模板级 job。work 承载 pre_pow_hash / timestamp / block target / 完整块。
type velkarJob struct {
	id      uint64
	work    *velkarrpc.VelkarWork
	height  uint64
	reward  string  // 十进制字符串（适配器已归一）
	netDiff float64 // = Diff1 / BlockTargetBE（PPLNS 窗口 = pplnsN × netDiff）
}

// Manager 每币一个。实现 stratum.KaspaShareHandler 与 coininstance 作业管线面。
type Manager struct {
	coinID string
	node   nodeIface
	algo   string

	jobSeq    atomic.Uint64
	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu        sync.Mutex
	jobs      map[uint64]*velkarJob
	order     []uint64
	current   uint64
	hasJob    bool
	lastFetch time.Time
}

// keepJobs 保留最近 N 个 job 供在途 share 归属（矿工 RTT + 池换 job 窗口）。
const keepJobs = 24

// templateRefresh 同 job 重拉节流。velkar 10s 出块、gRPC 推送为主，2s 轮询兜底；
// 同 pre_pow（JobKey）不重发 job（timestamp 抖动不换工），此节流仅防同工作重复登记。
const templateRefresh = 8 * time.Second

var _ stratum.KaspaShareHandler = (*Manager)(nil)

func New(coinID, algo string, node nodeIface) *Manager {
	return &Manager{
		coinID: coinID, algo: algo, node: node,
		jobs: map[uint64]*velkarJob{},
	}
}

// SetCallbacks 注入广播与会计回调。
func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Refresh 拉模板 → 登记 job → 广播。同一份可挖工作（JobKey=hex(prePow)）不发新 job，
// 避免矿工无谓换工（timestamp 每次拉都变但不入 pre_pow_hash，故不算换工）。
// clean=true（高度变化/首拉）跳过节流立即拉。
func (m *Manager) Refresh(ctx context.Context, clean bool) error {
	m.mu.Lock()
	if !clean && !m.lastFetch.IsZero() && time.Since(m.lastFetch) < templateRefresh {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	tpl, err := m.node.GetTemplate(ctx)
	if err != nil {
		return err
	}
	work, ok := tpl.Raw.(*velkarrpc.VelkarWork)
	if !ok {
		return fmt.Errorf("[%s] 模板 Raw 非 *velkarrpc.VelkarWork（适配器实现错误）", m.coinID)
	}
	if err := validateWork(work); err != nil {
		return fmt.Errorf("[%s] 模板非法: %w", m.coinID, err)
	}

	m.mu.Lock()
	m.lastFetch = time.Now()
	// 同一份可挖工作就不发新 job（JobKey=hex(prePow)：同 pre_pow 即同一块模板）。
	if cur, ok := m.jobs[m.current]; ok && m.hasJob && cur.work.JobKey == work.JobKey {
		m.mu.Unlock()
		return nil
	}
	id := m.jobSeq.Add(1)
	reward := tpl.CoinbaseValue
	if reward == "" {
		reward = "0" // 防空串塞 numeric 列（midstate 22P02 教训）
	}
	j := &velkarJob{
		id:      id,
		work:    work,
		height:  tpl.Height,
		reward:  reward,
		netDiff: diffFromTargetBE(work.BlockTargetBE),
	}
	m.jobs[id] = j
	m.order = append(m.order, id)
	m.current = id
	m.hasJob = true
	for len(m.order) > keepJobs {
		old := m.order[0]
		m.order = m.order[1:]
		delete(m.jobs, old)
	}
	m.mu.Unlock()

	if m.broadcast != nil {
		m.broadcast()
	}
	return nil
}

// Snapshot 当前 job 的链上视图（coininstance.Network 用）。
func (m *Manager) Snapshot() (height uint64, netDiff float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, found := m.jobs[m.current]
	if !found || !m.hasJob {
		return 0, 0, false
	}
	return j.height, j.netDiff, true
}

// ---- stratum.KaspaShareHandler ----

func (m *Manager) Algo() string { return m.algo }

// CurrentJob 返回当前共享 job（所有连接挖同一块模板，extranonce 由方言按连接分配）。
func (m *Manager) CurrentJob() (stratum.KaspaWireJob, bool) {
	m.mu.Lock()
	j, ok := m.jobs[m.current]
	has := m.hasJob
	m.mu.Unlock()
	if !ok || !has {
		return stratum.KaspaWireJob{}, false
	}
	return stratum.KaspaWireJob{
		JobID:         j.id,
		PrePowHash:    j.work.PrePowHash, // 32B 内部序，方言转 64hex 下发
		Timestamp:     j.work.Timestamp,
		BlockTargetLE: j.work.BlockTargetLE, // ★VelkarHash 独有：矿工 stage4 需 block target
	}, true
}

// HandleSubmit 池端重算 VelkarHash → 份额难度判定 → 命中网络 target 爆块 → 记账回调。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.KaspaSubmission) stratum.SubmitResult {
	m.mu.Lock()
	j, ok := m.jobs[sub.JobID]
	m.mu.Unlock()
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	work := j.work

	// 池端权威重算：pow = VelkarHash(prePow, timestamp, nonce, blockTargetLE)。
	// ⚠ target 参数 = block target（网络 target），stage4 吃它——velkar 独有点。
	powLE, err := velkarhash.CalculatePow(work.PrePowHash, work.Timestamp, sub.Nonce, work.BlockTargetLE)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	powBE := new(big.Int).SetBytes(reverseBytes(powLE))
	if powBE.Sign() == 0 {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	shareDiff := shareDiffFromPow(powBE)

	// 命中网络 target → 爆块（爆块 share 同样计入 PPLNS 权重，miningcore 同款语义）。
	if powBE.Cmp(work.BlockTargetBE) <= 0 {
		credit, ok := sub.Judge(shareDiff)
		if !ok {
			credit = shareDiff // 低难度端口挖出真块：按实际难度计权（不吞份额）
		}
		if m.onShare != nil {
			m.onShare(ctx, m.share(sub, credit))
		}
		m.handleBlock(ctx, j, sub, powBE)
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: credit}
	}

	credit, judged := sub.Judge(shareDiff)
	if !judged {
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}
	}
	if m.onShare != nil {
		m.onShare(ctx, m.share(sub, credit))
	}
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: credit}
}

func (m *Manager) share(sub stratum.KaspaSubmission, credit float64) core.Share {
	return core.Share{
		Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
		UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
		Difficulty: credit, Solo: sub.Solo, At: time.Now(),
	}
}

// handleBlock 爆块提交（意图先落库 → 整块提交 → BlockSink 转 pending）。
// velkar 块 id = header hash（无 blob 链 id≠PoW 的两难）：意图 hash 直接是权威 id，
// 即使提交失败/崩溃分类器也能按链上 hash 归位。
func (m *Manager) handleBlock(ctx context.Context, j *velkarJob, sub stratum.KaspaSubmission, powBE *big.Int) {
	nonce := sub.Nonce
	work := j.work
	intentHash, err := velkarrpc.BlockHashOf(work, nonce)
	if err != nil {
		return
	}
	submit := func(ctx context.Context) (string, error) {
		return m.node.SubmitSolved(ctx, work, nonce) // 返回节点权威块 id（= intentHash）
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: intentHash,
		Finder: sub.Address, Worker: sub.Worker,
		Reward: j.reward, NetDiff: j.netDiff,
		Status: core.BlockPending, FoundAt: time.Now(), Solo: sub.Solo,
	}
	// rawBlockHex 对 velkar 无意义（整块由 gRPC 提交，非 hex）——传空，SubmitSolved 自带块。
	_ = m.onBlock(ctx, fb, "", submit)
}

// ---- 内部工具 ----

// shareDiffFromPow = Diff1 / powBE（float64）。pow 越小难度越高。
func shareDiffFromPow(powBE *big.Int) float64 {
	if powBE.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(velkarDiff1),
		new(big.Float).SetInt(powBE),
	).Float64()
	return q
}

// diffFromTargetBE 网络难度 = Diff1 / networkTargetBE。
func diffFromTargetBE(targetBE *big.Int) float64 {
	if targetBE == nil || targetBE.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(velkarDiff1),
		new(big.Float).SetInt(targetBE),
	).Float64()
	return q
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func validateWork(w *velkarrpc.VelkarWork) error {
	if len(w.PrePowHash) != 32 {
		return fmt.Errorf("PrePowHash 长度 %d ≠ 32", len(w.PrePowHash))
	}
	if len(w.BlockTargetLE) != 32 {
		return fmt.Errorf("BlockTargetLE 长度 %d ≠ 32", len(w.BlockTargetLE))
	}
	if w.BlockTargetBE == nil || w.BlockTargetBE.Sign() <= 0 {
		return fmt.Errorf("BlockTargetBE 缺失/非正")
	}
	if w.Block == nil || w.Block.Header == nil {
		return fmt.Errorf("Block/Header 缺失（提交要整块回填）")
	}
	if w.JobKey == "" {
		return fmt.Errorf("JobKey 缺失（换工去重依赖它）")
	}
	return nil
}
