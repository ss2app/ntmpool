// Package scashjob 实现 SCASH 的标准 Bitcoin Stratum V1 作业与 share 验证。
// 它只以 scashrx.Result.CM 判 share/block，R 仅随区块 solution 交给 scashrpc。
package scashjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/scashrpc"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/scashrx"
	"github.com/scashcc/ntmpool/internal/stratum"
)

const DifficultyScale = 65536.0

var poolTag = []byte("/NTMPool/")

type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	PayoutScript(ctx context.Context, address string) (string, error)
	SubmitSolution(ctx context.Context, sol *scashrpc.Solution) (string, error)
}

type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error
type AcceptedSink func(ctx context.Context, s core.Share)

type jobRules struct {
	epochDuration uint64
	minTime       int64
}

type Manager struct {
	coinID   string
	node     nodeIface
	pow      scashrx.Engine
	reg      *stratum.JobRegistry
	en2Size  int
	decimals int

	poolScript []byte
	jobSeq     atomic.Uint64
	broadcast  func()
	onBlock    BlockSink
	onShare    AcceptedSink

	mu        sync.RWMutex
	rules     map[string]jobRules
	ruleOrder []string
	now       func() time.Time
}

var _ stratum.ShareHandler = (*Manager)(nil)

func New(coinID string, node nodeIface, pow scashrx.Engine, reg *stratum.JobRegistry, en2Size, decimals int) *Manager {
	if decimals <= 0 {
		decimals = 8
	}
	return &Manager{
		coinID: coinID, node: node, pow: pow, reg: reg, en2Size: en2Size, decimals: decimals,
		rules: map[string]jobRules{}, now: time.Now,
	}
}

func (m *Manager) Registry() *stratum.JobRegistry { return m.reg }
func (m *Manager) ExtraNonce2Size() int           { return m.en2Size }

func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast, m.onBlock, m.onShare = broadcast, onBlock, onShare
}

func (m *Manager) Init(ctx context.Context, poolAddress string) error {
	scriptHex, err := m.node.PayoutScript(ctx, poolAddress)
	if err != nil {
		return err
	}
	m.poolScript, err = hex.DecodeString(scriptHex)
	if err != nil {
		return fmt.Errorf("SCASH 矿池地址 scriptPubKey 非 hex: %w", err)
	}
	return nil
}

func (m *Manager) Snapshot() (height uint64, netDiff float64, ok bool) {
	j, ok := m.reg.Current()
	if !ok {
		return 0, 0, false
	}
	return j.Height, j.NetDiff, true
}

func (m *Manager) Refresh(ctx context.Context, forceClean bool) error {
	bt, err := m.node.GetTemplate(ctx)
	if err != nil {
		return err
	}
	t, ok := bt.Raw.(*scashrpc.Template)
	if !ok {
		return fmt.Errorf("[%s] SCASH 模板 Raw 类型错误 %T", m.coinID, bt.Raw)
	}
	job, rules, err := m.buildJob(t, forceClean)
	if err != nil {
		return err
	}
	m.putRules(job.ID, rules)
	m.reg.Put(job)
	if m.broadcast != nil {
		m.broadcast()
	}
	return nil
}

func (m *Manager) putRules(id string, rules jobRules) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[id] = rules
	m.ruleOrder = append(m.ruleOrder, id)
	for len(m.ruleOrder) > 4 {
		old := m.ruleOrder[0]
		m.ruleOrder = m.ruleOrder[1:]
		delete(m.rules, old)
	}
}

func (m *Manager) getRules(id string) (jobRules, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rules[id]
	return r, ok
}

