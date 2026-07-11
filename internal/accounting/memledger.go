package accounting

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// 金额一律用整数「最小单位」（聪）在内部计算，绝不过浮点（金额铁律）。
// 对外接口用十进制字符串。coinUnit = 10^decimals。

type memShare struct {
	addr   string
	weight float64
	at     time.Time
	solo   bool // solo 端口的 share：只作记录，绝不进 PPLNS 窗口（防稀释 PPLNS 矿工分账）
}

type memBlock struct {
	b      core.FoundBlock
	rawHex string
	// 分账快照：confirm 时按当时 PPLNS 窗口算好的每地址应得（聪）与计提费，
	// 孤块时按快照整体回滚（含费——费也来自该块，不回滚则守恒差 -fee 误冻结打款）
	credited bool
	payouts  map[string]int64
	feeSat   int64
}

// MemLedger 单实例内存会计（M1 + 单元测试；生产多实例用 Postgres 实现）。
type MemLedger struct {
	mu       sync.Mutex
	decimals int
	pplnsN   float64 // 窗口 = pplnsN × 网络难度

	shares   []memShare       // 统一滚动窗口（PPLNS）；孤块 share 天然并入
	balances map[string]int64 // 地址→聪
	debts    map[string]int64 // 地址→未抵扣欠款（聪）
	blocks   []*memBlock

	totalPaid  int64
	totalFees  int64
	paidOut    int64            // 已发出（可能未确认）；对账「在途」用
	paidByAddr map[string]int64 // 地址→累计已付（API 矿工自查）
}

var _ Ledger = (*MemLedger)(nil)

func NewMemLedger(decimals int, pplnsN float64) *MemLedger {
	if pplnsN <= 0 {
		pplnsN = 2.0
	}
	return &MemLedger{
		decimals:   decimals,
		pplnsN:     pplnsN,
		balances:   map[string]int64{},
		debts:      map[string]int64{},
		paidByAddr: map[string]int64{},
	}
}

func (l *MemLedger) unit() int64 { return amountUnit(l.decimals) }

func (l *MemLedger) toStr(sat int64) string { return formatAmount(sat, l.decimals) }

func (l *MemLedger) parse(s string) (int64, error) { return parseAmount(s, l.decimals) }

func (l *MemLedger) RecordShare(_ context.Context, s core.Share, weight float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.shares = append(l.shares, memShare{addr: s.Address, weight: weight, at: s.At, solo: s.Solo})
	return nil
}

func (l *MemLedger) RecordBlock(_ context.Context, b core.FoundBlock, rawHex string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b.Status = core.BlockPending // 逻辑上 submitting，内存实现简化为 pending 前的占位
	l.blocks = append(l.blocks, &memBlock{b: b, rawHex: rawHex})
	return nil
}

func (l *MemLedger) UpdateBlockHash(_ context.Context, _, oldHash, newHash string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if oldHash == newHash {
		return nil
	}
	for _, mb := range l.blocks {
		if mb.b.Hash == oldHash {
			mb.b.Hash = newHash
			return nil
		}
	}
	return fmt.Errorf("块 %s 不存在", oldHash)
}

func (l *MemLedger) MarkBlockPending(_ context.Context, _, hash string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, mb := range l.blocks {
		if mb.b.Hash == hash {
			mb.b.Status = core.BlockPending
			return nil
		}
	}
	return fmt.Errorf("块 %s 不存在", hash)
}

func (l *MemLedger) PendingBlocks(_ context.Context, _ string) ([]core.FoundBlock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.FoundBlock
	for _, mb := range l.blocks {
		if mb.b.Status == core.BlockPending {
			out = append(out, mb.b)
		}
	}
	return out, nil
}

// pplnsShares 返回参与分账的 share（从末尾往前累加权重到 windowWeight 截断）。
// windowWeight = pplnsN × 网络难度；由调用方在 ConfirmBlock 时以块的网络难度算。
// 窗口不满时按实际份额归一化付满 100%（Snipa22 #349 修法）——即无论累计权重多少，
// 都用「各地址权重 / 总参与权重」分配，故窗口不满不会少发。
func (l *MemLedger) windowByWeight(windowWeight float64) map[string]float64 {
	acc := 0.0
	perAddr := map[string]float64{}
	for i := len(l.shares) - 1; i >= 0; i-- {
		s := l.shares[i]
		if s.solo {
			continue // solo share 不参与 PPLNS 分账，也不占窗口容量
		}
		perAddr[s.addr] += s.weight
		acc += s.weight
		if acc >= windowWeight {
			break
		}
	}
	return perAddr
}

