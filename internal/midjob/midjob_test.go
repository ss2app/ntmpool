package midjob

import (
	"testing"

	"github.com/scashcc/ntmpool/internal/accounting"
)

func TestTopKPow2(t *testing.T) {
	cases := []struct {
		v    uint64
		k    int
		want uint64
	}{
		{0, 6, 0},
		{16, 6, 16},
		{0b10110111, 2, 0b10100000},
		{0b10110111, 99, 0b10110111},
		{1<<63 | 1, 1, 1 << 63},
	}
	for _, c := range cases {
		if got := topKPow2(c.v, c.k); got != c.want {
			t.Fatalf("topKPow2(%b,%d)=%b want %b", c.v, c.k, got, c.want)
		}
	}
}

func TestDecomposePow2(t *testing.T) {
	if got := decomposePow2(0); len(got) != 0 {
		t.Fatalf("0 应无分解: %v", got)
	}
	got := decomposePow2(255)
	if len(got) != 8 || got[0] != 1 || got[7] != 128 {
		t.Fatalf("255 分解错误: %v", got)
	}
	var sum uint64
	for _, v := range got {
		if v&(v-1) != 0 {
			t.Fatalf("非 2 的幂: %d", v)
		}
		sum += v
	}
	if sum != 255 {
		t.Fatalf("分解不守恒: %d", sum)
	}
}

// splitInvariants 跑 planSplit 并断言通用不变量：Σ输出==total、每输出非零
// 2 的幂、快照 paid 与实际输出逐地址一致；返回 (credits, paid, fee) 便于各场景断言。
func splitInvariants(t *testing.T, cfg Config, height, total uint64, weights map[string]float64, carry map[string]uint64) (map[string]uint64, map[string]uint64, uint64) {
	t.Helper()
	outputs, credits := planSplit(cfg, height, total, weights, carry)
	var sum uint64
	feePaid := uint64(0)
	paidByAddr := map[string]uint64{}
	for _, o := range outputs {
		if o.Value == 0 || o.Value&(o.Value-1) != 0 {
			t.Fatalf("输出非零 2 的幂: %+v", o)
		}
		if len(o.Salt) != 64 || len(o.Address) != 64 {
			t.Fatalf("输出字段非法: %+v", o)
		}
		sum += o.Value
		if o.Address == cfg.PoolAddress {
			feePaid += o.Value
		} else {
			paidByAddr[o.Address] += o.Value
		}
	}
	if sum != total {
		t.Fatalf("Σ输出 %d ≠ total %d", sum, total)
	}
	creditByAddr := map[string]uint64{}
	for _, c := range credits {
		cs, err := accounting.ParseAmount(c.Credit, cfg.Decimals)
		if err != nil {
			t.Fatalf("credit 金额非法: %+v", c)
		}
		ps, err := accounting.ParseAmount(c.Paid, cfg.Decimals)
		if err != nil {
			t.Fatalf("paid 金额非法: %+v", c)
		}
		creditByAddr[c.Address] = uint64(cs)
		if uint64(ps) != paidByAddr[c.Address] {
			t.Fatalf("%s 快照 paid=%d 与输出 %d 不符", c.Address, ps, paidByAddr[c.Address])
		}
	}
	return creditByAddr, paidByAddr, feePaid
}

var (
	addrA = "aa" + repeat62("a")
	addrB = "bb" + repeat62("b")
	addrF = "ff" + repeat62("f")
)

func repeat62(s string) string {
	out := ""
	for len(out) < 62 {
		out += s
	}
	return out[:62]
}

func baseCfg() Config {
	return Config{FeePercent: 5, MaxCoinsPerMiner: 6, MaxPayoutMiners: 300, PoolAddress: addrF, Decimals: 0}
}

func TestPlanSplitBasic(t *testing.T) {
	cfg := baseCfg()
	credits, paid, fee := splitInvariants(t, cfg, 100, 10000,
		map[string]float64{addrA: 3, addrB: 1}, nil)
	// D=9500；cA=7125 cB=2375（float 比例 floor）
	if credits[addrA] != 7125 || credits[addrB] != 2375 {
		t.Fatalf("credits: %v", credits)
	}
	// paid = top-6 二进制项 ≤ credit；费吸收残差
	if paid[addrA] > 7125 || paid[addrB] > 2375 || paid[addrA] == 0 || paid[addrB] == 0 {
		t.Fatalf("paid: %v", paid)
	}
	if fee != 10000-paid[addrA]-paid[addrB] {
		t.Fatalf("费未吸收残差: fee=%d paid=%v", fee, paid)
	}
}

func TestPlanSplitDustThreshold(t *testing.T) {
	cfg := baseCfg()
	cfg.MinPayoutUnits = 5000
	credits, paid, _ := splitInvariants(t, cfg, 100, 10000,
		map[string]float64{addrA: 3, addrB: 1}, nil)
	if paid[addrB] != 0 {
		t.Fatalf("B(2375) 低于阈值应结转: %v", paid)
	}
	if credits[addrB] != 2375 {
		t.Fatalf("B 的 credit 必须照记（结转靠账本）: %v", credits)
	}
	if paid[addrA] == 0 {
		t.Fatalf("A(7125) 过阈值应实付: %v", paid)
	}
}

func TestPlanSplitCarry(t *testing.T) {
	cfg := baseCfg()
	cfg.FeePercent = 20 // 留足费侧余量给 carry 兑付（D=8000，A 全付后剩 2000）
	// B 无窗口权重但有 carry ≥ 阈值 → 也兑付（credit=0, paid=carry）
	credits, paid, _ := splitInvariants(t, cfg, 100, 10000,
		map[string]float64{addrA: 1}, map[string]uint64{addrB: 512})
	if credits[addrB] != 0 {
		t.Fatalf("离场矿工 credit 应为 0: %v", credits)
	}
	if paid[addrB] != 512 {
		t.Fatalf("carry 应全额兑付: %v", paid)
	}
	if paid[addrA] == 0 {
		t.Fatalf("A 应照常实付: %v", paid)
	}
}

func TestPlanSplitBudgetCap(t *testing.T) {
	cfg := baseCfg()
	// carry 大户（> total）一次兑付必须被本块预算封顶：Σ输出仍 == total
	_, paid, fee := splitInvariants(t, cfg, 100, 10000,
		map[string]float64{addrA: 1}, map[string]uint64{addrB: 1 << 40})
	if paid[addrA]+paid[addrB]+fee != 10000 {
		t.Fatalf("预算封顶破坏: %v fee=%d", paid, fee)
	}
}

func TestPlanSplitMaxMiners(t *testing.T) {
	cfg := baseCfg()
	cfg.MaxPayoutMiners = 1
	credits, paid, _ := splitInvariants(t, cfg, 100, 10000,
		map[string]float64{addrA: 3, addrB: 1}, nil)
	if paid[addrA] == 0 || paid[addrB] != 0 {
		t.Fatalf("超员应只付权重最大者: %v", paid)
	}
	if credits[addrB] == 0 {
		t.Fatalf("被截断矿工 credit 照记: %v", credits)
	}
}

func TestPlanSplitEmptyWindow(t *testing.T) {
	cfg := baseCfg()
	credits, _, fee := splitInvariants(t, cfg, 100, 10000, nil, nil)
	if len(credits) != 0 {
		t.Fatalf("空窗口不应有 credits: %v", credits)
	}
	if fee != 10000 {
		t.Fatalf("空窗口应全额归费: %d", fee)
	}
}