func (m *Manager) buildJob(t *scashrpc.Template, forceClean bool) (*stratum.Job, jobRules, error) {
	if len(m.poolScript) == 0 {
		return nil, jobRules{}, fmt.Errorf("[%s] 未初始化矿池地址脚本", m.coinID)
	}
	if t.RxEpochDuration == 0 {
		return nil, jobRules{}, fmt.Errorf("[%s] rx_epoch_duration 为零（fail closed）", m.coinID)
	}
	bits, err := parseHexU32(t.Bits)
	if err != nil {
		return nil, jobRules{}, fmt.Errorf("SCASH bits 非法 %q", t.Bits)
	}
	declaredTarget, err := btcwork.TargetFromHex(t.Target)
	if err != nil {
		return nil, jobRules{}, fmt.Errorf("SCASH target 非法: %w", err)
	}
	networkTarget, err := TargetFromBits(bits)
	if err != nil {
		return nil, jobRules{}, err
	}
	if declaredTarget.Cmp(networkTarget) != 0 {
		return nil, jobRules{}, fmt.Errorf("SCASH GBT target 与 nBits 不一致")
	}
	var witness []byte
	if t.DefaultWitnessCommitment != "" {
		witness, err = hex.DecodeString(t.DefaultWitnessCommitment)
		if err != nil {
			return nil, jobRules{}, fmt.Errorf("SCASH witness commitment 非 hex: %w", err)
		}
	}
	cb, err := btcwork.BuildCoinbase(t.Height, t.CoinbaseValue, m.poolScript, witness, poolTag, 4+m.en2Size)
	if err != nil {
		return nil, jobRules{}, err
	}

	otherTxids := make([][]byte, 0, len(t.Transactions))
	rawTxs := make([][]byte, 0, len(t.Transactions))
	for i, tx := range t.Transactions {
		idDisplay, err := hex.DecodeString(tx.TxID)
		if err != nil || len(idDisplay) != 32 {
			return nil, jobRules{}, fmt.Errorf("SCASH transactions[%d].txid 非法", i)
		}
		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, jobRules{}, fmt.Errorf("SCASH transactions[%d].data 非 hex", i)
		}
		otherTxids = append(otherTxids, btcwork.Reverse(idDisplay))
		rawTxs = append(rawTxs, raw)
	}

	id := strconv.FormatUint(m.jobSeq.Add(1), 16)
	job := &stratum.Job{
		ID: id, Height: t.Height, PrevHashBE: t.PreviousBlockHash,
		Coinbase: cb, MerkleBranch: btcwork.MerkleBranch(otherTxids), RawTxs: rawTxs,
		Version: t.Version, Bits: bits, NTime: uint32(t.CurTime), NetworkTarget: networkTarget,
		NetDiff: btcwork.TargetToDiff(networkTarget), RewardSat: t.CoinbaseValue, CleanJobs: forceClean,
	}
	return job, jobRules{epochDuration: t.RxEpochDuration, minTime: t.MinTime}, nil
}

// EpochKey 返回节点传给 RandomX cache 的 32B SHA256d 原始字节序。
func EpochKey(ntime uint32, duration uint64) ([32]byte, error) {
	var out [32]byte
	if duration == 0 {
		return out, fmt.Errorf("SCASH epoch duration 为零")
	}
	epoch := uint64(ntime) / duration
	seed := []byte("Scash/RandomX/Epoch/" + strconv.FormatUint(epoch, 10))
	h1 := sha256.Sum256(seed)
	return sha256.Sum256(h1[:]), nil
}

func EffectiveDifficulty(wire float64) (float64, error) {
	if wire <= 0 {
		return 0, fmt.Errorf("SCASH wire difficulty 必须大于零")
	}
	return wire / DifficultyScale, nil
}

func ShareTarget(wire float64) (*big.Int, error) {
	effective, err := EffectiveDifficulty(wire)
	if err != nil {
		return nil, err
	}
	return btcwork.DiffToTarget(effective), nil
}

// TargetFromBits 按 Bitcoin SetCompact 正数 256-bit 语义展开 nBits。network block
// 判断使用这里的原始 nBits target，绝不经过 SCASH 的 /65536 Stratum 归一化。
func TargetFromBits(bits uint32) (*big.Int, error) {
	size := uint(bits >> 24)
	word := bits & 0x007fffff
	if word == 0 || bits&0x00800000 != 0 {
		return nil, fmt.Errorf("SCASH nBits 非法: %08x", bits)
	}
	target := new(big.Int).SetUint64(uint64(word))
	if size <= 3 {
		target.Rsh(target, 8*(3-size))
	} else {
		target.Lsh(target, 8*(size-3))
	}
	if target.Sign() <= 0 || target.BitLen() > 256 {
		return nil, fmt.Errorf("SCASH nBits target 溢出: %08x", bits)
	}
	return target, nil
}