func (l *MemLedger) ConfirmBlock(_ context.Context, b core.FoundBlock, feePercent float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	mb := l.findBlock(b.Hash)
	if mb == nil {
		return fmt.Errorf("块 %s 不存在", b.Hash)
	}
	if mb.credited {
		return nil // 幂等
	}
	rewardSat, err := l.parse(b.Reward)
	if err != nil {
		return err
	}
	feeSat := int64(float64(rewardSat) * feePercent / 100.0)
	distributable := rewardSat - feeSat

	payouts := map[string]int64{}
	if mb.b.Solo {
		// SOLO：全归爆块者
		payouts[b.Finder] = distributable
	} else {
		windowWeight := l.pplnsN * b.NetworkDifficulty()
		perAddr := l.windowByWeight(windowWeight)
		total := 0.0
		for _, w := range perAddr {
			total += w
		}
		if total <= 0 {
			// 无 share（不该发生）→ 全给爆块者兜底，不吞奖励
			payouts[b.Finder] = distributable
		} else {
			// 按占比分（整数分配，余数给最大占比者，保证守恒）
			assigned := int64(0)
			addrs := sortedKeys(perAddr)
			for _, a := range addrs {
				share := int64(float64(distributable) * perAddr[a] / total)
				payouts[a] = share
				assigned += share
			}
			if rem := distributable - assigned; rem != 0 && len(addrs) > 0 {
				payouts[addrs[0]] += rem
			}
		}
	}

	// 入账：先抵扣该地址 debts（孤块追缴），余额入 balances
	for a, amt := range payouts {
		if d := l.debts[a]; d > 0 {
			take := d
			if take > amt {
				take = amt
			}
			l.debts[a] -= take
			amt -= take
		}
		l.balances[a] += amt
	}
	l.totalFees += feeSat
	mb.credited = true
	mb.payouts = payouts
	mb.feeSat = feeSat
	mb.b.Status = core.BlockConfirmed
	l.setBlockStatus(b.Hash, core.BlockConfirmed)
	return nil
}

func (l *MemLedger) OrphanBlock(_ context.Context, b core.FoundBlock) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	mb := l.findBlock(b.Hash)
	if mb == nil {
		return fmt.Errorf("块 %s 不存在", b.Hash)
	}
	if mb.b.Status == core.BlockOrphaned {
		return nil
	}
	// 若已入账（预打款垫付场景）：把已发的每地址金额转成 debts 追缴
	if mb.credited {
		for a, amt := range mb.payouts {
			// 已在余额里的先扣回，扣不动的（已打款出去）记 debt
			if l.balances[a] >= amt {
				l.balances[a] -= amt
			} else {
				remain := amt - l.balances[a]
				l.balances[a] = 0
				l.debts[a] += remain
			}
		}
		// 计提费一并作废（不作废则守恒 delta=-fee 误冻结打款；M4 修）
		l.totalFees -= mb.feeSat
		mb.feeSat = 0
	}
	mb.credited = false
	mb.b.Status = core.BlockOrphaned
	l.setBlockStatus(b.Hash, core.BlockOrphaned)
	return nil
}

func (l *MemLedger) PayableBalances(_ context.Context, _ string, defaultThreshold float64, perAddr map[string]float64) (map[string]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]string{}
	for a, sat := range l.balances {
		if sat <= 0 {
			continue
		}
		th := defaultThreshold
		if v, ok := perAddr[a]; ok && v > th {
			th = v
		}
		thSat := int64(th * float64(l.unit()))
		if sat >= thSat {
			out[a] = l.toStr(sat)
		}
	}
	return out, nil
}

