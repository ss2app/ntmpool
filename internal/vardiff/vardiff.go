// Package vardiff 实现自适应共享难度。
//
// 这是 midstate/btx 实战打磨过的完整设计，内建「三件套」（GPU 批量提交币的必备项，
// 详见 _knowledge/pitfalls/gpu-提交效率-vardiff-straddle.md）：
//  1. 一步难度 grace：share 满足 current 或（retarget 后一个目标周期内）previous
//     难度即接受，按【实际满足的难度】计 PPLNS 权重；
//  2. retarget 限频：两次调整至少间隔一个目标周期，保证在飞的一批工作至多跨一次
//     retarget → 一步 grace 必然够用，同时削超调；
//  3. 分类归属：过不了 grace 的只算 lowdiff（良性），绝不混进 badpow。
package vardiff

import (
	"sync"
	"time"
)

// Config 每端口一份（低难度端口 = 低 StartDiff + 低 MaxDiff）。热更新对新连接生效。
type Config struct {
	StartDiff      float64       // 新连接初始难度
	MinDiff        float64       // 下限
	MaxDiff        float64       // 上限（0 = 不设上限）
	TargetInterval time.Duration // 期望的 share 间隔（如 12s）
	RetargetEvery  time.Duration // retarget 最小间隔；0 则取 TargetInterval
	DeadZone       float64       // 死区倍率（如 1.8）：估算难度在 cur/DeadZone ~ cur*DeadZone 内不动
	MaxDelta       float64       // 单次调整最大倍率（如 4.0），防超调
	MinSamples     int           // 至少积累多少个 share 才允许 retarget
	EmaAlpha       float64       // share 间隔 EMA 系数（如 0.3）
}

// Norm 补默认值。
func (c Config) Norm() Config {
	if c.RetargetEvery <= 0 {
		c.RetargetEvery = c.TargetInterval
	}
	if c.DeadZone < 1.01 {
		c.DeadZone = 1.8
	}
	if c.MaxDelta < 1.01 {
		c.MaxDelta = 4.0
	}
	if c.MinSamples <= 0 {
		c.MinSamples = 3
	}
	if c.EmaAlpha <= 0 || c.EmaAlpha > 1 {
		c.EmaAlpha = 0.3
	}
	return c
}

// State 每连接一份。
type State struct {
	mu           sync.Mutex
	cfg          Config
	cur          float64
	prev         float64   // 上一档难度（一步 grace 用）
	lastRetarget time.Time // 上次 retarget 时刻
	lastShare    time.Time
	emaInterval  float64 // 秒
	samples      int
}

func New(cfg Config, now time.Time) *State {
	cfg = cfg.Norm()
	return &State{
		cfg:          cfg,
		cur:          cfg.StartDiff,
		prev:         cfg.StartDiff,
		lastRetarget: now,
		lastShare:    now,
	}
}

// Current 当前难度（推给矿工的 set_difficulty / job target）。
func (s *State) Current() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Judge 判定一条 share 的难度归属（不含 badpow —— 共识校验在调用方先做）。
// shareDiff 是该 share 实际达到的难度。
// 返回 creditDiff：应计入权重的难度；ok=false 表示 lowdiff。
//
// 一步 grace：retarget 后一个目标周期内，满足 prev 难度也接受（按 prev 计权）。
func (s *State) Judge(shareDiff float64, now time.Time) (creditDiff float64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shareDiff >= s.cur {
		return s.cur, true
	}
	inGrace := now.Sub(s.lastRetarget) <= s.cfg.RetargetEvery
	if inGrace && s.prev < s.cur && shareDiff >= s.prev {
		return s.prev, true
	}
	return 0, false
}

// OnAccepted 在 share 被接受后调用，更新间隔 EMA。
func (s *State) OnAccepted(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := now.Sub(s.lastShare).Seconds()
	if dt < 0 {
		dt = 0
	}
	s.lastShare = now
	if s.samples == 0 {
		s.emaInterval = dt
	} else {
		a := s.cfg.EmaAlpha
		s.emaInterval = a*dt + (1-a)*s.emaInterval
	}
	s.samples++
}

// MaybeRetarget 判断是否需要调难度。返回 (newDiff, changed)。
// 满足限频 + 样本数 + 越出死区 才动；单次调整夹在 MaxDelta 内；夹 Min/Max。
// 长时间无 share（例如机器太弱难度过高）也会触发降档。
func (s *State) MaybeRetarget(now time.Time) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if now.Sub(s.lastRetarget) < s.cfg.RetargetEvery {
		return s.cur, false
	}

	var est float64
	idle := now.Sub(s.lastShare)
	switch {
	case s.samples >= s.cfg.MinSamples && s.emaInterval > 0:
		// 估算能让间隔回到目标值的难度
		est = s.cur * s.cfg.TargetInterval.Seconds() / s.emaInterval
	case idle > 4*s.cfg.TargetInterval:
		// 完全交不上 share：强制降档
		est = s.cur / s.cfg.MaxDelta
	default:
		return s.cur, false
	}

	// 死区：小波动不动，减少矿工侧难度抖动
	if est > s.cur/s.cfg.DeadZone && est < s.cur*s.cfg.DeadZone {
		return s.cur, false
	}
	// 单步夹逼
	if est > s.cur*s.cfg.MaxDelta {
		est = s.cur * s.cfg.MaxDelta
	}
	if est < s.cur/s.cfg.MaxDelta {
		est = s.cur / s.cfg.MaxDelta
	}
	// 全局夹逼
	if est < s.cfg.MinDiff {
		est = s.cfg.MinDiff
	}
	if s.cfg.MaxDiff > 0 && est > s.cfg.MaxDiff {
		est = s.cfg.MaxDiff
	}
	if est == s.cur {
		return s.cur, false
	}

	s.prev = s.cur
	s.cur = est
	s.lastRetarget = now
	s.samples = 0 // 新档位重新积累样本
	return s.cur, true
}