func (m *Manager) validNTime(job *stratum.Job, rules jobRules, ntime uint32) bool {
	lower := int64(job.NTime)
	if rules.minTime > lower {
		lower = rules.minTime
	}
	// Bitcoin 共识允许最多 adjusted time + 2h；池用本机当前时间作保守前置门禁，
	// 节点仍是最终权威。矿机只允许从 notify ntime 向前滚动。
	upper := m.now().Unix() + 2*60*60
	return int64(ntime) >= lower && int64(ntime) <= upper
}

func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.Submission) stratum.SubmitResult {
	job, ok := m.reg.Get(sub.JobID)
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	rules, ok := m.getRules(sub.JobID)
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	if len(sub.ExtraNonce2) != m.en2Size || !m.validNTime(job, rules, sub.NTime) {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	effective, err := EffectiveDifficulty(sub.RequiredDiff)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	shareTarget := btcwork.DiffToTarget(effective)

	cbTxid, err := job.Coinbase.TxID(sub.ExtraNonce1, sub.ExtraNonce2)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	root := btcwork.MerkleRootFromBranch(cbTxid, job.MerkleBranch)
	prevDisplay, err := hex.DecodeString(job.PrevHashBE)
	if err != nil || len(prevDisplay) != 32 {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	header80 := btcwork.SerializeHeader(sub.EffectiveVersion(job.Version), btcwork.Reverse(prevDisplay), root, sub.NTime, job.Bits, sub.Nonce)
	zeroed112 := make([]byte, scashrx.HeaderSize)
	copy(zeroed112, header80)

	key, err := EpochKey(sub.NTime, rules.epochDuration)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	pow, err := m.pow.Hash(key[:], zeroed112)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	// 共识边界：以下两个判断都只读取 CM，绝不读取 R 的数值难度。
	if btcwork.HashMeetsTarget(pow.CM[:], job.NetworkTarget) {
		m.recordShare(ctx, sub, effective)
		if err := m.handleBlock(ctx, job, sub, zeroed112, pow.R); err != nil {
			return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
		}
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: effective}
	}
	if !btcwork.HashMeetsTarget(pow.CM[:], shareTarget) {
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}
	}
	m.recordShare(ctx, sub, effective)
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: effective}
}

func (m *Manager) recordShare(ctx context.Context, sub stratum.Submission, effective float64) {
	if m.onShare == nil {
		return
	}
	m.onShare(ctx, core.Share{
		Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
		UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
		Difficulty: effective, Solo: sub.Solo, At: m.now(),
	})
}

func (m *Manager) handleBlock(ctx context.Context, job *stratum.Job, sub stratum.Submission, zeroed112 []byte, r [32]byte) error {
	coinbaseWitness, err := job.Coinbase.SerializeWitness(sub.ExtraNonce1, sub.ExtraNonce2)
	if err != nil {
		return err
	}
	solution := &scashrpc.Solution{
		ZeroedHeader: zeroed112, Nonce: sub.Nonce, R: r,
		CoinbaseWitness: coinbaseWitness, RawTxs: job.RawTxs,
	}
	prepared, err := scashrpc.BuildSolvedBlock(solution)
	if err != nil {
		return err
	}
	found := core.FoundBlock{
		Coin: m.coinID, Height: job.Height, Hash: prepared.ID,
		Finder: sub.Address, Worker: sub.Worker, Reward: unitsToString(job.RewardSat, m.decimals),
		NetDiff: job.NetDiff, Status: core.BlockPending, FoundAt: m.now(), Solo: sub.Solo,
	}
	submit := func(ctx context.Context) (string, error) {
		return m.node.SubmitSolution(ctx, solution)
	}
	if m.onBlock != nil {
		_ = m.onBlock(ctx, found, prepared.BlockHex, submit)
		return nil
	}
	_, _ = submit(ctx)
	return nil
}

func unitsToString(v int64, decimals int) string {
	unit := int64(1)
	for i := 0; i < decimals; i++ {
		unit *= 10
	}
	return fmt.Sprintf("%d.%0*d", v/unit, decimals, v%unit)
}

func parseHexU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 16, 32)
	return uint32(v), err
}
