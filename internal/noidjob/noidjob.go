//go:build noid

// Package noidjob 是 ParanO(1)d (NOID) 的作业管理器——第五条作业管线
// （与 jobmanager=GBT 系 / cnjob=blob 系 / midjob=midstate / btc09job 平行）。
//
// NOID 模型（施工图 §3 + REPORT-节点协议）：节点单飞行槽强类型模板（noidrpc 客户端侧
// 缓存单槽），本管理器持有当前模板一份向矿工面 fanout：
//
//	① Refresh 拉 noidrpc.GetTemplate（Raw=*NoidTemplate）；template_id 变即换 job + 广播。
//	② 矿工面（stratum noidhttp）submit → HandleSubmit 池端重算校验：
//	   noidp2b.Digest 重算 → (template_id,nonce) 全池去重 → share target 门（noidp2b.Check
//	   宽松，严格 <）→ 达全网 target 才 SubmitNonce 上行节点（普通 share ★绝不上行，
//	   否则烧模板槽）→ 记账回调。
//	③ 爆块：SubmitNonce 成功 → ConsumeTemplate 作废本地缓存 + 清 m.cur（停发已消费模板）
//	   + async 触发 Refresh 拿下一个模板（制备 7-35s，不阻塞 submit 响应）。
//
// ★安全不变式（错一条就丢块/烧模板/误封矿工）：
//   - target 判定【只】走 noidp2b.Check（官方 le256_lt 严格 <，等于拒绝）——绝不用
//     cnwork.MeetsTarget（那是 <=，NOID 是 <）。
//   - 普通 share 不上行节点；只有池端重算达全网 target 的才 SubmitNonce。
//   - dup 按 (template_id,nonce) 全池维度（当前模板一张 seen 表，换模板即换表）。
package noidjob

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/noidrpc"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/noidp2b"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// algoName 本币算法名（login 能力协商 + hasher 注册名）。
const algoName = "poseidon2b/noid"

// nodeIface Manager 对节点的最小依赖（便于测试打桩）。
type nodeIface interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	// SubmitNonce 提交爆块解，返回节点权威块 hash（孤块比对基准）。
	SubmitNonce(ctx context.Context, templateID, nonceHex string) (string, error)
	// ConsumeTemplate submitBlock 成功后作废节点客户端侧的单槽缓存。
	ConsumeTemplate(templateID string)
}

// SubmitFunc / BlockSink / AcceptedSink 会计层注入（与其余管线同形）。
type SubmitFunc func(ctx context.Context) (finalHash string, err error)
type BlockSink func(ctx context.Context, b core.FoundBlock, rawBlockHex string, submit func(context.Context) (string, error)) error
type AcceptedSink func(ctx context.Context, s core.Share)

// noidJob 一个模板级 job。
type noidJob struct {
	tpl       *noidrpc.NoidTemplate
	height    uint64
	netTarget [32]byte // 全网 target（256-bit LE，noidp2b.Check 直用）
	netDiff   float64
	reward    string    // 十进制字符串（NOID 奖励不在模板里 → 占位 "0"，见 New 注释）
	seen      *sync.Map // dedupKey(nonce hex) → struct{}；全池维度，按当前 template
}

// Manager 每币一个。实现 stratum.NoidShareHandler + coininstance.jobPipe。
type Manager struct {
	coinID string
	node   nodeIface

	broadcast func()
	onBlock   BlockSink
	onShare   AcceptedSink

	mu  sync.Mutex
	cur *noidJob
}

var _ stratum.NoidShareHandler = (*Manager)(nil)

// New 建一个 NOID 作业管理器。
//
// NOID 块奖励不在模板里（节点用自身 mining-key 钱包付款，per-miner 直付不可行 →
// 池 PPLNS 分账）。当前无 WalletAdapter（payout 关），FoundBlock.Reward 置占位 "0"；
// 接钱包适配器 + BlockRewardSource 后补真值（施工图 §5 遗留）。
func New(coinID string, node nodeIface) *Manager {
	return &Manager{coinID: coinID, node: node}
}

// SetCallbacks 注入广播与会计回调。
func (m *Manager) SetCallbacks(broadcast func(), onBlock BlockSink, onShare AcceptedSink) {
	m.broadcast = broadcast
	m.onBlock = onBlock
	m.onShare = onShare
}

// Algo 实现 stratum.NoidShareHandler。
func (m *Manager) Algo() string { return algoName }

// ---- coininstance.jobPipe ----

