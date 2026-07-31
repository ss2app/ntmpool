// Package cnjob 是 blob 系链（CryptoNote/RandomX 家族 + zoka 类自定义链）的作业管理器：
// 模板 → per-connection blob 物化 → 池端重算校验 → 组块提交 → 记账回调。
// 与 internal/jobmanager（bitcoin GBT 系）平行，同样实现 coininstance 的作业管线面。
//
// nonce 字段统一模型见 adapter.BlobWork 注释；本包只消费归一化字段，
// 链差异（blob 布局、target 编码、提交载荷）全部由适配器在模板里声明。
package cnjob

import (
	"bytes"
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

	conns connIDAllocator
}

// keepJobs 保留最近 N 个 job 供 share 归属。够大以覆盖「矿工网络延迟 + 池换 job」
// 的窗口：真实矿工经中转/跨境提交有 RTT，太小(原 4)会把在途 share 判 stale。
const keepJobs = 24

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

// SetConnIDSpace 声明连接 tag 的可用空间大小，启用「在线连接 tag 互不重复」的池化分配。
//
// 为什么需要：materialize 把 nonce 字段拆成【低 SearchLen 字节=矿工搜索区】+【高位=连接 tag】。
// tag 是矿工之间唯一的区分手段——tag 相同的两个连接会拿到逐字节相同的 blob，而锄头都从 nonce 0
// 起扫，于是它们算出完全相同的 hash 序列。share 去重是 per-connection 的，两边都会被正常接受、
// 正常计分，**但池实际覆盖的 nonce 空间只等于一台矿机**，爆块率不随算力增长。矿工侧毫无异样，
// 只会发现收益远低于预期。
//
// tag 很宽时（dragonx: NonceLen 8 - SearchLen 4 = 4 字节 = 2^32）自增序号撞不上；tag 只有 1 字节
// （BRVA: 4-3=1 → 256 个）时，按生日问题 20 个在线连接就有过半概率撞。故窄 tag 的币必须调本方法。
//
// space=0（默认）= 不限：走纯自增，行为与调用方原先的 connSeq 等价。
func (m *Manager) SetConnIDSpace(space uint32) { m.conns.setSpace(space) }

// AcquireConnectionID / ReleaseConnectionID 实现 stratum.CNConnectionIDAllocator。
// 空间满时返回 false，stratum 层会拒绝新连接（宁可拒连，也好过默默发重复工作）。
func (m *Manager) AcquireConnectionID() (uint32, bool) { return m.conns.acquire() }
func (m *Manager) ReleaseConnectionID(connID uint32)   { m.conns.release(connID) }

// connIDAllocator 分配在线唯一的连接 tag，断开即归还复用。
type connIDAllocator struct {
	mu     sync.Mutex
	space  uint32 // 0 = 不限（纯自增，不入 active）
	next   uint32
	active map[uint32]struct{}
}

func (a *connIDAllocator) setSpace(space uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.space = space
	if space > 0 && a.active == nil {
		a.active = map[uint32]struct{}{}
	}
}

func (a *connIDAllocator) acquire() (uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.space == 0 {
		a.next++
		return a.next, true
	}
	if a.active == nil {
		a.active = map[uint32]struct{}{}
	}
	if uint32(len(a.active)) >= a.space {
		return 0, false
	}
	for attempts := uint32(0); attempts < a.space; attempts++ {
		id := a.next % a.space
		a.next++
		if _, used := a.active[id]; used {
			continue
		}
		a.active[id] = struct{}{}
		return id, true
	}
	return 0, false
}

