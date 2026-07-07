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
	PayoutSent("mtest", 47.5)

	out := Render()
	// Prometheus 文本格式基本骨架
	for _, want := range []string{
		"# TYPE ntmpool_shares_total counter",
		`ntmpool_shares_total{coin="mtest",outcome="accepted"} 2`,
		`ntmpool_shares_total{coin="mtest",outcome="badpow"} 1`,
		`ntmpool_shares_total{coin="mtest",outcome="lowdiff"} 1`,
		`ntmpool_blocks_submitted_total{coin="mtest"} 1`,
		`ntmpool_payouts_total{coin="mtest"} 1`,
		`ntmpool_payout_coins_total{coin="mtest"} 47.5`,
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
