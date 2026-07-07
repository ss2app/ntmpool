// Package cnjob 是 blob 系链（CryptoNote/RandomX 家族 + zoka 类自定义链）的作业管理器：
// 模板 → per-connection blob 物化 → 池端重算校验 → 组块提交 → 记账回调。
// 与 internal/jobmanager（bitcoin GBT 系）平行，同样实现 coininstance 的作业管线面。
//
// nonce 字段统一模型见 adapter.BlobWork 注释；本包只消费归一化字段，
// 链差异（blob 布局、target 编码、提交载荷）全部由适配器在模板里声明。
package cnjob

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// nodeIface 是 Manager 对节点的最小依赖（便于测试打桩）。
type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	// SubmitBlob 提交爆块解，返回链上权威块 hash（孤块比对基准）。
	SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (blockHash string, err error)
}

// SubmitFunc / BlockSink / AcceptedSink 会计层注入（与 jobmanager 同形）。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

// cnJob 一个模板级 job。
type cnJob struct {
	id       string
	work     *adapter.BlobWork
	height   uint64
	prevHash string
	reward   string // 十进制字符串（适配器已归一）
	netDiff  float64
}

// Manager 每币一个。实现 stratum.CNShareHandler。
type Manager struct {
	coinID string
	node   nodeIface
	hsh    hasher.KeyedHasher
	algo   string

	jobSeq    atomic.Uint64
	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu        sync.Mutex
	jobs      map[string]*cnJob
	order     []string
	current   string
	lastFetch time.Time
}

const keepJobs = 4

// templateRefresh 同高度模板重拉间隔。zoka 类链模板每次拉都带新 timestamp/template_id
// （blob 必变），不节流的话 2s 轮询 = 每 2s 推新 job 白白打断矿工（live 冒烟实测）。
// 15s 同时兼作 job 心跳（<60s 铁律，docs/04 §1）。
const templateRefresh = 15 * time.Second

var _ stratum.CNShareHandler = (*Manager)(nil)

func New(coinID, algo string, node nodeIface, hsh hasher.KeyedHasher) *Manager {
	return &Manager{
		coinID: coinID, algo: algo, node: node, hsh: hsh,
		jobs: map[string]*cnJob{},
	}
}

// SetCallbacks 注入广播与会计回调。
func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Refresh 拉模板 → 登记 job → 广播。blob 系没有 clean_jobs 概念：
// 矿工收到新 job 即整体换工。force=true（高度变化/首拉）立即拉；
// force=false 受 templateRefresh 节流（同高度不必每个轮询 tick 都换 job）。
func (m *Manager) Refresh(ctx context.Context, force bool) error {
	m.mu.Lock()
	if !force && !m.lastFetch.IsZero() && time.Since(m.lastFetch) < templateRefresh {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	tpl, err := m.node.GetTemplate(ctx)
	if err != nil {
		return err
	}
	work, ok := tpl.Raw.(*adapter.BlobWork)
	if !ok {
		return fmt.Errorf("[%s] 模板 Raw 非 BlobWork（适配器实现错误）", m.coinID)
	}
	if err := validateWork(work); err != nil {
		return fmt.Errorf("[%s] 模板非法: %w", m.coinID, err)
	}

	m.mu.Lock()
	m.lastFetch = time.Now()
	// blob 没变（同模板轮询）就不发新 job，避免矿工无谓换工
	if cur, ok := m.jobs[m.current]; ok &&
		cur.height == tpl.Height &&
		string(cur.work.HashingBlob) == string(work.HashingBlob) {
		m.mu.Unlock()
		return nil
	}
	id := strconv.FormatUint(m.jobSeq.Add(1), 16)
	j := &cnJob{
		id: id, work: work,
		height:   tpl.Height,
		prevHash: tpl.PrevHash,
		reward:   tpl.CoinbaseValue,
		netDiff:  diffFromTarget(work),
	}
	m.jobs[id] = j
	m.order = append(m.order, id)
	m.current = id
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
	if !found {
		return 0, 0, false
	}
	return j.height, j.netDiff, true
}

// ---- stratum.CNShareHandler ----

func (m *Manager) Algo() string { return m.algo }

func (m *Manager) LoginExtensions() []string {
	ext := []string{"algo", "keepalive"}
	m.mu.Lock()
	j, ok := m.jobs[m.current]
	m.mu.Unlock()
	if ok && j.work.Nicehash {
		ext = append(ext, "nicehash")
	}
	return ext
}

// ConnJob 物化 per-connection blob：清零搜索区 + 高位写连接 tag。
func (m *Manager) ConnJob(connID uint32, difficulty float64) (stratum.CNWireJob, bool) {
	m.mu.Lock()
	j, ok := m.jobs[m.current]
	m.mu.Unlock()
	if !ok {
		return stratum.CNWireJob{}, false
	}
	blob := materialize(j.work, connID, 0)
	return stratum.CNWireJob{
		JobID:    j.id,
		Blob:     hex.EncodeToString(blob),
		Target:   cnwork.EncodeTargetForDiff(difficulty, j.work.TargetCompactLE),
		Algo:     m.algo,
		Height:   j.height,
		SeedHash: j.work.SeedHash,
	}, true
}

// HandleSubmit 池端重算一条提交（badpow tripwire）→ 难度归属 → 命中检测 → 组块提交。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.CNSubmission) stratum.SubmitResult {
	m.mu.Lock()
	j, ok := m.jobs[sub.JobID]
	m.mu.Unlock()
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	work := j.work

	nonce, nBytes, err := cnwork.ParseNonceLE(sub.NonceHex)
	if err != nil || nBytes > work.NonceLen || sub.ResultHex == "" {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	// 只信矿工的搜索区；连接 tag 用池侧记录重建（防伪造，CRB/zoka 先例同款）
	search := nonce & searchMask(work.SearchLen)

	candidate := materialize(work, sub.ConnID, search)
	fullNonce := cnwork.NonceFieldLE(candidate, work.NonceOffset, work.NonceLen)

	key, err := seedKey(work.SeedHash)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	hash, err := m.hsh.HashKeyed(key, candidate)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	// 共识 tripwire：矿工声称的 hash 必须与池端重算逐字节一致
	if !strings.EqualFold(sub.ResultHex, hex.EncodeToString(hash)) {
		return stratum.SubmitResult{Outcome: core.OutcomeBadPow}
	}

	shareDiff := cnwork.ShareDiff(hash, work.HashBigEndian)

	// 命中全网目标 → 爆块（爆块 share 同样计入 PPLNS 权重，miningcore 同款语义）
	if cnwork.MeetsTarget(hash, work.NetworkTarget, work.HashBigEndian) {
		credit, ok := sub.Judge(shareDiff)
		if !ok {
			credit = shareDiff // 低难度端口挖出真块（regtest 常见）：按实际难度计权
		}
		if m.onShare != nil {
			m.onShare(ctx, core.Share{
				Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
				UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
				Difficulty: credit, Solo: sub.Solo, At: time.Now(),
			})
		}
		m.handleBlock(ctx, j, sub, candidate, fullNonce, hash)
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: credit}
	}

	credit, judged := sub.Judge(shareDiff)
	if !judged {
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}
	}
	if m.onShare != nil {
		m.onShare(ctx, core.Share{
			Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
			UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
			Difficulty: credit, Solo: sub.Solo, At: time.Now(),
		})
	}
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: credit}
}

