package minersettings

import (
	"path/filepath"
	"testing"
)

func TestParsePassword(t *testing.T) {
	cases := []struct {
		in   string
		want PasswordParams
	}{
		{"x", PasswordParams{}},
		{"d=8192", PasswordParams{FixedDiff: 8192}},
		{"mp=21", PasswordParams{MinPayout: 21}},
		{"pl=5", PasswordParams{MinPayout: 5}}, // yiimp 系兼容
		{"secret,mp=21,d=1024", PasswordParams{Password: "secret", MinPayout: 21, FixedDiff: 1024}},
		{" D=16 , MP=0.5 ", PasswordParams{FixedDiff: 16, MinPayout: 0.5}},
		{"x,unknown=1,mp=3", PasswordParams{MinPayout: 3}},
	}
	for _, c := range cases {
		if got := ParsePassword(c.in); got != c.want {
			t.Fatalf("ParsePassword(%q)=%+v want %+v", c.in, got, c.want)
		}
	}
}

func TestApplyPasswordBindingAndOverride(t *testing.T) {
	s, _ := New("", []byte("salt"))
	s.MaxMinPayout = 1000

	// 无密码设 mp：允许（未绑定前先用先得），下限=池默认 0.01
	if _, err := s.ApplyPassword("c", "addr1", ParsePassword("mp=0.001"), 0.01); err != nil {
		t.Fatal(err)
	}
	if mp := s.MinPayouts("c")["addr1"]; mp != 0.01 {
		t.Fatalf("应被抬到池默认下限: %v", mp)
	}

	// 带密码绑定 + 设 mp=21
	note, err := s.ApplyPassword("c", "addr1", ParsePassword("secret,mp=21"), 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Fatal("绑定密码应有 note")
	}
	if mp := s.MinPayouts("c")["addr1"]; mp != 21 {
		t.Fatalf("mp=%v want 21", mp)
	}

	// 错密码改设置 → 拒绝且不变
	if _, err := s.ApplyPassword("c", "addr1", ParsePassword("wrong,mp=99"), 0.01); err == nil {
		t.Fatal("错密码应被拒")
	}
	if mp := s.MinPayouts("c")["addr1"]; mp != 21 {
		t.Fatalf("错密码后设置不应变: %v", mp)
	}

	// 对密码可改
	if _, err := s.ApplyPassword("c", "addr1", ParsePassword("secret,mp=50"), 0.01); err != nil {
		t.Fatal(err)
	}
	if mp := s.MinPayouts("c")["addr1"]; mp != 50 {
		t.Fatalf("mp=%v want 50", mp)
	}

	// 超上限 → 拒
	if _, err := s.ApplyPassword("c", "addr1", ParsePassword("secret,mp=99999"), 0.01); err == nil {
		t.Fatal("超上限应被拒")
	}
}

func TestAdminResetBypass(t *testing.T) {
	s, _ := New("", []byte("salt"))
	_, _ = s.ApplyPassword("c", "a", ParsePassword("pwd,mp=100"), 0.01)
	// 面板凭错密码改 → 拒
	if err := s.SetMinPayout("c", "a", "bad", 5, false); err == nil {
		t.Fatal("面板错密码应被拒")
	}
	// 管理后台 bypass 重置
	if err := s.SetMinPayout("c", "a", "", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.MinPayouts("c")["a"]; ok {
		t.Fatal("重置后不应再有覆盖")
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s1, _ := New(path, []byte("salt"))
	_, _ = s1.ApplyPassword("c", "a", ParsePassword("pwd,mp=33"), 0.01)

	s2, err := New(path, []byte("salt"))
	if err != nil {
		t.Fatal(err)
	}
	if mp := s2.MinPayouts("c")["a"]; mp != 33 {
		t.Fatalf("重载 mp=%v want 33", mp)
	}
	// 重载后密码约束仍在
	if _, err := s2.ApplyPassword("c", "a", ParsePassword("wrong,mp=1"), 0.01); err == nil {
		t.Fatal("重载后错密码应被拒")
	}
}
