// Package metrics 进程级指标收集 + Prometheus 文本格式导出（零第三方依赖，
// 手写 exposition format，守 go.mod 只依赖 pgx 的极简依赖面）。
//
// 为什么进程单例：Prometheus 一个进程一个 /metrics 端点、抓的是全进程累计量，
// 指标天然是进程级全局状态。用单例避免把 *Collector 穿过 stratum→dialect→conn
// 每一层构造函数（那种线程反而更易漏埋点）。并发安全（加锁）。
//
// 覆盖 zoka live 冒烟暴露的缺口：当时池侧没有 accepted/stale/lowdiff/badpow/dup
// 计数，只能靠矿工侧数拒绝。现在池侧每个 outcome 都有计数器，badpow>0 立刻可见。
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

var (
	mu           sync.Mutex
	shareCounts  = map[shareKey]uint64{}
	blockCounts  = map[string]uint64{}  // coin → 爆块提交数（池找到并交节点）
	payoutCounts = map[string]uint64{}  // coin → 打款批次广播数
	payoutAmount = map[string]float64{} // coin → 累计打款币量（展示用，非结算依据）
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

// PayoutSent 记一次打款批次广播（amount=本批币量，展示用）。
func PayoutSent(coin string, amount float64) {
	mu.Lock()
	payoutCounts[coin]++
	payoutAmount[coin] += amount
	mu.Unlock()
}

// Render 渲染 Prometheus 文本格式（稳定排序，便于 diff/测试）。
func Render() string {
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

	fmt.Fprintln(&sb, "# HELP ntmpool_blocks_submitted_total Blocks found and submitted to the node.")
	fmt.Fprintln(&sb, "# TYPE ntmpool_blocks_submitted_total counter")
	for _, c := range sortedU(blockCounts) {
		fmt.Fprintf(&sb, "ntmpool_blocks_submitted_total{coin=%q} %d\n", c, blockCounts[c])
	}

	fmt.Fprintln(&sb, "# HELP ntmpool_payouts_total Payout batches broadcast.")
	fmt.Fprintln(&sb, "# TYPE ntmpool_payouts_total counter")
	for _, c := range sortedU(payoutCounts) {
		fmt.Fprintf(&sb, "ntmpool_payouts_total{coin=%q} %d\n", c, payoutCounts[c])
	}

	fmt.Fprintln(&sb, "# HELP ntmpool_payout_coins_total Cumulative coins paid out (display only).")
	fmt.Fprintln(&sb, "# TYPE ntmpool_payout_coins_total counter")
	for _, c := range sortedF(payoutAmount) {
		fmt.Fprintf(&sb, "ntmpool_payout_coins_total{coin=%q} %g\n", c, payoutAmount[c])
	}
	return sb.String()
}

func sortedU(m map[string]uint64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedF(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
