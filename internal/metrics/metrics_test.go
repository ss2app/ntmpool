package metrics

import (
	"strings"
	"testing"

	"github.com/scashcc/ntmpool/internal/core"
)

func TestRenderShapeAndCounts(t *testing.T) {
	// 注：进程单例，测试用独立 coin 名避免与其他测试串味
	ShareResult("mtest", core.OutcomeAccepted)
	ShareResult("mtest", core.OutcomeAccepted)
	ShareResult("mtest", core.OutcomeBadPow)
	ShareResult("mtest", core.OutcomeLowDiff)
	BlockSubmitted("mtest")
	PayoutSent("mtest", "payout", 47.5)
	PayoutSent("mtest", "fee_collect", 2.5)

	out := Render()
	// Prometheus 文本格式基本骨架
	for _, want := range []string{
		"# TYPE ntmpool_shares_total counter",
		`ntmpool_shares_total{coin="mtest",outcome="accepted"} 2`,
		`ntmpool_shares_total{coin="mtest",outcome="badpow"} 1`,
		`ntmpool_shares_total{coin="mtest",outcome="lowdiff"} 1`,
		`ntmpool_blocks_submitted_total{coin="mtest"} 1`,
		`ntmpool_payouts_total{coin="mtest",kind="fee_collect"} 1`,
		`ntmpool_payouts_total{coin="mtest",kind="payout"} 1`,
		`ntmpool_payout_coins_total{coin="mtest",kind="fee_collect"} 2.5`,
		`ntmpool_payout_coins_total{coin="mtest",kind="payout"} 47.5`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺行 %q\n完整输出:\n%s", want, out)
		}
	}
	// badpow 与 lowdiff 必须分开（共识 tripwire 不许被良性拒稀释）
	if strings.Count(out, `outcome="badpow"`) != 1 || strings.Count(out, `outcome="lowdiff"`) != 1 {
		t.Fatal("badpow / lowdiff 未各自独立计数")
	}
}

// 真值 gauge：注入 TruthSource 后 Render 必须带 DB 真值（重启不清零口径）；
// FeesUncollected<0 = 未知，不许渲染该行。
func TestRenderTruthGauges(t *testing.T) {
	SetTruthSource(func() map[string]Truth {
		return map[string]Truth{
			"ttest": {
				BlocksFound: 7, BlocksConfirmed: 5, BlocksOrphaned: 1,
				FeesAccrued: 3.25, FeesUncollected: 1.5,
				TotalPaid: 40, MinerBalance: 6.75, DebtsNet: 0,
			},
			"tunknown": {FeesUncollected: -1},
		}
	})
	defer SetTruthSource(nil)

	out := Render()
	for _, want := range []string{
		"# TYPE ntmpool_blocks gauge",
		`ntmpool_blocks{coin="ttest",status="found"} 7`,
		`ntmpool_blocks{coin="ttest",status="confirmed"} 5`,
		`ntmpool_blocks{coin="ttest",status="orphaned"} 1`,
		`ntmpool_fees_accrued_coins{coin="ttest"} 3.25`,
		`ntmpool_fees_uncollected_coins{coin="ttest"} 1.5`,
		`ntmpool_paid_coins{coin="ttest"} 40`,
		`ntmpool_miner_balance_coins{coin="ttest"} 6.75`,
		`ntmpool_debts_net_coins{coin="ttest"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺行 %q\n完整输出:\n%s", want, out)
		}
	}
	if strings.Contains(out, `ntmpool_fees_uncollected_coins{coin="tunknown"}`) {
		t.Fatal("未知的未归集费(<0)不许渲染")
	}
	// 真值源卸掉后不得再渲染 gauge（单测/无 DB 部署形态）
	SetTruthSource(nil)
	if strings.Contains(Render(), "ntmpool_blocks{") {
		t.Fatal("TruthSource=nil 时不应渲染真值 gauge")
	}
}
