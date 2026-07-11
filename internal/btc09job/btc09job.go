// Package btc09job 是 Bitcoin 09 (09C) 的作业管理器：88 字节 header 模板 →
// per-connection nonce 窗口 → 池端 Argon2id 重算校验 → 组块提交 → 记账回调。
// 与 cnjob（blob 系）/jobmanager（bitcoin GBT 系）平行，第三种作业管线。
//
// 与 cnjob 的三点差异（也是不复用它的原因）：
//  1. 难度标尺 = 链的 maxTarget（bits 0x1f00ffff，diff1≈65537 hash/share），
//     不是 cnwork 的 2^256-1 —— 保住矿工现有 sub-1 难度语义（startdiff 0.05）。
//  2. 矿工 submit 是数字 nonce（uint64），hash 字段可选：带了就做 badpow
//     tripwire 对拍，没带也行（池端重算才是权威）。
//  3. nonce 窗口 = connID<<44（44-bit 私有搜索区，老池同款），不是字节分片。
package btc09job

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// nonceWindowBits 每连接私有 nonce 窗口位宽（2^44 ≈ 1.7e13 个 nonce，64 MiB
// Argon2id 矿工取之不尽）；connID 落在高位，窗口天然不重叠（老池同款）。
const nonceWindowBits = 44

// nodeIface Manager 对节点的最小依赖（便于测试打桩）。
type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (blockHash string, err error)
}

// SubmitFunc / BlockSink / AcceptedSink 会计层注入（与 cnjob/jobmanager 同形）。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

type job struct {
	id       string
	work     *adapter.BlobWork
	height   uint64
	prevHash string
	reward   string
	netDiff  float64
}

// Manager 每币一个。实现 stratum.Btc09ShareHandler。
type Manager struct {
	coinID    string
	node      nodeIface
	hsh       hasher.Hasher
	maxTarget *big.Int // 难度标尺：share_target = maxTarget / diff

	jobSeq    atomic.Uint64
	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu        sync.Mutex
	jobs      map[string]*job
	order     []string
	current   string
	lastFetch time.Time
}

const keepJobs = 24
const templateRefresh = 15 * time.Second

var _ stratum.Btc09ShareHandler = (*Manager)(nil)

// max256 全 1 的 256-bit 上界（share target 钳制，防超小难度溢出 64-hex 编码）。
var max256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

func New(coinID string, node nodeIface, hsh hasher.Hasher, maxTargetBits uint32) *Manager {
	return &Manager{
		coinID: coinID, node: node, hsh: hsh,
		maxTarget: CompactToTarget(maxTargetBits),
		jobs:      map[string]*job{},
	}
}

