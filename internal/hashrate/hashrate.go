// Package hashrate 算力统计采样器（docs/01 R6、docs/02 §5）。
//
// 铁律：算力窗口与 PPLNS 窗口彻底分开（pitfall C6）——本包只做统计，不碰会计。
// 口径：一条难度 D 的 share 平均代表 D × multiplier 次哈希（bitcoin 系 multiplier=2^32），
// hashrate = Σdiff × multiplier / 窗口秒数。全是实测 share 推出的真实口径，无估算。
//
// 两套存储：
//   - recent：精确滚动窗口（默认 10min），即时算力/矿工榜/自查用；
//   - buckets：10min 桶保留 24h（144 点），曲线 API 用。
package hashrate

import (
	"sort"
	"sync"
	"time"
)

// DefaultMultiplier bitcoin 系难度 1 share 的期望哈希数（2^32）。
const DefaultMultiplier = 4294967296.0

// Config 采样器参数（零值取默认）。
type Config struct {
	Multiplier float64       // 每难度哈希数，默认 2^32
	Window     time.Duration // 即时算力滚动窗口，默认 10min
	BucketSize time.Duration // 曲线桶粒度，默认 10min
	Retain     time.Duration // 曲线保留时长，默认 24h
}

func (c *Config) fill() {
	if c.Multiplier <= 0 {
		c.Multiplier = DefaultMultiplier
	}
	if c.Window <= 0 {
		c.Window = 10 * time.Minute
	}
	if c.BucketSize <= 0 {
		c.BucketSize = 10 * time.Minute
	}
	if c.Retain <= 0 {
		c.Retain = 24 * time.Hour
	}
}

// shareRec 滚动窗口里的一条 share。
type shareRec struct {
	addr   string
	worker string
	diff   float64
	at     time.Time
}

// workerAgg 桶内 per-worker 累计。
type workerAgg struct {
	sumDiff float64
	shares  int64
}

// bucket 一个曲线桶：Σdiff + per-矿工 per-worker 明细。
type bucket struct {
	start   time.Time
	sumDiff float64
	shares  int64
	miners  map[string]map[string]*workerAgg // addr → worker → agg
}

// Tracker 每币一个。并发安全。
type Tracker struct {
	mu      sync.Mutex
	cfg     Config
	recent  []shareRec        // 按时间递增
	buckets map[int64]*bucket // bucketStartUnix → bucket
}

func New(cfg Config) *Tracker {
	cfg.fill()
	return &Tracker{cfg: cfg, buckets: map[int64]*bucket{}}
}

// Record 记一条 accepted/block share（由 coininstance 的 onShare 回调驱动）。
func (t *Tracker) Record(addr, worker string, diff float64, at time.Time) {
	if worker == "" {
		worker = "default"
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.recent = append(t.recent, shareRec{addr: addr, worker: worker, diff: diff, at: at})
	t.pruneLocked(at)

	bs := at.Truncate(t.cfg.BucketSize)
	b := t.buckets[bs.Unix()]
	if b == nil {
		b = &bucket{start: bs, miners: map[string]map[string]*workerAgg{}}
		t.buckets[bs.Unix()] = b
	}
	b.sumDiff += diff
	b.shares++
	mw := b.miners[addr]
	if mw == nil {
		mw = map[string]*workerAgg{}
		b.miners[addr] = mw
	}
	wa := mw[worker]
	if wa == nil {
		wa = &workerAgg{}
		mw[worker] = wa
	}
	wa.sumDiff += diff
	wa.shares++
}

// pruneLocked 裁剪滚动窗口与过期桶。
func (t *Tracker) pruneLocked(now time.Time) {
	cut := now.Add(-t.cfg.Window)
	i := 0
	for i < len(t.recent) && t.recent[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		t.recent = append(t.recent[:0], t.recent[i:]...)
	}
	bcut := now.Add(-t.cfg.Retain).Unix()
	for k := range t.buckets {
		if k < bcut {
			delete(t.buckets, k)
		}
	}
}

// Snapshot 即时算力快照。
type Snapshot struct {
	Hashrate        float64
	SharesPerSecond float64
	Miners          int
	Workers         int
}

// MinerSnapshot 单矿工即时快照（Workers 按 worker 名拆分）。
type MinerSnapshot struct {
	Addr            string
	Hashrate        float64
	SharesPerSecond float64
	Workers         map[string]Snapshot
}

func (t *Tracker) windowSec() float64 { return t.cfg.Window.Seconds() }

// Pool 池级即时算力（滚动窗口）。
func (t *Tracker) Pool(now time.Time) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	var sum float64
	miners := map[string]bool{}
	workers := map[string]bool{}
	for _, r := range t.recent {
		sum += r.diff
		miners[r.addr] = true
		workers[r.addr+"."+r.worker] = true
	}
	w := t.windowSec()
	return Snapshot{
		Hashrate:        sum * t.cfg.Multiplier / w,
		SharesPerSecond: float64(len(t.recent)) / w,
		Miners:          len(miners),
		Workers:         len(workers),
	}
}

