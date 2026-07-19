// Package domjob 实现 DOM 的 work→Stratum job→池端 RandomX 重算→节点提交链路。
// DOM 的 224B preimage 完全由节点构造；矿工只搜索 offset 216 的 8B nonce。
package domjob

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter/domrpc"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

const (
	extraNonce1Len = 3
	counterBits    = 40
	maxConnID      = uint32((1 << 24) - 1)
	keepJobs       = 24
)

type nodeIface interface {
	GetWork(ctx context.Context) (*domrpc.Work, error)
	SubmitWork(ctx context.Context, jobID, nonceHex string) (*domrpc.SubmitResult, error)
}

type keyPrewarmer interface {
	PrewarmKey(key []byte) error
}

type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

type job struct {
	work    *domrpc.Work
	netDiff float64
}

// Manager 实现现有 CN login/submit 方言的 handler，但下发 DOM 特有 job 字段。
type Manager struct {
	coinID string
	algo   string
	node   nodeIface
	hsh    hasher.KeyedHasher

	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu      sync.Mutex
	jobs    map[string]*job
	order   []string
	current string
	en1     *extraNonceAllocator
}

var (
	_ stratum.CNShareHandler          = (*Manager)(nil)
	_ stratum.CNJobCleanAware         = (*Manager)(nil)
	_ stratum.CNConnectionIDAllocator = (*Manager)(nil)
)

func New(coinID, algo string, node nodeIface, hsh hasher.KeyedHasher) *Manager {
	return &Manager{
		coinID: coinID, algo: algo, node: node, hsh: hsh,
		jobs: map[string]*job{}, en1: newExtraNonceAllocator(),
	}
}

func (m *Manager) AcquireConnectionID() (uint32, bool) { return m.en1.acquire() }
func (m *Manager) ReleaseConnectionID(connID uint32)   { m.en1.release(connID) }

