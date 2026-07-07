package jobmanager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// nodeIface 是 JobManager 对节点的最小依赖（便于测试打桩）。
type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	SubmitBlock(ctx context.Context, raw any) error
	PayoutScript(ctx context.Context, address string) (string, error)
}

// SubmitFunc 由作业管理器提供：把已组装的块交给节点，返回节点认可的权威块 hash。
// bitcoin 系块 hash 在提交前已确定（返回原 hash）；blob 系由节点受理后才给出。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)

// BlockSink 会计层注入：池找到块 → 先记意图(submitting) → 调 submit 交节点 →
// 成功则 submitting→pending（必要时改用权威 hash），失败留 submitting 交恢复扫描
// （docs/05 场景B「意图先落库，动作后执行」）。
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit SubmitFunc) error

// AcceptedSink 会计层注入：一条 accepted share 记权。
type AcceptedSink func(ctx context.Context, s core.Share)

// JobManager 每币一个。实现 stratum.ShareHandler。
type JobManager struct {
	coinID   string
	node     nodeIface
	hsh      hasher.Hasher
	reg      *stratum.JobRegistry
	poolTag  []byte
	en2Size  int
	decimals int

	poolScript []byte // 矿池地址 scriptPubKey（启动时取一次）

	jobSeq    atomic.Uint64
	broadcast func() // 通知所有连接推新 job（clean）
	onBlock   BlockSink
	onShare   AcceptedSink

	mu           sync.Mutex
	lastTemplate *gbtTemplate
	lastRaw      json.RawMessage
}

var _ stratum.ShareHandler = (*JobManager)(nil)

func New(coinID string, node nodeIface, hsh hasher.Hasher, reg *stratum.JobRegistry, en2Size, decimals int) *JobManager {
	if decimals <= 0 {
		decimals = 8
	}
	return &JobManager{
		coinID:   coinID,
		node:     node,
		hsh:      hsh,
		reg:      reg,
		poolTag:  []byte("/NTMPool/"),
		en2Size:  en2Size,
		decimals: decimals,
	}
}

// satToStr 聪 → 十进制币字符串（整数运算不过浮点）。
func satToStr(sat int64, decimals int) string {
	u := int64(1)
	for i := 0; i < decimals; i++ {
		u *= 10
	}
	return fmt.Sprintf("%d.%0*d", sat/u, decimals, sat%u)
}

