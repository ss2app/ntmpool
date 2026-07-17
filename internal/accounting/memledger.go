package accounting

import (
	"context"
	"fmt"
	"log"
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
	// 直付块（midstate coinbase 直付，docs/07 §6）：RecordBlock 时定死的
	// credit/paid 快照；confirm 按快照入账（+credit −paid），绝不重算窗口。
	direct     bool
	directCred map[string]int64 // 地址→应得（credit）
	directPaid map[string]int64 // 地址→随 coinbase 实付（paid）
}

// memBalanceChange 与 PG balance_changes 的语义逐项对齐，是 J4 重放校验的内存事件流。
// 它刻意保持私有：当前只用于在线不变量检查，不是产品查询 API。
type memBalanceChange struct {
	addr  string
	delta int64
	usage string
	tag   string
}

type memJournalLeg struct {
	account string
	address string
	delta   int64
}

type memJournalTx struct {
	key  string
	kind string
	legs []memJournalLeg
}

// MemLedger 单实例内存会计（M1 + 单元测试；生产多实例用 Postgres 实现）。
type MemLedger struct {
	mu       sync.Mutex
	decimals int
	pplnsN   float64 // 窗口 = pplnsN × 网络难度

	shares   []memShare       // 统一滚动窗口（PPLNS）；孤块 share 天然并入
	balances map[string]int64 // 地址→聪
	changes  []memBalanceChange
	debts    map[string]int64 // 地址→未抵扣欠款（聪）
	blocks   []*memBlock
	journal  []memJournalTx

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

// addBalanceChange 须在持有 l.mu 时调用；usage/tag 必须与 PGLedger.addBalanceTx 完全一致。
func (l *MemLedger) addBalanceChange(addr string, delta int64, usage, tag string) {
	l.changes = append(l.changes, memBalanceChange{addr: addr, delta: delta, usage: usage, tag: tag})
}

// appendJournal 须在持有 l.mu 时调用。影子期校验失败或 business_key 重复只记 P0 并跳过，
// 绝不回滚或改变已经完成的内存投影业务结果。
func (l *MemLedger) appendJournal(coin string, j *journalBuilder) {
	if err := j.validate(); err != nil {
		log.Printf("[P0] [会计 %s] journal_write_skipped business_key=%s kind=%s err=%v",
			coin, j.businessKey, j.kind, err)
		return
	}
	for _, existing := range l.journal {
		if existing.key == j.businessKey {
			log.Printf("[P0] [会计 %s] journal_duplicate_business_key business_key=%s kind=%s action=skip",
				coin, j.businessKey, j.kind)
			return
		}
	}
	mt := memJournalTx{key: j.businessKey, kind: j.kind, legs: make([]memJournalLeg, 0, len(j.legs))}
	for _, leg := range j.legs {
		mt.legs = append(mt.legs, memJournalLeg{account: leg.account, address: leg.address, delta: leg.delta})
	}
	l.journal = append(l.journal, mt)
}

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
	mb := &memBlock{b: b, rawHex: rawHex}
	if b.Direct != nil {
		mb.direct = true
		mb.directCred = map[string]int64{}
		mb.directPaid = map[string]int64{}
		for _, dc := range b.Direct {
			c, err := l.parse(dc.Credit)
			if err != nil {
				return fmt.Errorf("直付 credit 金额非法 %q: %w", dc.Credit, err)
			}
			p, err := l.parse(dc.Paid)
			if err != nil {
				return fmt.Errorf("直付 paid 金额非法 %q: %w", dc.Paid, err)
			}
			mb.directCred[dc.Address] += c
			mb.directPaid[dc.Address] += p
		}
	}
	l.blocks = append(l.blocks, mb)
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

func (l *MemLedger) UpdateBlockReward(_ context.Context, _, hash, reward string) error {
	rewardSat, err := l.parse(reward)
	if err != nil {
		return fmt.Errorf("块 reward 金额非法 %q: %w", reward, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	mb := l.findBlock(hash)
	if mb == nil {
		return fmt.Errorf("块 %s 不存在", hash)
	}
	if mb.b.Status != core.BlockPending {
		return fmt.Errorf("块 %s 状态 %s 不允许回填 reward", hash, mb.b.Status)
	}
	mb.b.Reward = l.toStr(rewardSat)
	mb.b.RewardPending = false
	return nil
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
	if mb.b.RewardPending {
		return fmt.Errorf("块 %s 的权威 reward 尚未回填", b.Hash)
	}
	rewardSat, err := l.parse(mb.b.Reward)
	if err != nil {
		return err
	}

	// 直付块：按 RecordBlock 快照入账（+credit −paid），费=E−Σcredit，
	// 忽略传入 feePercent（费率已在模板分账时生效）。不碰 debts 抵扣——
	// carry 计划时已减掉 debts（DirectPlanInputs），coinbase 无法扣款。
	if mb.direct {
		journal := newJournalTx("direct_confirm", "confirm:block:"+b.Hash, journalPolicyPreRegistry,
			fmt.Sprintf("feePercent=%g solo=%t direct=true", feePercent, b.Solo))
		journal.leg("block:revenue", "", rewardSat)
		var sumCredit int64
		for a, c := range mb.directCred {
			l.balances[a] += c
			l.addBalanceChange(a, c, "reward", "block:"+b.Hash)
			sumCredit += c
			journal.leg("miner:payable", a, -c)
			if p := mb.directPaid[a]; p > 0 {
				l.balances[a] -= p
				l.addBalanceChange(a, -p, "payment", "block:"+b.Hash)
				l.totalPaid += p
				l.paidOut += p
				l.paidByAddr[a] += p
				journal.leg("miner:payable", a, p).leg("payout:settled", "", -p)
			}
		}
		mb.credited = true
		mb.feeSat = rewardSat - sumCredit
		l.totalFees += mb.feeSat
		journal.leg("pool:fee_accrued", "", -mb.feeSat)
		mb.b.Status = core.BlockConfirmed
		l.setBlockStatus(b.Hash, core.BlockConfirmed)
		l.appendJournal(b.Coin, journal)
		return nil
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

	journal := newJournalTx("confirm", "confirm:block:"+b.Hash, journalPolicyPreRegistry,
		fmt.Sprintf("feePercent=%g solo=%t", feePercent, b.Solo))
	journal.leg("block:revenue", "", rewardSat).leg("pool:fee_accrued", "", -feeSat)
	// 入账：先抵扣该地址 debts（孤块追缴），余额入 balances
	for a, amt := range payouts {
		fullAmt := amt
		if d := l.debts[a]; d > 0 {
			take := d
			if take > amt {
				take = amt
			}
			l.debts[a] -= take
			amt -= take
		}
		l.balances[a] += amt
		l.addBalanceChange(a, amt, "reward", "block:"+b.Hash)
		journal.leg("miner:payable", a, -amt)
		journal.leg("debt:receivable", a, -(fullAmt - amt))
	}
	l.totalFees += feeSat
	mb.credited = true
	mb.payouts = payouts
	mb.feeSat = feeSat
	mb.b.Status = core.BlockConfirmed
	l.setBlockStatus(b.Hash, core.BlockConfirmed)
	l.appendJournal(b.Coin, journal)
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
	// 直付块回滚：coinbase 没上链=谁都没拿到——先退回 paid（对冲 totalPaid），
	// 再按 credit 快照反转余额（扣不动的记 debt，同普通块）。
	if mb.credited && mb.direct {
		journal := newJournalTx("orphan", "orphan:block:"+b.Hash, journalPolicyPreRegistry, "")
		rewardSat, err := l.parse(mb.b.Reward)
		if err != nil {
			return err
		}
		journal.leg("pool:fee_accrued", "", mb.feeSat).leg("block:revenue", "", -rewardSat)
		var takeSum, remainSum int64
		for a, p := range mb.directPaid {
			if p <= 0 {
				continue
			}
			l.balances[a] += p
			l.addBalanceChange(a, p, "payment_refund", "block:"+b.Hash)
			l.totalPaid -= p
			l.paidOut -= p
			l.paidByAddr[a] -= p
			journal.leg("payout:settled", "", p).leg("miner:payable", a, -p)
		}
		for a, c := range mb.directCred {
			if l.balances[a] >= c {
				l.balances[a] -= c
				if c > 0 {
					l.addBalanceChange(a, -c, "orphan_reversal", "block:"+b.Hash)
				}
				takeSum += c
				journal.leg("miner:payable", a, c)
			} else {
				take := l.balances[a]
				remain := c - take
				l.balances[a] = 0
				if take > 0 {
					l.addBalanceChange(a, -take, "orphan_reversal", "block:"+b.Hash)
				}
				l.debts[a] += remain
				takeSum += take
				remainSum += remain
				journal.leg("miner:payable", a, take).leg("debt:receivable", a, remain)
			}
		}
		l.totalFees -= mb.feeSat
		mb.feeSat = 0
		mb.credited = false
		mb.b.Status = core.BlockOrphaned
		l.setBlockStatus(b.Hash, core.BlockOrphaned)
		journal.memo = fmt.Sprintf("takeSum=%s remainSum=%s direct=true", l.toStr(takeSum), l.toStr(remainSum))
		l.appendJournal(b.Coin, journal)
		return nil
	}

	// 若已入账（预打款垫付场景）：把已发的每地址金额转成 debts 追缴
	if mb.credited {
		journal := newJournalTx("orphan", "orphan:block:"+b.Hash, journalPolicyPreRegistry, "")
		rewardSat, err := l.parse(mb.b.Reward)
		if err != nil {
			return err
		}
		journal.leg("pool:fee_accrued", "", mb.feeSat).leg("block:revenue", "", -rewardSat)
		var takeSum, remainSum int64
		// 配平推导：Σtake + Σremain = Σpayouts = reward - fee；
		// 再加 DR fee 与 CR reward 后整笔为零，builder 的 J1 断言作最终兜底。
		for a, amt := range mb.payouts {
			// 已在余额里的先扣回，扣不动的（已打款出去）记 debt
			if l.balances[a] >= amt {
				l.balances[a] -= amt
				if amt > 0 {
					l.addBalanceChange(a, -amt, "orphan_reversal", "block:"+b.Hash)
				}
				takeSum += amt
				journal.leg("miner:payable", a, amt)
			} else {
				take := l.balances[a]
				remain := amt - take
				l.balances[a] = 0
				if take > 0 {
					l.addBalanceChange(a, -take, "orphan_reversal", "block:"+b.Hash)
				}
				l.debts[a] += remain
				takeSum += take
				remainSum += remain
				journal.leg("miner:payable", a, take).leg("debt:receivable", a, remain)
			}
		}
		// 计提费一并作废（不作废则守恒 delta=-fee 误冻结打款；M4 修）
		l.totalFees -= mb.feeSat
		mb.feeSat = 0
		journal.memo = fmt.Sprintf("takeSum=%s remainSum=%s direct=false", l.toStr(takeSum), l.toStr(remainSum))
		l.appendJournal(b.Coin, journal)
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

func (l *MemLedger) DeductForPayout(_ context.Context, coin string, outputs map[string]string, batchID int64) error {
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
	journal := newJournalTx("deduct", fmt.Sprintf("deduct:batch:%d", batchID), journalPolicyPreRegistry,
		fmt.Sprintf("batchID=%d", batchID))
	for a, amt := range deduct {
		l.balances[a] -= amt
		l.addBalanceChange(a, -amt, "payment", fmt.Sprintf("batch:%d", batchID))
		l.paidOut += amt
		l.totalPaid += amt
		l.paidByAddr[a] += amt
		journal.leg("miner:payable", a, amt).leg("payout:settled", "", -amt)
	}
	l.appendJournal(coin, journal)
	return nil
}

func (l *MemLedger) RefundPayout(_ context.Context, coin string, outputs map[string]string, batchID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	journal := newJournalTx("refund", fmt.Sprintf("refund:batch:%d", batchID), journalPolicyPreRegistry,
		fmt.Sprintf("batchID=%d", batchID))
	for a, s := range outputs {
		amt, err := l.parse(s)
		if err != nil {
			return err
		}
		l.balances[a] += amt
		l.addBalanceChange(a, amt, "payment_refund", fmt.Sprintf("batch:%d", batchID))
		l.paidOut -= amt
		l.totalPaid -= amt
		l.paidByAddr[a] -= amt
		journal.leg("payout:settled", "", amt).leg("miner:payable", a, -amt)
	}
	l.appendJournal(coin, journal)
	return nil
}

// DirectPlanInputs 直付分账计划输入（docs/07 §6）：窗口快照 + 可用 carry。
func (l *MemLedger) DirectPlanInputs(_ context.Context, _ string, windowWeight float64) (map[string]float64, map[string]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	weights := l.windowByWeight(windowWeight)

	// 在飞直付预留：未入账（pending 占位）直付块的 Σmax(paid−credit,0)
	reserved := map[string]int64{}
	for _, mb := range l.blocks {
		if !mb.direct || mb.credited || mb.b.Status == core.BlockOrphaned {
			continue
		}
		for a, p := range mb.directPaid {
			if extra := p - mb.directCred[a]; extra > 0 {
				reserved[a] += extra
			}
		}
	}
	carry := map[string]string{}
	for a, bal := range l.balances {
		avail := bal - reserved[a] - l.debts[a]
		if avail > 0 {
			carry[a] = l.toStr(avail)
		}
	}
	return weights, carry, nil
}

func (l *MemLedger) Reconcile(_ context.Context, coin string) (string, error) {
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

	streamByAddr := make(map[string]int64)
	for _, change := range l.changes {
		streamByAddr[change.addr] += change.delta
	}
	type mismatch struct {
		addr                  string
		balance, stream, diff int64
	}
	var mismatches []mismatch
	var j4Abs int64
	seen := make(map[string]struct{}, len(l.balances))
	for addr, balance := range l.balances {
		stream := streamByAddr[addr]
		seen[addr] = struct{}{}
		if diff := balance - stream; diff != 0 {
			mismatches = append(mismatches, mismatch{balance: balance, stream: stream, diff: diff})
			mismatches[len(mismatches)-1].addr = addr
			j4Abs += absAmount(diff)
		}
	}
	for addr, stream := range streamByAddr {
		if _, ok := seen[addr]; ok || stream == 0 {
			continue
		}
		mismatches = append(mismatches, mismatch{addr: addr, stream: stream, diff: -stream})
		j4Abs += absAmount(stream)
	}
	if len(mismatches) > 0 {
		sort.Slice(mismatches, func(i, j int) bool { return mismatches[i].addr < mismatches[j].addr })
		log.Printf("[P0] [会计 %s] j4_balance_stream_mismatch mismatches=%d abs_diff=%s", coin, len(mismatches), l.toStr(j4Abs))
		for i := 0; i < len(mismatches) && i < 3; i++ {
			m := mismatches[i]
			log.Printf("[P0] [会计 %s] j4_sample address=%s balance=%s stream_sum=%s diff=%s",
				coin, m.addr, l.toStr(m.balance), l.toStr(m.stream), l.toStr(m.diff))
		}
	}
	// 现有守恒异常优先；仅当守恒为零时，J4 绝对差才接管返回值并触发冻结。
	if delta == 0 && j4Abs != 0 {
		delta = j4Abs
	}
	return l.toStr(delta), nil
}

func absAmount(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
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

func (l *MemLedger) HasBlockHash(_ context.Context, _, blockHash string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, mb := range l.blocks {
		if mb.b.Hash == blockHash {
			return true, nil
		}
	}
	return false, nil
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
