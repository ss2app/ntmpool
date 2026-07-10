// Package metrics 进程级指标收集 + Prometheus 文本格式导出（零第三方依赖，
// 手写 exposition format，守 go.mod 只依赖 pgx 的极简依赖面）。
//
// 为什么进程单例：Prometheus 一个进程一个 /metrics 端点、抓的是全进程累计量，
// 指标天然是进程级全局状态。用单例避免把 *Collector 穿过 stratum→dialect→conn
// 每一层构造函数（那种线程反而更易漏埋点）。并发安全（加锁）。
//
// 覆盖 zoka live 冒烟暴露的缺口：当时池侧没有 accepted/stale/lowdiff/badpow/dup
// 计数，只能靠矿工侧数拒绝。现在池侧每个 outcome 都有计数器，badpow>0 立刻可见。
//
// ⚠ 两类指标，口径不同（M5 生产教训：dragonx 真爆块后看计数器是 0——进程级
// counter 随 systemd 重启清零，爆块/打款这种低频关键事件一重启就"消失"）：
//   - *_total counter：进程启动以来的事件数，重启归零（Prometheus rate() 语义正确，
//     但绝不能当业务总量看）。
//   - 真值 gauge（SetTruthSource 注入）：每次抓取从会计层(Postgres)现读，重启不丢，
//     业务总量以这组为准。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/scashcc/ntmpool/internal/core"
)

// shareKey 是 (coin, outcome) 复合键。
type shareKey struct {
	coin    string
	outcome string
}

// payoutKey 是 (coin, kind) 复合键（kind=payout|fee_collect|fee_sweep|consolidate）。
type payoutKey struct {
	coin string
	kind string
}

// Truth 会计层真值快照（每币一份）。来源是 Ledger/Postgres，重启不清零；
// 金额转浮点仅供监控展示，绝不作结算依据（金额铁律）。
type Truth struct {
	BlocksFound     int     // blocks 表全部状态计数
	BlocksConfirmed int     // status=confirmed
	BlocksOrphaned  int     // status=orphaned
	FeesAccrued     float64 // 已确认块计提总费
	FeesUncollected float64 // 未归集费（计提 − 已归集批次）；<0 = 未知，不渲染
	TotalPaid       float64 // 累计已打款
	MinerBalance    float64 // Σ 矿工待付余额
	DebtsNet        float64 // 孤块追缴净额
}

var (
	mu           sync.Mutex
	shareCounts  = map[shareKey]uint64{}
	blockCounts  = map[string]uint64{}     // coin → 爆块提交数（池找到并交节点）
	payoutCounts = map[payoutKey]uint64{}  // (coin,kind) → 批次广播数
	payoutAmount = map[payoutKey]float64{} // (coin,kind) → 累计币量（展示用，非结算依据）
	truthSource  func() map[string]Truth   // coin → 真值；nil = 未接线（如无 DB 单测）
)

// ShareResult 记一条 share 的终局结果（每个 outcome 唯一计一次）。
func ShareResult(coin string, outcome core.ShareOutcome) {
	mu.Lock()
	shareCounts[shareKey{coin, outcome.String()}]++
	mu.Unlock()
}

// BlockSubmitted 记一次爆块提交（池找到块并交节点，不含孤块判定）。
func BlockSubmitted(coin string) {
	mu.Lock()
	blockCounts[coin]++
	mu.Unlock()
}

// PayoutSent 记一次批次广播（kind 区分矿工打款/手续费归集/清扫；amount=本批币量，展示用）。
func PayoutSent(coin, kind string, amount float64) {
	mu.Lock()
	k := payoutKey{coin, kind}
	payoutCounts[k]++
	payoutAmount[k] += amount
	mu.Unlock()
}

// SetTruthSource 注入会计层真值提供者（main 接线：遍历币实例读 Ledger.Snapshot）。
// fn 在每次 Render 时于锁外调用，可以做 DB 查询；失败的币直接从结果里省略。
func SetTruthSource(fn func() map[string]Truth) {
	mu.Lock()
	truthSource = fn
	mu.Unlock()
}

