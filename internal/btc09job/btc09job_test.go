package btc09job

import (
	"math/big"
	"testing"
)

func TestCompactToTarget(t *testing.T) {
	// 09C 主网 maxTarget：0x1f00ffff → 0xffff << (8*(0x1f-3))
	want := new(big.Int).Lsh(big.NewInt(0xffff), 8*(0x1f-3))
	if got := CompactToTarget(0x1f00ffff); got.Cmp(want) != 0 {
		t.Fatalf("0x1f00ffff: got %x want %x", got, want)
	}
	// regtest：0x207fffff → 0x7fffff << (8*(0x20-3))
	want = new(big.Int).Lsh(big.NewInt(0x7fffff), 8*(0x20-3))
	if got := CompactToTarget(0x207fffff); got.Cmp(want) != 0 {
		t.Fatalf("0x207fffff: got %x want %x", got, want)
	}
}

func TestShareTargetSub1AndClamp(t *testing.T) {
	m := New("t", nil, nil, 0x1f00ffff)
	// sub-1 难度：target = maxTarget / 0.05 = maxTarget × 20
	got := m.shareTargetFor(0.05)
	want := new(big.Int).Mul(m.maxTarget, big.NewInt(20))
	if got.Cmp(want) != 0 {
		t.Fatalf("diff 0.05: got %x want %x", got, want)
	}
	// 超小难度不得溢出 256-bit（wire 编码是 64-hex）
	tiny := m.shareTargetFor(0.00000001)
	if tiny.BitLen() > 256 {
		t.Fatalf("clamp 失效: bitlen=%d", tiny.BitLen())
	}
	// 难度越大目标越小
	if m.shareTargetFor(2).Cmp(m.shareTargetFor(1)) >= 0 {
		t.Fatal("难度单调性破坏")
	}
}

func TestValidateAddress(t *testing.T) {
	// 真实主网地址（老池池钱包地址，公开信息）
	if err := ValidateAddress("4jD5ghtpnHcubrmXDsSHhNfvxW4hnn3bx7"); err != nil {
		t.Fatalf("真实地址被拒: %v", err)
	}
	bad := []string{
		"",
		"4jD5ghtpnHcubrmXDsSHhNfvxW4hnn3bx8",                                   // 尾字符篡改 → 校验和错
		"zs1w29gp72xqya3gjdmqgllr4nqx0uy4gmswvvkxu2t9sc2p3vx0jlxlh8vh26x9h4a",  // 别的币（DRGX zs）
		"0x1234",                                                               // 非 base58
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",                                   // BTC 地址（版本 0x00）
	}
	for _, a := range bad {
		if err := ValidateAddress(a); err == nil {
			t.Fatalf("非法地址 %q 被接受", a)
		}
	}
}

func TestDiffOfHash(t *testing.T) {
	m := New("t", nil, nil, 0x1f00ffff)
	// hash == maxTarget → diff 1
	d := m.diffOfHash(new(big.Int).Set(m.maxTarget))
	if d < 0.999 || d > 1.001 {
		t.Fatalf("diff(maxTarget) = %v, want ≈1", d)
	}
	// hash 是 maxTarget 的 1/2 → diff 2
	half := new(big.Int).Rsh(m.maxTarget, 1)
	d = m.diffOfHash(half)
	if d < 1.999 || d > 2.001 {
		t.Fatalf("diff(maxTarget/2) = %v, want ≈2", d)
	}
}