func (l *MemLedger) DeductForPayout(_ context.Context, _ string, outputs map[string]string, _ int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 先校验够扣，再统一扣（事务语义）
	deduct := map[string]int64{}
	for a, s := range outputs {
		amt, err := l.parse(s)
		if err != nil {
			return err
		}
		if l.balances[a] < amt {
			return fmt.Errorf("地址 %s 余额不足: 有 %s 需 %s", a, l.toStr(l.balances[a]), s)
		}
		deduct[a] = amt
	}
	for a, amt := range deduct {
		l.balances[a] -= amt
		l.paidOut += amt
		l.totalPaid += amt
		l.paidByAddr[a] += amt
	}
	return nil
}

func (l *MemLedger) RefundPayout(_ context.Context, _ string, outputs map[string]string, _ int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for a, s := range outputs {
		amt, err := l.parse(s)
		if err != nil {
			return err
		}
		l.balances[a] += amt
		l.paidOut -= amt
		l.totalPaid -= amt
		l.paidByAddr[a] -= amt
	}
	return nil
}

func (l *MemLedger) Reconcile(_ context.Context, _ string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Σ已确认奖励 = Σ已付 + Σ余额 + Σ手续费 + Σ债务净额（在途在内存实现里已计入 totalPaid）
	var confirmedRewards int64
	for _, mb := range l.blocks {
		if mb.b.Status == core.BlockConfirmed {
			r, _ := l.parse(mb.b.Reward)
			confirmedRewards += r
		}
	}
	var balSum, debtSum int64
	for _, v := range l.balances {
		balSum += v
	}
	for _, v := range l.debts {
		debtSum += v
	}
	// delta = 确认奖励 - 已付 - 余额 - 费 + 债务净额（债务是「已多付待收回」，抵账正号）
	delta := confirmedRewards - l.totalPaid - balSum - l.totalFees + debtSum
	return l.toStr(delta), nil
}

func (l *MemLedger) Snapshot(_ context.Context, _ string) (Stats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bal := map[string]string{}
	for a, v := range l.balances {
		bal[a] = l.toStr(v)
	}
	var debtNet int64
	for _, v := range l.debts {
		debtNet += v
	}
	confirmed, orphaned := 0, 0
	for _, mb := range l.blocks {
		switch mb.b.Status {
		case core.BlockConfirmed:
			confirmed++
		case core.BlockOrphaned:
			orphaned++
		}
	}
	return Stats{
		Balances: bal, TotalPaid: l.toStr(l.totalPaid), TotalFees: l.toStr(l.totalFees),
		BlocksFound: len(l.blocks), Confirmed: confirmed, Orphaned: orphaned,
		DebtsNet: l.toStr(debtNet), WindowShares: len(l.shares),
	}, nil
}

// Blocks 分页返回块（新→旧：按记录顺序倒序）与总数。
func (l *MemLedger) Blocks(_ context.Context, _ string, offset, limit int) ([]core.FoundBlock, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(l.blocks)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	out := make([]core.FoundBlock, 0, limit)
	// blocks 按发现顺序追加，倒着走 = 新→旧
	for i := total - 1 - offset; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.blocks[i].b)
	}
	return out, total, nil
}

// MinerSummary 单矿工摘要。余额/已付/欠款全为 0 且无记录 = ok=false。
func (l *MemLedger) MinerSummary(_ context.Context, _ string, addr string) (MinerSummary, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bal, hasBal := l.balances[addr]
	paid, hasPaid := l.paidByAddr[addr]
	debt, hasDebt := l.debts[addr]
	if !hasBal && !hasPaid && !hasDebt {
		return MinerSummary{}, false, nil
	}
	return MinerSummary{
		Balance:   l.toStr(bal),
		TotalPaid: l.toStr(paid),
		Debt:      l.toStr(debt),
	}, true, nil
}

// ---- 内部工具 ----

func (l *MemLedger) findBlock(hash string) *memBlock {
	for _, mb := range l.blocks {
		if mb.b.Hash == hash {
			return mb
		}
	}
	return nil
}

func (l *MemLedger) setBlockStatus(hash string, st core.BlockStatus) {
	for _, mb := range l.blocks {
		if mb.b.Hash == hash {
			mb.b.Status = st
		}
	}
}

func sortedKeys(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if m[ks[i]] != m[ks[j]] {
			return m[ks[i]] > m[ks[j]] // 权重大在前（余数给它）
		}
		return ks[i] < ks[j]
	})
	return ks
}