// handleBlock 提交爆块。blob 链的权威块 hash 只有节点受理后才知道（zoka 由
// /mining/submit 返回；块 id ≠ PoW hash），所以「意图先落库」：先用 PoW hash 作
// 占位记 submitting，SubmitBlob 拿到真 id 后由 BlockSink 改写 hash 并转 pending
// （M4：闭合「提交成功→落账」的崩溃窗口，docs/05 场景B）。
func (m *Manager) handleBlock(ctx context.Context, j *cnJob, sub stratum.CNSubmission, blob []byte, fullNonce uint64, hash []byte) {
	sol := &adapter.BlobSolution{Work: j.work, Blob: blob, Nonce: fullNonce, Hash: hash}
	submit := func(ctx context.Context) (string, error) {
		return m.node.SubmitBlob(ctx, sol) // 返回节点权威块 id
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	// 意图 hash = PoW hash（每个解唯一）；BlockSink 提交成功后换成节点权威 id。
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: hex.EncodeToString(hash),
		Finder: sub.Address, Worker: sub.Worker,
		Reward: j.reward, NetDiff: j.netDiff,
		Status: core.BlockPending, FoundAt: time.Now(), Solo: sub.Solo,
	}
	_ = m.onBlock(ctx, fb, hex.EncodeToString(blob), submit)
}

// ---- 内部工具 ----

// materialize 物化 blob：nonce 字段 = 搜索区（低 SearchLen 字节，LE）+ 连接 tag（高位）。
func materialize(w *adapter.BlobWork, connID uint32, search uint64) []byte {
	blob := make([]byte, len(w.HashingBlob))
	copy(blob, w.HashingBlob)
	tagBytes := w.NonceLen - w.SearchLen
	full := search & searchMask(w.SearchLen)
	if tagBytes > 0 {
		tag := uint64(connID) & searchMask(tagBytes)
		full |= tag << (8 * uint(w.SearchLen))
	}
	cnwork.PutNonceLE(blob, w.NonceOffset, w.NonceLen, full)
	return blob
}

func searchMask(nbytes int) uint64 {
	if nbytes >= 8 {
		return ^uint64(0)
	}
	return (uint64(1) << (8 * uint(nbytes))) - 1
}

func seedKey(seedHash string) ([]byte, error) {
	if seedHash == "" {
		return nil, nil
	}
	return hex.DecodeString(seedHash)
}

func diffFromTarget(w *adapter.BlobWork) float64 {
	if w.NetworkTarget == nil || w.NetworkTarget.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(cnwork.Diff1),
		new(big.Float).SetInt(w.NetworkTarget),
	).Float64()
	return q
}

func validateWork(w *adapter.BlobWork) error {
	if w.NonceLen <= 0 || w.NonceLen > 8 {
		return fmt.Errorf("NonceLen=%d 非法", w.NonceLen)
	}
	if w.SearchLen <= 0 || w.SearchLen > w.NonceLen {
		return fmt.Errorf("SearchLen=%d 非法（NonceLen=%d）", w.SearchLen, w.NonceLen)
	}
	if w.NonceOffset < 0 || w.NonceOffset+w.NonceLen > len(w.HashingBlob) {
		return fmt.Errorf("nonce 字段越界（offset=%d len=%d blob=%d）", w.NonceOffset, w.NonceLen, len(w.HashingBlob))
	}
	if w.NetworkTarget == nil || w.NetworkTarget.Sign() <= 0 {
		return fmt.Errorf("NetworkTarget 缺失")
	}
	// RandomX 家族 seed_hash 必须恰好 64 hex（docs/04 §2：缺失/长度不对锄头直接拒）
	if strings.HasPrefix(w.Algo, "rx/") {
		if len(w.SeedHash) != 64 {
			return fmt.Errorf("rx 系 seed_hash 必须 64 hex（got %d）", len(w.SeedHash))
		}
		if _, err := hex.DecodeString(w.SeedHash); err != nil {
			return fmt.Errorf("seed_hash 非 hex: %w", err)
		}
	}
	return nil
}