func (a *connIDAllocator) release(connID uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.space == 0 || a.active == nil {
		return
	}
	delete(a.active, connID)
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
	// 同一份可挖工作就不发新 job，避免矿工无谓换工（换工 = 矿工手上 job 过期 stale）。
	// 判定：适配器给了 JobKey 就按它（dragonx=height+prevhash，忽略 curtime 每秒抖动）；
	// 否则回退到整个 HashingBlob 逐字节比较（zoka 等链原行为）。
	if cur, ok := m.jobs[m.current]; ok && cur.height == tpl.Height {
		same := false
		if work.JobKey != "" {
			same = cur.work.JobKey == work.JobKey
		} else {
			same = string(cur.work.HashingBlob) == string(work.HashingBlob)
		}
		if same {
			m.mu.Unlock()
			return nil
		}
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
		JobID:       j.id,
		Blob:        hex.EncodeToString(blob),
		Target:      cnwork.EncodeTargetForDiff(difficulty, j.work.TargetCompactLE),
		Algo:        m.algo,
		Height:      j.height,
		SeedHash:    j.work.SeedHash,
		BrvaJobMode: j.work.BrvaJobMode,
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

	wireLen := work.WireNonceLen
	if wireLen == 0 {
		wireLen = work.NonceLen
	}
	var (
		search    uint64
		wireBytes []byte
	)
	if wireLen == work.NonceLen {
		nonce, nBytes, err := cnwork.ParseNonceLE(sub.NonceHex)
		if err != nil || nBytes > work.NonceLen || sub.ResultHex == "" {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		search = nonce & searchMask(work.SearchLen)
	} else {
		// 宽 wire nonce（dragonx 类）：矿工回显完整字段，池只滚低 NonceLen 字节
		wb, err := hex.DecodeString(strings.TrimSpace(sub.NonceHex))
		if err != nil || len(wb) != wireLen || sub.ResultHex == "" {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		wireBytes = wb
		search = cnwork.NonceFieldLE(wireBytes, 0, work.NonceLen) & searchMask(work.SearchLen)
	}

	// 只信矿工的搜索区；连接 tag 用池侧记录重建（防伪造，CRB/zoka 先例同款）
	candidate := materialize(work, sub.ConnID, search)
	fullNonce := cnwork.NonceFieldLE(candidate, work.NonceOffset, work.NonceLen)

	// 宽 wire nonce 回显校验：矿工必须原样带回池下发的整个 nonce 字段。
	// 回显被改 = 矿工实际 hash 的 blob 与池重建不同——与其困惑地 badpow 不如明确 malformed。
	if wireBytes != nil && !bytes.Equal(wireBytes, candidate[work.NonceOffset:work.NonceOffset+wireLen]) {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	key, err := seedKey(work.SeedHash)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	// 池端重算（共识 tripwire：矿工声称的 hash 必须与池端重算逐字节一致）。
	// 双段算法（rx/dragonx）：矿工上报内层 result，难度/命中用外层 pow；
	// 单段算法两者是同一个 hash。
	var hash, aux []byte
	if ts, isTwoStage := m.hsh.(hasher.TwoStageKeyedHasher); isTwoStage {
		result, pow, err := ts.HashKeyedTwoStage(key, candidate)
		if err != nil {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		if !strings.EqualFold(sub.ResultHex, hex.EncodeToString(result)) {
			return stratum.SubmitResult{Outcome: core.OutcomeBadPow}
		}
		hash, aux = pow, result
	} else {
		h, err := m.hsh.HashKeyed(key, candidate)
		if err != nil {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		if !strings.EqualFold(sub.ResultHex, hex.EncodeToString(h)) {
			return stratum.SubmitResult{Outcome: core.OutcomeBadPow}
		}
		hash = h
	}

	shareDiff := cnwork.ShareDiff(hash, work.HashBigEndian)
	// 双段双接受（miningcore DragonX 同款）：内层 rx 真实性已由重算证明，
	// 内外任一口径达标即计 share——兼容按内层 hash 过滤的 legacy 锄头。
	if aux != nil {
		if d := cnwork.ShareDiff(aux, work.HashBigEndian); d > shareDiff {
			shareDiff = d
		}
	}

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
		m.handleBlock(ctx, j, sub, candidate, fullNonce, hash, aux)
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
func (m *Manager) handleBlock(ctx context.Context, j *cnJob, sub stratum.CNSubmission, blob []byte, fullNonce uint64, hash, aux []byte) {
	sol := &adapter.BlobSolution{Work: j.work, Blob: blob, Nonce: fullNonce, Hash: hash, AuxHash: aux}
	submit := func(ctx context.Context) (string, error) {
		return m.node.SubmitBlob(ctx, sol) // 返回节点权威块 id
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	// 意图 hash = PoW hash（每个解唯一）；BlockSink 提交成功后换成节点权威 id。
	// PowIsBlockHash 链（dragonx）：反转即链上真块 hash，占位直接用显示序——
	// 即使提交失败/崩溃，分类器也能按链上 hash 归位。
	intentHash := hex.EncodeToString(hash)
	if j.work.PowIsBlockHash {
		intentHash = hex.EncodeToString(reverseBytes(hash))
	}
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: intentHash,
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

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
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
	if w.WireNonceLen != 0 {
		if w.WireNonceLen < w.NonceLen {
			return fmt.Errorf("WireNonceLen=%d < NonceLen=%d", w.WireNonceLen, w.NonceLen)
		}
		if w.NonceOffset+w.WireNonceLen > len(w.HashingBlob) {
			return fmt.Errorf("wire nonce 字段越界（offset=%d wire=%d blob=%d）", w.NonceOffset, w.WireNonceLen, len(w.HashingBlob))
		}
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
