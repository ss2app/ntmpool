package config

import "testing"

func TestPortSecurityDefaults(t *testing.T) {
	p := WithPortDefaults(PortConfig{})
	tests := []struct {
		name      string
		got, want int
	}{
		{"message", p.MessageMaxBytes, 32 * 1024},
		{"json depth", p.JSONMaxDepth, 16},
		{"unauth messages", p.UnauthMaxMessages, 32},
		{"unauth bytes", p.UnauthMaxBytes, 128 * 1024},
		{"handshake", p.HandshakeTimeoutSec, 10},
		{"authorize", p.AuthorizeTimeoutSec, 30},
		{"pow concurrency", p.PowVerifyConcurrency, 4},
		{"pow queue", p.PowVerifyQueue, 64},
		{"collapse min", p.CollapseMinConnections, 20},
		{"collapse percent", p.CollapsePercent, 90},
		{"reject log rate", p.BanRejectLogPerMinute, 5},
		{"autoban samples", p.AutoBanMinSamples, 10},
		{"autoban percent", p.AutoBanPercent, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got=%d want=%d", tt.got, tt.want)
			}
		})
	}
}

func TestSecurityConfigValidation(t *testing.T) {
	base := Config{ProtectedCIDRs: []string{"10.0.0.0/24"}, Coins: []CoinConfig{{
		ID: "c", Ports: []PortConfig{{Port: 1}},
	}}}
	if err := base.Validate(); err != nil {
		t.Fatalf("默认值应通过: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"非法 protected CIDR", func(c *Config) { c.ProtectedCIDRs = []string{"bad"} }},
		{"消息超过硬上限", func(c *Config) { c.Coins[0].Ports[0].MessageMaxBytes = HardMessageMaxBytes + 1 }},
		{"JSON 深度负数", func(c *Config) { c.Coins[0].Ports[0].JSONMaxDepth = -1 }},
		{"坍缩比例越界", func(c *Config) { c.Coins[0].Ports[0].CollapsePercent = 101 }},
		{"PoW 并发负数", func(c *Config) { c.Coins[0].Ports[0].PowVerifyConcurrency = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			c.ProtectedCIDRs = append([]string(nil), base.ProtectedCIDRs...)
			c.Coins = append([]CoinConfig(nil), base.Coins...)
			c.Coins[0].Ports = append([]PortConfig(nil), base.Coins[0].Ports...)
			tt.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("非法配置应被拒绝")
			}
		})
	}
}
