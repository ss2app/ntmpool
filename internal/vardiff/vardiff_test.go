package vardiff

import (
	"testing"
	"time"
)

func testCfg() Config {
	return Config{
		StartDiff:      1000,
		MinDiff:        100,
		MaxDiff:        100000,
		TargetInterval: 10 * time.Second,
	}
}

// share 交太快 → 难度应上调；且 retarget 后旧难度在 grace 窗内仍被接受（一步 grace）。
func TestRetargetUpAndOneStepGrace(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := New(testCfg(), t0)

	// 每 1s 一个 share（目标 10s）→ 应上调
	now := t0
	for i := 0; i < 12; i++ {
		now = now.Add(1 * time.Second)
		if _, ok := s.Judge(1000, now); !ok {
			t.Fatalf("share %d 应被接受", i)
		}
		s.OnAccepted(now)
	}
	nd, changed := s.MaybeRetarget(now)
	if !changed || nd <= 1000 {
		t.Fatalf("应上调难度, got %v changed=%v", nd, changed)
	}

	// grace 窗内：旧难度 1000 的 share 仍接受，按旧难度计权
	credit, ok := s.Judge(1000, now.Add(1*time.Second))
	if !ok || credit != 1000 {
		t.Fatalf("一步 grace 失效: credit=%v ok=%v", credit, ok)
	}
	// grace 窗外：旧难度 share 变 lowdiff
	if _, ok := s.Judge(1000, now.Add(1*time.Hour)); ok {
		t.Fatal("grace 窗外旧难度 share 应为 lowdiff")
	}
}

// retarget 限频：目标周期内绝不二次调整。
func TestRetargetRateLimit(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := New(testCfg(), t0)
	now := t0
	for i := 0; i < 12; i++ {
		now = now.Add(1 * time.Second)
		s.Judge(1000, now)
		s.OnAccepted(now)
	}
	if _, changed := s.MaybeRetarget(now); !changed {
		t.Fatal("第一次应 retarget")
	}
	for i := 0; i < 5; i++ {
		now = now.Add(500 * time.Millisecond)
		s.Judge(s.Current(), now)
		s.OnAccepted(now)
		if _, changed := s.MaybeRetarget(now); changed {
			t.Fatal("限频期内不应再次 retarget")
		}
	}
}

// 完全交不上 share → 强制降档，且不破 MinDiff。
func TestIdleForcesDown(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := New(testCfg(), t0)
	now := t0.Add(2 * time.Minute) // 远超 4×target 无 share
	nd, changed := s.MaybeRetarget(now)
	if !changed || nd >= 1000 {
		t.Fatalf("空闲应降档, got %v", nd)
	}
	for i := 0; i < 20; i++ {
		now = now.Add(2 * time.Minute)
		nd, _ = s.MaybeRetarget(now)
	}
	if nd < 100 {
		t.Fatalf("不应破 MinDiff, got %v", nd)
	}
}
