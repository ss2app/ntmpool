package hashrate

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

func approx(a, b float64) bool {
	if b == 0 {
		return math.Abs(a) < 1e-9
	}
	return math.Abs(a-b)/math.Abs(b) < 1e-9
}

// 算力数学：Σdiff × 2^32 / 窗口秒。
func TestPoolHashrateMath(t *testing.T) {
	tr := New(Config{Window: 600 * time.Second})
	// 10 条 diff=1024 的 share 落在窗口内
	for i := 0; i < 10; i++ {
		tr.Record("addrA", "rig1", 1024, t0.Add(time.Duration(i)*time.Second))
	}
	snap := tr.Pool(t0.Add(30 * time.Second))
	want := 10 * 1024 * DefaultMultiplier / 600.0
	if !approx(snap.Hashrate, want) {
		t.Fatalf("Hashrate=%v want %v", snap.Hashrate, want)
	}
	if !approx(snap.SharesPerSecond, 10.0/600.0) {
		t.Fatalf("SharesPerSecond=%v", snap.SharesPerSecond)
	}
	if snap.Miners != 1 || snap.Workers != 1 {
		t.Fatalf("Miners=%d Workers=%d", snap.Miners, snap.Workers)
	}
}

// 滚动窗口：窗口外 share 被剔除。
func TestWindowPrune(t *testing.T) {
	tr := New(Config{Window: 600 * time.Second})
	tr.Record("a", "w", 100, t0)
	tr.Record("a", "w", 100, t0.Add(650*time.Second)) // 第二条把第一条挤出窗口
	snap := tr.Pool(t0.Add(660 * time.Second))
	want := 100 * DefaultMultiplier / 600.0
	if !approx(snap.Hashrate, want) {
		t.Fatalf("Hashrate=%v want %v（窗口外 share 未剔除？）", snap.Hashrate, want)
	}
}

// 矿工榜：降序 + per-worker 拆分。
func TestMinersAndWorkers(t *testing.T) {
	tr := New(Config{Window: 600 * time.Second})
	tr.Record("big", "rig1", 3000, t0)
	tr.Record("big", "rig2", 1000, t0)
	tr.Record("small", "solo", 500, t0)
	ms := tr.Miners(t0.Add(time.Second))
	if len(ms) != 2 || ms[0].Addr != "big" || ms[1].Addr != "small" {
		t.Fatalf("榜单错: %+v", ms)
	}
	if len(ms[0].Workers) != 2 {
		t.Fatalf("big 应有 2 个 worker: %+v", ms[0].Workers)
	}
	wantBig := 4000 * DefaultMultiplier / 600.0
	if !approx(ms[0].Hashrate, wantBig) {
		t.Fatalf("big Hashrate=%v want %v", ms[0].Hashrate, wantBig)
	}
	rig1 := ms[0].Workers["rig1"]
	if !approx(rig1.Hashrate, 3000*DefaultMultiplier/600.0) {
		t.Fatalf("rig1=%v", rig1.Hashrate)
	}
	if _, ok := tr.Miner("nobody", t0); ok {
		t.Fatal("不存在的矿工不应命中")
	}
}

// 曲线桶：10min 归桶、24h 裁剪、矿工曲线含 worker 拆分。
func TestSamples(t *testing.T) {
	tr := New(Config{Window: 600 * time.Second, BucketSize: 600 * time.Second, Retain: 24 * time.Hour})
	tr.Record("a", "w1", 600, t0)                     // 桶 1
	tr.Record("a", "w1", 600, t0.Add(1*time.Minute))  // 桶 1
	tr.Record("a", "w2", 300, t0.Add(11*time.Minute)) // 桶 2
	now := t0.Add(12 * time.Minute)

	ps := tr.PoolSamples(now)
	if len(ps) != 2 {
		t.Fatalf("应有 2 个桶: %+v", ps)
	}
	if !approx(ps[0].Hashrate, 1200*DefaultMultiplier/600.0) {
		t.Fatalf("桶1=%v", ps[0].Hashrate)
	}

	msamp := tr.MinerSamples("a", now)
	if len(msamp) != 2 {
		t.Fatalf("矿工曲线应 2 点: %+v", msamp)
	}
	if _, ok := msamp[1].Workers["w2"]; !ok {
		t.Fatalf("桶2 应含 w2: %+v", msamp[1].Workers)
	}

	// 25h 后全部过期
	if got := tr.PoolSamples(t0.Add(25 * time.Hour)); len(got) != 0 {
		t.Fatalf("24h 外桶未裁剪: %+v", got)
	}
}

// 空 worker 名归入 default。
func TestDefaultWorker(t *testing.T) {
	tr := New(Config{})
	tr.Record("a", "", 10, t0)
	m, ok := tr.Miner("a", t0.Add(time.Second))
	if !ok {
		t.Fatal("矿工应存在")
	}
	if _, ok := m.Workers["default"]; !ok {
		t.Fatalf("空 worker 应归 default: %+v", m.Workers)
	}
}
