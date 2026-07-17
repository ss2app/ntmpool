package banlist

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var b0 = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

func TestBanUnbanAndCIDR(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return b0 }

	if err := l.Ban("1.2.3.4", "手工", 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Ban("10.0.0.0/24", "网段", 0); err != nil {
		t.Fatal(err)
	}
	if banned, reason := l.Banned("1.2.3.4"); !banned || reason != "手工" {
		t.Fatalf("单 IP 应命中: %v %q", banned, reason)
	}
	if banned, _ := l.Banned("10.0.0.99"); !banned {
		t.Fatal("CIDR 应命中")
	}
	if banned, _ := l.Banned("10.0.1.1"); banned {
		t.Fatal("网段外不应命中")
	}
	if err := l.Unban("1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if banned, _ := l.Banned("1.2.3.4"); banned {
		t.Fatal("解 ban 后不应命中")
	}
	if err := l.Ban("not-an-ip", "x", 0); err == nil {
		t.Fatal("非法 target 应报错")
	}
}

func TestTTLExpiry(t *testing.T) {
	l, _ := New("")
	now := b0
	l.now = func() time.Time { return now }
	_ = l.Ban("5.6.7.8", "退避", 10*time.Minute)
	if banned, _ := l.Banned("5.6.7.8"); !banned {
		t.Fatal("TTL 内应命中")
	}
	now = b0.Add(11 * time.Minute)
	if banned, _ := l.Banned("5.6.7.8"); banned {
		t.Fatal("过期应自动失效")
	}
	if len(l.Entries()) != 0 {
		t.Fatalf("过期条目应清理: %+v", l.Entries())
	}
}

func TestStrikesEscalation(t *testing.T) {
	l, _ := New("")
	l.now = func() time.Time { return b0 }
	_ = l.Ban("9.9.9.9", "auto", time.Minute)
	_ = l.Ban("9.9.9.9", "auto", 2*time.Minute) // 再犯
	if l.Strikes("9.9.9.9") != 1 {
		t.Fatalf("Strikes=%d want 1", l.Strikes("9.9.9.9"))
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.json")
	l1, _ := New(path)
	l1.now = func() time.Time { return b0 }
	_ = l1.Ban("2.2.2.2", "persist", 0)
	_ = l1.Ban("192.168.0.0/16", "lan", 0)

	l2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	l2.now = func() time.Time { return b0 }
	if banned, _ := l2.Banned("2.2.2.2"); !banned {
		t.Fatal("重载后单 IP 应命中")
	}
	if banned, _ := l2.Banned("192.168.5.5"); !banned {
		t.Fatal("重载后 CIDR 应命中")
	}
	if len(l2.Entries()) != 2 {
		t.Fatalf("条目数=%d", len(l2.Entries()))
	}
}

func TestProtectedTargetsRejectBan(t *testing.T) {
	tests := []struct {
		name      string
		protected []string
		target    string
		wantErr   bool
	}{
		{"单 IP", []string{"10.0.0.2"}, "10.0.0.2", true},
		{"CIDR 内 IP", []string{"10.0.0.0/24"}, "10.0.0.99", true},
		{"目标 CIDR 覆盖保护 IP", []string{"10.0.0.2"}, "10.0.0.0/24", true},
		{"不相交", []string{"10.0.0.0/24"}, "10.0.1.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := New("", tt.protected...)
			if err != nil {
				t.Fatal(err)
			}
			err = l.Ban(tt.target, "auto: test", time.Minute)
			if errors.Is(err, ErrProtected) != tt.wantErr {
				t.Fatalf("err=%v wantProtected=%v", err, tt.wantErr)
			}
			if tt.wantErr && len(l.Entries()) != 0 {
				t.Fatalf("受保护目标写入了 ban: %+v", l.Entries())
			}
		})
	}
}

func TestSetProtectedPurgesExistingBan(t *testing.T) {
	l, _ := New("")
	_ = l.Ban("192.0.2.10", "legacy", 0)
	if err := l.SetProtected([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if !l.IsProtected("192.0.2.10") {
		t.Fatal("应命中受保护名单")
	}
	if banned, _ := l.Banned("192.0.2.10"); banned {
		t.Fatal("新增保护规则应摘除历史 ban")
	}
}