// SetCallbacks 注入广播与会计回调。
func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Refresh 拉模板 → 登记 job → 广播。JobKey（=template_id）不变即同一份工作，
// 不打断矿工。
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
	if cur, ok := m.jobs[m.current]; ok && cur.height == tpl.Height && cur.work.JobKey == work.JobKey {
		m.mu.Unlock()
		return nil
	}
	id := strconv.FormatUint(m.jobSeq.Add(1), 16)
	j := &job{
		id: id, work: work,
		height:   tpl.Height,
		prevHash: tpl.PrevHash,
		reward:   tpl.CoinbaseValue,
		netDiff:  m.diffOfTarget(work.NetworkTarget),
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

// ---- stratum.Btc09ShareHandler ----

// ValidateAddress 校验 09C 地址（base58check version 0x09），登录门禁。
func (m *Manager) ValidateAddress(addr string) error { return ValidateAddress(addr) }

// ConnJob 为一条连接物化当前 job：share target 按难度编码 + 私有 nonce 窗口。
func (m *Manager) ConnJob(connID uint32, difficulty float64) (stratum.Btc09WireJob, bool) {
	m.mu.Lock()
	j, ok := m.jobs[m.current]
	m.mu.Unlock()
	if !ok {
		return stratum.Btc09WireJob{}, false
	}
	base := uint64(connID) << nonceWindowBits
	end := base | (uint64(1)<<nonceWindowBits - 1)
	headerHex := hex.EncodeToString(j.work.HashingBlob)
	return stratum.Btc09WireJob{
		JobID:         j.id,
		Height:        j.height,
		HeaderBase:    headerHex,
		Header:        headerHex,
		Target:        hex32(m.shareTargetFor(difficulty)),
		NetworkTarget: hex32(j.work.NetworkTarget),
		Difficulty:    difficulty,
		NonceStart:    base,
		NonceEnd:      end,
	}, true
}

// HandleSubmit 池端重算一条提交 → 难度归属 → 命中检测 → 组块提交。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.Btc09Submission) stratum.SubmitResult {
	m.mu.Lock()
	j, ok := m.jobs[sub.JobID]
	m.mu.Unlock()
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	work := j.work

	// nonce 必须落在该连接的私有窗口（高位 = connID）——防伪造/跨连接重放。
	if sub.Nonce>>nonceWindowBits != uint64(sub.ConnID) {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	// 物化候选 header：模板 + 矿工 nonce（LE @ offset 80）。
	candidate := make([]byte, len(work.HashingBlob))
	copy(candidate, work.HashingBlob)
	putNonceLE(candidate, work.NonceOffset, work.NonceLen, sub.Nonce)

	// 池端重算（与节点 AcceptBlock 同一套 Argon2id，零共识分歧）。
	hash, err := m.hsh.Hash(candidate)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	// 矿工带了声明 hash 就做共识 tripwire 对拍（老池协议里该字段可选）。
	if sub.HashHex != "" && !strings.EqualFold(sub.HashHex, hex.EncodeToString(hash)) {
		return stratum.SubmitResult{Outcome: core.OutcomeBadPow}
	}

	hashVal := new(big.Int).SetBytes(hash) // 09C：大端语义
	shareDiff := m.diffOfHash(hashVal)

	// 命中全网目标 → 爆块（爆块 share 同样计入权重）。
	if hashVal.Cmp(work.NetworkTarget) <= 0 {
		credit, ok := sub.Judge(shareDiff)
		if !ok {
			credit = shareDiff
		}
		if m.onShare != nil {
			m.onShare(ctx, core.Share{
				Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
				UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
				Difficulty: credit, Solo: sub.Solo, At: time.Now(),
			})
		}
		m.handleBlock(ctx, j, sub, candidate, hash)
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

// handleBlock 组块提交。09C 的链上块 id = SHA256d(88B header)——本地可算，
// 意图落库直接用链上真 id（即使提交失败/崩溃，分类器也能按链上 hash 归位）。
func (m *Manager) handleBlock(ctx context.Context, j *job, sub stratum.Btc09Submission, candidate, hash []byte) {
	sol := &adapter.BlobSolution{Work: j.work, Blob: candidate, Nonce: sub.Nonce, Hash: hash}
	submit := func(ctx context.Context) (string, error) {
		return m.node.SubmitBlob(ctx, sol)
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return
	}
	blockID := sha256d(candidate)
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: hex.EncodeToString(blockID),
		Finder: sub.Address, Worker: sub.Worker,
		Reward: j.reward, NetDiff: j.netDiff,
		Status: core.BlockPending, FoundAt: time.Now(), Solo: sub.Solo,
	}
	_ = m.onBlock(ctx, fb, hex.EncodeToString(candidate), submit)
}

// ---- 难度/目标（09C 标尺：diff = maxTarget / target）----

// shareTargetFor share 难度 → 256-bit 目标（有理数除法支持 sub-1 难度，老池同款）。
func (m *Manager) shareTargetFor(d float64) *big.Int {
	if d <= 0 {
		d = 1
	}
	num := new(big.Int).Mul(m.maxTarget, big.NewInt(1_000_000))
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

func (m *Manager) diffOfHash(hashVal *big.Int) float64 {
	if hashVal.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(m.maxTarget),
		new(big.Float).SetInt(hashVal),
	).Float64()
	return q
}

func (m *Manager) diffOfTarget(target *big.Int) float64 {
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(m.maxTarget),
		new(big.Float).SetInt(target),
	).Float64()
	return q
}

// CompactToTarget bits（bitcoin compact 编码）→ 256-bit 目标。
func CompactToTarget(bits uint32) *big.Int {
	mant := big.NewInt(int64(bits & 0x007fffff))
	exp := int(bits >> 24)
	if exp <= 3 {
		return mant.Rsh(mant, uint(8*(3-exp)))
	}
	return mant.Lsh(mant, uint(8*(exp-3)))
}

func validateWork(w *adapter.BlobWork) error {
	if len(w.HashingBlob) != 88 {
		return fmt.Errorf("header 非 88 字节 (%d)", len(w.HashingBlob))
	}
	if w.NonceOffset != 80 || w.NonceLen != 8 {
		return fmt.Errorf("nonce 字段非 [80:88]（offset=%d len=%d）", w.NonceOffset, w.NonceLen)
	}
	if w.NetworkTarget == nil || w.NetworkTarget.Sign() <= 0 {
		return errors.New("NetworkTarget 缺失")
	}
	if w.JobKey == "" {
		return errors.New("JobKey（template_id）缺失")
	}
	return nil
}

func putNonceLE(blob []byte, offset, n int, v uint64) {
	for i := 0; i < n; i++ {
		blob[offset+i] = byte(v >> (8 * uint(i)))
	}
}

func sha256d(b []byte) []byte {
	h1 := sha256.Sum256(b)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// hex32 渲染 256-bit 目标为固定 64-hex 大端串。
func hex32(t *big.Int) string {
	b := t.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return hex.EncodeToString(out)
}

// ---- 09C 地址（base58check version 0x09）----

const addrVersion = 0x09

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ValidateAddress 校验 09C 地址格式与校验和（与节点 core.DecodeAddress 同规则）。
func ValidateAddress(s string) error {
	if s == "" {
		return errors.New("empty address")
	}
	x := new(big.Int)
	radix := big.NewInt(58)
	for _, c := range s {
		i := strings.IndexRune(b58Alphabet, c)
		if i < 0 {
			return fmt.Errorf("bad base58 char %q", c)
		}
		x.Mul(x, radix)
		x.Add(x, big.NewInt(int64(i)))
	}
	raw := x.Bytes()
	for _, c := range s {
		if c != '1' {
			break
		}
		raw = append([]byte{0}, raw...)
	}
	if len(raw) != 25 || raw[0] != addrVersion {
		return errors.New("bad address length or version")
	}
	payload, chk := raw[:21], raw[21:]
	want := sha256.Sum256(payload)
	want = sha256.Sum256(want[:])
	if !bytes.Equal(chk, want[:4]) {
		return errors.New("bad address checksum")
	}
	return nil
}