// Render 渲染 Prometheus 文本格式（稳定排序，便于 diff/测试）。
func Render() string {
	mu.Lock()
	fn := truthSource
	mu.Unlock()
	var truth map[string]Truth
	if fn != nil {
		truth = fn() // 锁外：可能查 DB，不阻塞埋点热路径
	}

	mu.Lock()
	defer mu.Unlock()
	var sb strings.Builder

	fmt.Fprintln(&sb, "# HELP ntmpool_shares_total Total shares by outcome.")
	fmt.Fprintln(&sb, "# TYPE ntmpool_shares_total counter")
	keys := make([]shareKey, 0, len(shareCounts))
	for k := range shareCounts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].coin != keys[j].coin {
			return keys[i].coin < keys[j].coin
		}
		return keys[i].outcome < keys[j].outcome
	})
	for _, k := range keys {
		fmt.Fprintf(&sb, "ntmpool_shares_total{coin=%q,outcome=%q} %d\n", k.coin, k.outcome, shareCounts[k])
	}

	fmt.Fprintln(&sb, "# HELP ntmpool_blocks_submitted_total Blocks found and submitted to the node (since process start).")
	fmt.Fprintln(&sb, "# TYPE ntmpool_blocks_submitted_total counter")
	for _, c := range sortedU(blockCounts) {
		fmt.Fprintf(&sb, "ntmpool_blocks_submitted_total{coin=%q} %d\n", c, blockCounts[c])
	}

	fmt.Fprintln(&sb, "# HELP ntmpool_payouts_total Batches broadcast by kind (since process start).")
	fmt.Fprintln(&sb, "# TYPE ntmpool_payouts_total counter")
	pkeys := make([]payoutKey, 0, len(payoutCounts))
	for k := range payoutCounts {
		pkeys = append(pkeys, k)
	}
	sort.Slice(pkeys, func(i, j int) bool {
		if pkeys[i].coin != pkeys[j].coin {
			return pkeys[i].coin < pkeys[j].coin
		}
		return pkeys[i].kind < pkeys[j].kind
	})
	for _, k := range pkeys {
		fmt.Fprintf(&sb, "ntmpool_payouts_total{coin=%q,kind=%q} %d\n", k.coin, k.kind, payoutCounts[k])
	}

	fmt.Fprintln(&sb, "# HELP ntmpool_payout_coins_total Cumulative coins sent by kind (display only, since process start).")
	fmt.Fprintln(&sb, "# TYPE ntmpool_payout_coins_total counter")
	for _, k := range pkeys {
		fmt.Fprintf(&sb, "ntmpool_payout_coins_total{coin=%q,kind=%q} %g\n", k.coin, k.kind, payoutAmount[k])
	}

	renderTruth(&sb, truth)
	return sb.String()
}

// renderTruth 渲染会计层真值 gauge（重启不清零，业务总量以这组为准）。
func renderTruth(sb *strings.Builder, truth map[string]Truth) {
	if len(truth) == 0 {
		return
	}
	coins := make([]string, 0, len(truth))
	for c := range truth {
		coins = append(coins, c)
	}
	sort.Strings(coins)

	fmt.Fprintln(sb, "# HELP ntmpool_blocks Blocks in the accounting DB by status (truth, survives restarts).")
	fmt.Fprintln(sb, "# TYPE ntmpool_blocks gauge")
	for _, c := range coins {
		t := truth[c]
		fmt.Fprintf(sb, "ntmpool_blocks{coin=%q,status=\"found\"} %d\n", c, t.BlocksFound)
		fmt.Fprintf(sb, "ntmpool_blocks{coin=%q,status=\"confirmed\"} %d\n", c, t.BlocksConfirmed)
		fmt.Fprintf(sb, "ntmpool_blocks{coin=%q,status=\"orphaned\"} %d\n", c, t.BlocksOrphaned)
	}

	gauge := func(name, help string, val func(Truth) (float64, bool)) {
		fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, c := range coins {
			if v, ok := val(truth[c]); ok {
				fmt.Fprintf(sb, "%s{coin=%q} %g\n", name, c, v)
			}
		}
	}
	always := func(f func(Truth) float64) func(Truth) (float64, bool) {
		return func(t Truth) (float64, bool) { return f(t), true }
	}
	gauge("ntmpool_fees_accrued_coins", "Pool fees accrued from confirmed blocks (truth, display only).",
		always(func(t Truth) float64 { return t.FeesAccrued }))
	gauge("ntmpool_fees_uncollected_coins", "Accrued fees not yet collected to the fee address (truth, display only).",
		func(t Truth) (float64, bool) { return t.FeesUncollected, t.FeesUncollected >= 0 })
	gauge("ntmpool_paid_coins", "Cumulative coins paid to miners (truth, display only).",
		always(func(t Truth) float64 { return t.TotalPaid }))
	gauge("ntmpool_miner_balance_coins", "Sum of pending miner balances (truth, display only).",
		always(func(t Truth) float64 { return t.MinerBalance }))
	gauge("ntmpool_debts_net_coins", "Net orphan-block debts (truth, display only).",
		always(func(t Truth) float64 { return t.DebtsNet }))
}

func sortedU(m map[string]uint64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