// Refresh 拉最新模板；template_id 变即换当前 job 并广播（noidrpc.GetTemplate 内部单槽
// 缓存已节流打节点，同模板重复调用是廉价缓存命中，不会打断矿工）。clean 未用：模板身份
// 由 template_id 判定，tip 变时 noidrpc 会自动返回新模板。
func (m *Manager) Refresh(ctx context.Context, _ bool) error {
	bt, err := m.node.GetTemplate(ctx)
	if err != nil {
		return err
	}
	tpl, ok := bt.Raw.(*noidrpc.NoidTemplate)
	if !ok {
		return fmt.Errorf("[%s] 模板 Raw 非 *noidrpc.NoidTemplate（适配器实现错误）", m.coinID)
	}
	m.mu.Lock()
	if m.cur != nil && m.cur.tpl.TemplateID == tpl.TemplateID {
		m.mu.Unlock()
		return nil // 同模板：矿工已有，别无谓换工
	}
	j := &noidJob{
		tpl:       tpl,
		height:    tpl.Height,
		netTarget: tpl.NetworkTarget,
		netDiff:   diffFromTargetLE(tpl.NetworkTarget),
		reward:    "0",
		seen:      &sync.Map{},
	}
	m.cur = j
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
	if m.cur == nil {
		return 0, 0, false
	}
	return m.cur.height, m.cur.netDiff, true
}

// ---- stratum.NoidShareHandler ----

// Template 为一个 worker 物化当前模板：worker tag 写入 nonce 字段（field 10）高 64 位。
//
// ★u128 nonce 分区（施工图 §3 + cnjob:89-101 血泪注释）：官方矿工覆写整个 16B nonce
// （随机起点 ~30bit 熵）忽略 tag；自研 NTMminer-noid 会只滚低 64 位、高 64 位用 tag，
// 从而多 worker 不相交。tag 撞车（64 位空间，概率可忽略）只会让爆块率不涨、池侧指标全
// 正常。field 10 内容对校验【无影响】：矿工提交完整 16B nonce，池按提交值覆写 field 10
// 重算——tag 只是给自研锄头的搜索起点建议。
func (m *Manager) Template(workerTag uint64) (stratum.NoidTemplateWire, bool) {
	m.mu.Lock()
	j := m.cur
	m.mu.Unlock()
	if j == nil {
		return stratum.NoidTemplateWire{}, false
	}
	fields := j.tpl.PowFields // 值拷贝 [256]byte
	tagOff := j.tpl.NonceFieldIndex*16 + 8
	if tagOff+8 <= len(fields) {
		putU64LE(fields[tagOff:tagOff+8], workerTag)
	}
	return stratum.NoidTemplateWire{
		TemplateID:       j.tpl.TemplateID,
		PowFieldsHex:     hex.EncodeToString(fields[:]),
		NonceFieldIndex:  j.tpl.NonceFieldIndex,
		Height:           j.height,
		ExpiresInSeconds: expiresIn(j.tpl.ExpiresAt),
		NTxs:             j.tpl.NTxs,
	}, true
}