// Miners 矿工榜（按算力降序）。
func (t *Tracker) Miners(now time.Time) []MinerSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	byAddr := map[string]*MinerSnapshot{}
	counts := map[string]int{}
	for _, r := range t.recent {
		m := byAddr[r.addr]
		if m == nil {
			m = &MinerSnapshot{Addr: r.addr, Workers: map[string]Snapshot{}}
			byAddr[r.addr] = m
		}
		m.Hashrate += r.diff // 先累 Σdiff，最后统一换算
		counts[r.addr]++
		ws := m.Workers[r.worker]
		ws.Hashrate += r.diff
		ws.SharesPerSecond++
		m.Workers[r.worker] = ws
	}
	w := t.windowSec()
	out := make([]MinerSnapshot, 0, len(byAddr))
	for addr, m := range byAddr {
		m.Hashrate = m.Hashrate * t.cfg.Multiplier / w
		m.SharesPerSecond = float64(counts[addr]) / w
		for name, ws := range m.Workers {
			ws.Hashrate = ws.Hashrate * t.cfg.Multiplier / w
			ws.SharesPerSecond = ws.SharesPerSecond / w
			m.Workers[name] = ws
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hashrate != out[j].Hashrate {
			return out[i].Hashrate > out[j].Hashrate
		}
		return out[i].Addr < out[j].Addr
	})
	return out
}

// Miner 单矿工即时快照；无 share 时 ok=false。
func (t *Tracker) Miner(addr string, now time.Time) (MinerSnapshot, bool) {
	for _, m := range t.Miners(now) {
		if m.Addr == addr {
			return m, true
		}
	}
	return MinerSnapshot{}, false
}

// Sample 曲线上的一个点（一个桶）。
type Sample struct {
	Created         time.Time
	Hashrate        float64
	SharesPerSecond float64
	Workers         map[string]Snapshot // 池级曲线为 nil；矿工曲线含 per-worker
}

// PoolSamples 池 24h 算力曲线（按时间升序；只含有 share 的桶——无数据不造 0 点）。
func (t *Tracker) PoolSamples(now time.Time) []Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	sec := t.cfg.BucketSize.Seconds()
	out := make([]Sample, 0, len(t.buckets))
	for _, b := range t.buckets {
		out = append(out, Sample{
			Created:         b.start,
			Hashrate:        b.sumDiff * t.cfg.Multiplier / sec,
			SharesPerSecond: float64(b.shares) / sec,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// MinerSamples 单矿工 24h 曲线（含 per-worker 拆分，miningcore performanceSamples 形状）。
func (t *Tracker) MinerSamples(addr string, now time.Time) []Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	sec := t.cfg.BucketSize.Seconds()
	var out []Sample
	for _, b := range t.buckets {
		mw := b.miners[addr]
		if mw == nil {
			continue
		}
		s := Sample{Created: b.start, Workers: map[string]Snapshot{}}
		for name, wa := range mw {
			hs := wa.sumDiff * t.cfg.Multiplier / sec
			sps := float64(wa.shares) / sec
			s.Workers[name] = Snapshot{Hashrate: hs, SharesPerSecond: sps}
			s.Hashrate += hs
			s.SharesPerSecond += sps
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