// SetCallbacks 注入广播与会计回调。
func (m *JobManager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Init 启动时取矿池地址脚本（组 coinbase 用）。
func (m *JobManager) Init(ctx context.Context, poolAddress string) error {
	scriptHex, err := m.node.PayoutScript(ctx, poolAddress)
	if err != nil {
		return err
	}
	m.poolScript, err = hex.DecodeString(scriptHex)
	if err != nil {
		return fmt.Errorf("矿池地址脚本非 hex: %w", err)
	}
	return nil
}

// ---- stratum.ShareHandler ----

func (m *JobManager) Registry() *stratum.JobRegistry { return m.reg }
func (m *JobManager) ExtraNonce2Size() int           { return m.en2Size }

// Snapshot 当前 job 的链上视图（coininstance 作业管线公共面）。
func (m *JobManager) Snapshot() (height uint64, netDiff float64, ok bool) {
	j, found := m.reg.Current()
	if !found {
		return 0, 0, false
	}
	return j.Height, j.NetDiff, true
}

// Refresh 拉模板 → 构造 job → 登记 → 广播。tip 变化或定时兜底时调用。
// forceClean=true（新块）时 job 的 clean_jobs 置真。
func (m *JobManager) Refresh(ctx context.Context, forceClean bool) error {
	tpl, err := m.node.GetTemplate(ctx)
	if err != nil {
		return err
	}
	raw, ok := tpl.Raw.(json.RawMessage)
	if !ok {
		// 有的适配器可能存 []byte
		if b, ok2 := tpl.Raw.([]byte); ok2 {
			raw = json.RawMessage(b)
		} else {
			return fmt.Errorf("[%s] 模板 Raw 非 JSON", m.coinID)
		}
	}
	g, err := parseGBT(raw)
	if err != nil {
		return err
	}

	job, err := m.buildJob(g, forceClean)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.lastTemplate = g
	m.lastRaw = raw
	m.mu.Unlock()

	m.reg.Put(job)
	if m.broadcast != nil {
		m.broadcast()
	}
	return nil
}

// buildJob 从 GBT 构造一个可挖 job（M1：空块，coinbase 只 claim subsidy——pitfall C5）。
func (m *JobManager) buildJob(g *gbtTemplate, forceClean bool) (*stratum.Job, error) {
	if len(m.poolScript) == 0 {
		return nil, fmt.Errorf("[%s] 未初始化矿池地址脚本", m.coinID)
	}
	bits, err := parseBits(g.Bits)
	if err != nil {
		return nil, err
	}
	target, err := btcwork.TargetFromHex(g.Target)
	if err != nil {
		return nil, err
	}
	var wc []byte
	if g.WitnessCommitment != "" {
		if wc, err = hex.DecodeString(g.WitnessCommitment); err != nil {
			return nil, fmt.Errorf("witness commitment 非 hex: %w", err)
		}
	}
	// M1 空块：coinbase 值 = coinbasevalue（不含任何 mempool tx，无 fee 可加）
	cb, err := btcwork.BuildCoinbase(g.Height, g.CoinbaseValue, m.poolScript, wc, m.poolTag, 4+m.en2Size)
	if err != nil {
		return nil, err
	}
	ntime := g.CurTime
	if ntime == 0 {
		ntime = time.Now().Unix()
	}
	id := strconv.FormatUint(m.jobSeq.Add(1), 16)
	return &stratum.Job{
		ID:            id,
		Height:        g.Height,
		PrevHashBE:    g.PreviousBlockHash,
		Coinbase:      cb,
		MerkleBranch:  nil, // 空块
		RawTxs:        nil,
		Version:       g.Version,
		Bits:          bits,
		NTime:         uint32(ntime),
		NetworkTarget: target,
		NetDiff:       btcwork.TargetToDiff(target),
		RewardSat:     g.CoinbaseValue,
		CleanJobs:     forceClean,
	}, nil
}

// HandleSubmit 校验一条提交：重建 coinbase→merkle→header→池端重算哈希→判难度/命中。
func (m *JobManager) HandleSubmit(ctx context.Context, sub stratum.Submission) stratum.SubmitResult {
	job, ok := m.reg.Get(sub.JobID)
	if !ok {
		return stratum.SubmitResult{Outcome: core.OutcomeStale}
	}
	if len(sub.ExtraNonce2) != m.en2Size {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	// 1) 重建 coinbase txid（en1||en2 填入 scriptSig 空位）
	cbTxid, err := job.Coinbase.TxID(sub.ExtraNonce1, sub.ExtraNonce2)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}
	// 2) merkle root（空块 = coinbase txid）
	root := btcwork.MerkleRootFromBranch(cbTxid, job.MerkleBranch)
	// 3) 组 header（prevhash 内部序）
	prevInternal := btcwork.Reverse(mustHex(job.PrevHashBE))
	header := btcwork.SerializeHeader(sub.EffectiveVersion(job.Version), prevInternal, root, sub.NTime, job.Bits, sub.Nonce)
	// 4) 池端重算共识哈希（与节点同源，铁律）
	hashRaw, err := m.hsh.Hash(header)
	if err != nil {
		return stratum.SubmitResult{Outcome: core.OutcomeMalformed}
	}

	shareDiff := btcwork.ShareDiff(hashRaw)

	// 5) 命中全网目标 → 爆块。爆块 share 同样是一条合格 share：
	// 计入 PPLNS 窗口与算力统计（miningcore 同款语义），否则爆块矿工反而少记一份权重。
	if btcwork.HashMeetsTarget(hashRaw, job.NetworkTarget) {
		if m.onShare != nil {
			m.onShare(ctx, core.Share{
				Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
				UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
				Difficulty: sub.RequiredDiff, Solo: sub.Solo, At: time.Now(),
			})
		}
		m.handleBlock(ctx, job, sub, header, hashRaw)
		return stratum.SubmitResult{Outcome: core.OutcomeBlock, CreditDiff: sub.RequiredDiff}
	}

	// 6) 份额难度判定（stratum 层已做 vardiff 上下文；这里按矿工当前难度判）
	if shareDiff+1e-9 < sub.RequiredDiff {
		// 注：一步 grace 的 prevDiff 判定在 vardiff.Judge，M1 由 stratum 层的 RequiredDiff 承载当前难度；
		// 更精细的 grace 归属在 M2 与 vardiff.State 打通（现按当前难度，straddle 记 lowdiff 良性）。
		return stratum.SubmitResult{Outcome: core.OutcomeLowDiff}
	}

	if m.onShare != nil {
		m.onShare(ctx, core.Share{
			Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
			UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
			Difficulty: sub.RequiredDiff, Solo: sub.Solo, At: time.Now(),
		})
	}
	return stratum.SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: sub.RequiredDiff}
}

func (m *JobManager) handleBlock(ctx context.Context, job *stratum.Job, sub stratum.Submission, header, hashRaw []byte) {
	cbWitness, err := job.Coinbase.SerializeWitness(sub.ExtraNonce1, sub.ExtraNonce2)
	if err != nil {
		return
	}
	blockHex := btcwork.AssembleBlock(header, cbWitness, job.RawTxs)
	blockHashBE := hex.EncodeToString(btcwork.Reverse(hashRaw))

	fb := core.FoundBlock{
		Coin: m.coinID, Height: job.Height, Hash: blockHashBE,
		Finder: sub.Address, Worker: sub.Worker,
		Reward: satToStr(job.RewardSat, m.decimals), NetDiff: job.NetDiff,
		Status: core.BlockPending, FoundAt: time.Now(), Solo: sub.Solo,
	}
	// 意图先落库，动作后执行（docs/05 场景B）：BlockSink 记 submitting → 调 submit
	// 交节点 → 成功 markPending。bitcoin 系 hash 提交前已知，submit 原样返回。
	submit := func(ctx context.Context) (string, error) {
		if err := m.node.SubmitBlock(ctx, blockHex); err != nil {
			return "", err
		}
		return blockHashBE, nil
	}
	if m.onBlock != nil {
		_ = m.onBlock(ctx, fb, blockHex, submit)
		return
	}
	_, _ = submit(ctx)
}

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