func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Refresh 每次都消费 ntm_get_work；Client 自身保证请求间隔 >=200ms。只有节点
// job_id 改变才登记并广播新活，同 job_id 的轮询响应不会打断矿工。
func (m *Manager) Refresh(ctx context.Context, _ bool) error {
	w, err := m.node.GetWork(ctx)
	if err != nil {
		return err
	}
	if err := validateWork(w); err != nil {
		return fmt.Errorf("[%s] DOM work 非法: %w", m.coinID, err)
	}
	// 当前 seed 与 next seed 都提前建 VM/cache。接口是可选能力；测试假哈希器、
	// 非 RandomX 实现不需要伪装支持。
	if p, ok := m.hsh.(keyPrewarmer); ok {
		if err := prewarmHex(p, w.SeedHash); err != nil {
			return fmt.Errorf("[%s] DOM seed 预热失败: %w", m.coinID, err)
		}
		if w.NextSeedHash != "" {
			if err := prewarmHex(p, w.NextSeedHash); err != nil {
				return fmt.Errorf("[%s] DOM next_seed 预热失败: %w", m.coinID, err)
			}
		}
	}

	m.mu.Lock()
	if m.current == w.JobID {
		m.mu.Unlock()
		return nil
	}
	j := &job{work: cloneWork(w), netDiff: diffFromTarget(w.NetworkTarget)}
	m.jobs[w.JobID] = j
	m.order = append(m.order, w.JobID)
	m.current = w.JobID
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

func (m *Manager) Snapshot() (height uint64, netDiff float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[m.current]
	if !ok {
		return 0, 0, false
	}
	return j.work.Height, j.netDiff, true
}

func (m *Manager) Algo() string { return m.algo }

func (m *Manager) LoginExtensions() []string {
	return []string{"algo", "keepalive", "dom-extranonce1"}
}

func (m *Manager) ConnJob(connID uint32, difficulty float64) (stratum.CNWireJob, bool) {
	return m.ConnJobWithClean(connID, difficulty, true)
}

// ConnJobWithClean 让复用的 CN 方言区分「节点换活」与「只改 vardiff target」。
// 新活 clean_jobs=true；vardiff 调档仍沿用同一 job/counter，避免重找重复 share。
func (m *Manager) ConnJobWithClean(connID uint32, difficulty float64, clean bool) (stratum.CNWireJob, bool) {
	en1, ok := extraNonce1ForConn(connID)
	if !ok {
		return stratum.CNWireJob{}, false
	}
	m.mu.Lock()
	j, found := m.jobs[m.current]
	m.mu.Unlock()
	if !found {
		return stratum.CNWireJob{}, false
	}
	cleanCopy := clean
	return stratum.CNWireJob{
		JobID:       j.work.JobID,
		Algo:        m.algo,
		Height:      j.work.Height,
		SeedHash:    j.work.SeedHash,
		Preimage:    hex.EncodeToString(j.work.Preimage),
		ShareTarget: cnwork.EncodeTargetHex256BE(cnwork.TargetFromDiff(difficulty)),
		CleanJobs:   &cleanCopy,
		ExtraNonce1: hex.EncodeToString(en1),
	}, true
}

// HandleSubmit 对 raw nonce 做连接分片校验，把其原始字节写回 offset 216，随后用
// stock RandomX(seed_hash, preimage) 重算。result 可省略；若提供则逐字节对拍并把
// 失配计为 badpow。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.CNSubmission) stratum.SubmitResult {
	m.mu.Lock()
	j, ok := m.jobs[sub.JobID]
	m.mu.Unlock()
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	nonceBytes, err := decodeNonce(sub.NonceHex)
	if err != nil || !nonceBelongsToConn(nonceBytes, sub.ConnID) {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	preimage, err := materializeNonce(j.work.Preimage, nonceBytes)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	seed, _ := hex.DecodeString(j.work.SeedHash) // validateWork 已钉 32B hex
	hash, err := m.hsh.HashKeyed(seed, preimage)
	if err != nil || len(hash) != 32 {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	if sub.ResultHex != "" {
		claimed := strings.TrimSpace(sub.ResultHex)
		if len(claimed) != 64 {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		if _, err := hex.DecodeString(claimed); err != nil {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		if !strings.EqualFold(claimed, hex.EncodeToString(hash)) {
			return stratum.SubmitResult{Outcome: core.OutcomeBadPow}
		}
	}

	shareDiff := cnwork.ShareDiff(hash, true)
	if meetsTarget(hash, j.work.NetworkTarget) {
		credit, judged := sub.Judge(shareDiff)
		if !judged {
			credit = shareDiff
		}
		m.acceptShare(ctx, sub, credit)
		m.handleBlock(ctx, j, sub, preimage, nonceBytes, hash)
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: credit}
	}
	credit, judged := sub.Judge(shareDiff)
	if !judged {
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}
	}
	m.acceptShare(ctx, sub, credit)
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: credit}
}

func (m *Manager) acceptShare(ctx context.Context, sub stratum.CNSubmission, credit float64) {
	if m.onShare == nil {
		return
	}
	m.onShare(ctx, core.Share{
		Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
		UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
		Difficulty: credit, Solo: sub.Solo, At: time.Now(),
	})
}

func (m *Manager) handleBlock(ctx context.Context, j *job, sub stratum.CNSubmission, preimage, nonce, hash []byte) {
	nonceHex := hex.EncodeToString(nonce)
	submit := func(ctx context.Context) (string, error) {
		res, err := m.node.SubmitWork(ctx, j.work.JobID, nonceHex)
		if err != nil {
			return "", err
		}
		if !res.Accepted {
			reason := "rejected"
			if res.Error != nil && *res.Error != "" {
				reason = *res.Error
			}
			return "", fmt.Errorf("ntm_submit_work: %s", reason)
		}
		if len(res.BlockHash) != 64 {
			return "", fmt.Errorf("ntm_submit_work accepted 但 block_hash 非 32B")
		}
		return res.BlockHash, nil
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.work.Height,
		Hash: hex.EncodeToString(hash), Finder: sub.Address, Worker: sub.Worker,
		Reward: "", NetDiff: j.netDiff, Status: core.BlockPending,
		FoundAt: time.Now(), Solo: sub.Solo,
	}
	_ = m.onBlock(ctx, fb, hex.EncodeToString(preimage), submit)
}

func validateWork(w *domrpc.Work) error {
	if w == nil {
		return fmt.Errorf("nil work")
	}
	if len(w.JobID) != 16 {
		return fmt.Errorf("job_id 长度 %d", len(w.JobID))
	}
	if len(w.Preimage) != domrpc.PreimageLen {
		return fmt.Errorf("preimage 长度 %d", len(w.Preimage))
	}
	for _, v := range w.Preimage[domrpc.NonceOffset : domrpc.NonceOffset+domrpc.NonceLen] {
		if v != 0 {
			return fmt.Errorf("preimage nonce 未置零")
		}
	}
	if len(w.SeedHash) != 64 {
		return fmt.Errorf("seed_hash 长度 %d", len(w.SeedHash))
	}
	if _, err := hex.DecodeString(w.SeedHash); err != nil {
		return fmt.Errorf("seed_hash 非 hex")
	}
	if w.NetworkTarget == nil || w.NetworkTarget.Sign() <= 0 || w.NetworkTarget.BitLen() > 256 {
		return fmt.Errorf("network target 非法")
	}
	return nil
}

func cloneWork(w *domrpc.Work) *domrpc.Work {
	c := *w
	c.Preimage = append([]byte(nil), w.Preimage...)
	c.NetworkTarget = new(big.Int).Set(w.NetworkTarget)
	return &c
}

func prewarmHex(p keyPrewarmer, seedHex string) error {
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != 32 {
		return fmt.Errorf("seed 非 32B hex")
	}
	return p.PrewarmKey(seed)
}

func extraNonce1ForConn(connID uint32) ([]byte, bool) {
	if connID == 0 || connID > maxConnID {
		return nil, false
	}
	en1 := [extraNonce1Len]byte{byte(connID >> 16), byte(connID >> 8), byte(connID)}
	return en1[:], true
}

type extraNonceAllocator struct {
	mu     sync.Mutex
	next   uint32
	active map[uint32]struct{}
}

func newExtraNonceAllocator() *extraNonceAllocator {
	return &extraNonceAllocator{active: map[uint32]struct{}{}}
}

func (a *extraNonceAllocator) acquire() (uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for attempts := uint32(0); attempts < maxConnID; attempts++ {
		a.next++
		if a.next == 0 || a.next > maxConnID {
			a.next = 1
		}
		if _, used := a.active[a.next]; used {
			continue
		}
		a.active[a.next] = struct{}{}
		return a.next, true
	}
	return 0, false
}

func (a *extraNonceAllocator) release(connID uint32) {
	if connID == 0 || connID > maxConnID {
		return
	}
	a.mu.Lock()
	delete(a.active, connID)
	a.mu.Unlock()
}

func decodeNonce(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) != domrpc.NonceLen*2 {
		return nil, fmt.Errorf("nonce 必须是 16 hex")
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != domrpc.NonceLen {
		return nil, fmt.Errorf("nonce 非 8B hex")
	}
	return b, nil
}

func nonceBelongsToConn(nonce []byte, connID uint32) bool {
	if len(nonce) != domrpc.NonceLen || connID == 0 || connID > maxConnID {
		return false
	}
	n := binary.LittleEndian.Uint64(nonce)
	return uint32(n>>counterBits) == connID
}

func materializeNonce(zeroedPreimage, nonce []byte) ([]byte, error) {
	if len(zeroedPreimage) != domrpc.PreimageLen || len(nonce) != domrpc.NonceLen {
		return nil, fmt.Errorf("DOM preimage/nonce 长度非法")
	}
	out := append([]byte(nil), zeroedPreimage...)
	copy(out[domrpc.NonceOffset:domrpc.NonceOffset+domrpc.NonceLen], nonce)
	return out, nil
}

func meetsTarget(hash []byte, target *big.Int) bool {
	return cnwork.MeetsTarget(hash, target, true)
}

func diffFromTarget(target *big.Int) float64 {
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	d, _ := new(big.Float).Quo(
		new(big.Float).SetInt(cnwork.Diff1),
		new(big.Float).SetInt(target),
	).Float64()
	return d
}
