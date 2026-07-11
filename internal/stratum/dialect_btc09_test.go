package stratum

import "testing"

// parseBtc09Login：NTMminer legacy "ntmminer" 兜底视同未设置 + ".worker" 后缀 + default 回退。
func TestParseBtc09Login(t *testing.T) {
	cases := []struct {
		addr, worker string
		wantAddr     string
		wantWorker   string
	}{
		// NTMminer ≤v1.13 未设 --worker：worker="ntmminer" → default
		{"4kAddr", "ntmminer", "4kAddr", "default"},
		{"4kAddr", "NTMminer", "4kAddr", "default"}, // 大小写不敏感
		// legacy 兜底 + 地址后缀：矿工不换锄头也能命名
		{"4kAddr.rig1", "ntmminer", "4kAddr", "rig1"},
		// 显式 worker 字段优先于后缀
		{"4kAddr.rig1", "myrig", "4kAddr", "myrig"},
		// 纯后缀
		{"4kAddr.rig2", "", "4kAddr", "rig2"},
		// 全没有
		{"4kAddr", "", "4kAddr", "default"},
		// 真起名叫 ntmminer2 的不受影响
		{"4kAddr", "ntmminer2", "4kAddr", "ntmminer2"},
	}
	for _, c := range cases {
		a, w := parseBtc09Login(c.addr, c.worker)
		if a != c.wantAddr || w != c.wantWorker {
			t.Errorf("parseBtc09Login(%q,%q) = (%q,%q)，期望 (%q,%q)",
				c.addr, c.worker, a, w, c.wantAddr, c.wantWorker)
		}
	}
}
