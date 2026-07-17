package stratum

import (
	"fmt"
	"testing"
	"time"
)

func TestRejectLogLazyCleanupAndHardCap(t *testing.T) {
	m := NewManager("test", nil)
	old := time.Now().Add(-2 * time.Minute)
	for i := 0; i < rejectLogMaxEntries; i++ {
		m.rejectLog[fmt.Sprintf("old-%d", i)] = rejectLogWindow{start: old, count: 1}
	}
	if !m.allowRejectLog("new-ip", 1) {
		t.Fatal("清理后新 IP 的首条日志应允许")
	}
	if len(m.rejectLog) != 1 {
		t.Fatalf("过期窗口应被惰性清理，仅保留新窗口；got=%d", len(m.rejectLog))
	}
	if _, ok := m.rejectLog["new-ip"]; !ok {
		t.Fatal("新窗口未写入")
	}

	// 全部活跃时也必须淘汰最老项，不能突破硬上限。
	m.rejectLog = make(map[string]rejectLogWindow, rejectLogMaxEntries)
	now := time.Now()
	for i := 0; i < rejectLogMaxEntries; i++ {
		m.rejectLog[fmt.Sprintf("live-%d", i)] = rejectLogWindow{start: now.Add(time.Duration(i) * time.Nanosecond)}
	}
	_ = m.allowRejectLog("overflow-ip", 1)
	if got := len(m.rejectLog); got != rejectLogMaxEntries {
		t.Fatalf("rejectLog 硬上限失效: got=%d want=%d", got, rejectLogMaxEntries)
	}
	if _, ok := m.rejectLog["overflow-ip"]; !ok {
		t.Fatal("满容量时应淘汰最老窗口并写入新 IP")
	}
}