// HandleSubmit 池端重算一条提交。★安全核心，见文件头不变式。
func (m *Manager) HandleSubmit(ctx context.Context, sub stratum.NoidSubmission) stratum.NoidSubmitResult {
	m.mu.Lock()
	j := m.cur
	m.mu.Unlock()
	// 只接受【当前模板】的提交：NOID 单飞行槽，别的 template_id 无法出块（节点会判 stale），
	// 记 stale 让矿工重取（不进 violent 计数）。
	if j == nil || sub.TemplateID != j.tpl.TemplateID {
		return stratum.NoidSubmitResult{Outcome: core.OutcomeStale}
	}
	if len(sub.NonceLE) != noidp2b.NonceWireBytes {
		return stratum.NoidSubmitResult{Outcome: core.OutcomeMalformed}
	}
	if len(sub.ShareTarget) != noidp2b.TargetBytes {
		return stratum.NoidSubmitResult{Outcome: core.OutcomeMalformed}
	}

	// dup 去重：(template_id, nonce) 全池维度（当前 job 一张 seen 表）。放在重算前，
	// 重复提交不再浪费一次 poseidon。
	dedupKey := hex.EncodeToString(sub.NonceLE)
	if _, dup := j.seen.LoadOrStore(dedupKey, struct{}{}); dup {
		return stratum.NoidSubmitResult{Outcome: core.OutcomeDup}
	}

	fields := j.tpl.PowFields[:] // 256B；noidp2b 内部用 nonce 覆写 field 10
	// 池端重算 digest（= 官方 BlockHash 原样 32B），供计权难度用。
	digest := noidp2b.Digest(fields, sub.NonceLE)

	// share target 门（宽松）：严格 256-bit LE 小于，只走 noidp2b.Check。
	if !noidp2b.Check(fields, sub.NonceLE, sub.ShareTarget) {
		return stratum.NoidSubmitResult{Outcome: core.OutcomeLowDiff}
	}

	// 有效 share：计权难度按 LE 口径（float 仅用于权重，门用整数 Check 已判过）。
	shareDiff := cnwork.ShareDiff(digest[:], false)
	credit, ok := sub.Judge(shareDiff)
	if !ok {
		credit = shareDiff
	}

	// 全网 target 门 → 爆块（爆块 share 同样计入 PPLNS 权重，行业同款）。
	if noidp2b.Check(fields, sub.NonceLE, j.netTarget[:]) {
		if m.onShare != nil {
			m.onShare(ctx, core.Share{
				Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
				UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
				Difficulty: credit, Solo: sub.Solo, At: time.Now(),
			})
		}
		blockHash := m.handleBlock(ctx, j, sub, digest)
		return stratum.NoidSubmitResult{Outcome: core.OutcomeBlock, CreditDiff: credit, BlockHash: blockHash}
	}

	// 普通有效 share（未达全网 target）：记账，不上行节点。
	if m.onShare != nil {
		m.onShare(ctx, core.Share{
			Coin: m.coinID, Address: sub.Address, Worker: sub.Worker,
			UserAgent: sub.UserAgent, RemoteIP: sub.RemoteIP,
			Difficulty: credit, Solo: sub.Solo, At: time.Now(),
		})
	}
	return stratum.NoidSubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: credit}
}

// handleBlock 提交爆块：SubmitNonce（唯一上行节点的路径）→ 成功则 ConsumeTemplate +
// 清 m.cur（停发已消费模板）+ async 拉下一模板。意图 hash = hex(digest)（NOID BlockHash
// = poseidon digest）；提交成功后 BlockSink 换成节点权威 id。返回给矿工的块 hash。
func (m *Manager) handleBlock(ctx context.Context, j *noidJob, sub stratum.NoidSubmission, digest [32]byte) string {
	tid := j.tpl.TemplateID
	nonceHex := hex.EncodeToString(sub.NonceLE)
	blockHashHex := hex.EncodeToString(digest[:])
	submit := func(ctx context.Context) (string, error) {
		h, err := m.node.SubmitNonce(ctx, tid, nonceHex)
		if err != nil {
			return "", err
		}
		m.node.ConsumeTemplate(tid)
		m.mu.Lock()
		if m.cur == j {
			m.cur = nil // 停发已消费模板；下一次 Refresh 装新模板（暖机期矿工收 warming up）
		}
		m.mu.Unlock()
		return h, nil
	}
	if m.onBlock == nil {
		_, _ = submit(ctx)
		return blockHashHex
	}
	fb := core.FoundBlock{
		Coin: m.coinID, Height: j.height, Hash: blockHashHex,
		Finder: sub.Address, Worker: sub.Worker,
		Reward: j.reward, NetDiff: j.netDiff,
		Status: core.BlockPending, FoundAt: time.Now(), Solo: sub.Solo,
	}
	_ = m.onBlock(ctx, fb, "", submit)
	// 触发拉下一个模板（async：制备 7-35s，别阻塞 submit 响应链）。
	go func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = m.Refresh(ctx2, true)
	}()
	return blockHashHex
}

// ---- 工具 ----

// diffFromTargetLE 全网难度 = Diff1 / target（target 为 256-bit LE 字节）。
func diffFromTargetLE(t [32]byte) float64 {
	be := make([]byte, 32)
	for i := 0; i < 32; i++ {
		be[i] = t[31-i] // LE → BE
	}
	v := new(big.Int).SetBytes(be)
	if v.Sign() <= 0 {
		return 0
	}
	q, _ := new(big.Float).Quo(
		new(big.Float).SetInt(cnwork.Diff1),
		new(big.Float).SetInt(v),
	).Float64()
	return q
}

// putU64LE 把 u64 小端写进 8 字节。
func putU64LE(b []byte, v uint64) {
	for i := 0; i < 8 && i < len(b); i++ {
		b[i] = byte(v >> (8 * uint(i)))
	}
}

// expiresIn 距失效秒数（≥1）。
func expiresIn(t time.Time) int64 {
	d := int64(time.Until(t).Seconds())
	if d < 1 {
		return 1
	}
	return d
}
