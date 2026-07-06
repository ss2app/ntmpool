// Package banlist ban 名单（docs/01 R12）：内存 + JSON 持久化，支持单 IP 与 CIDR。
//
// 使用方：
//   - stratum 接入层：Accept 后立即 Banned(ip) 查询，命中直接断开（连接层最外圈）。
//   - 协议层自动 ban（invalidPercent 阈值/指数退避）：调 Ban 并把退避倍数记在 Strikes。
//   - 管理后台：手工 ban/解 ban/查询。
//
// 全池共享一份（跨币）：攻击者不会只打一个币的端口。
package banlist

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

// Entry 一条 ban 记录。
type Entry struct {
	// Target 单 IP（"1.2.3.4"）或 CIDR（"1.2.3.0/24"）。
	Target string `json:"target"`
	// ExpiresAt 零值 = 永久。
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	// Strikes 自动 ban 的次数（指数退避：下次时长 = base × 2^strikes）。
	Strikes int       `json:"strikes,omitempty"`
	AddedAt time.Time `json:"addedAt"`
	// 解析好的网段（单 IP 也归一为 /32、/128）
	ipnet *net.IPNet
}

// List 并发安全 ban 名单。
type List struct {
	mu      sync.Mutex
	entries map[string]*Entry // target → entry
	path    string            // 持久化文件；空 = 不落盘
	now     func() time.Time
}

// New path 为空则纯内存。文件存在时自动加载（损坏文件报错，不静默吞）。
func New(path string) (*List, error) {
	l := &List{entries: map[string]*Entry{}, path: path, now: time.Now}
	if path == "" {
		return l, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	var stored []*Entry
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, fmt.Errorf("ban 名单文件损坏 %s: %w", path, err)
	}
	for _, e := range stored {
		if n, err := parseTarget(e.Target); err == nil {
			e.ipnet = n
			l.entries[e.Target] = e
		}
	}
	return l, nil
}

// parseTarget 单 IP 归一为 /32（v4）或 /128（v6）。
func parseTarget(target string) (*net.IPNet, error) {
	if _, n, err := net.ParseCIDR(target); err == nil {
		return n, nil
	}
	ip := net.ParseIP(target)
	if ip == nil {
		return nil, fmt.Errorf("非法 IP/CIDR: %q", target)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// Ban 加/续一条 ban。ttl<=0 = 永久。重复 ban 同一 target 会累加 Strikes（退避计数）。
func (l *List) Ban(target, reason string, ttl time.Duration) error {
	n, err := parseTarget(target)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[target]
	if e == nil {
		e = &Entry{Target: target, ipnet: n, AddedAt: l.now()}
		l.entries[target] = e
	} else {
		e.Strikes++
	}
	e.Reason = reason
	if ttl > 0 {
		e.ExpiresAt = l.now().Add(ttl)
	} else {
		e.ExpiresAt = time.Time{}
	}
	return l.saveLocked()
}

// Unban 解除。target 不存在不算错（幂等）。
func (l *List) Unban(target string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, target)
	return l.saveLocked()
}

// Banned 查询 IP 是否被 ban（含 CIDR 命中）。过期条目顺手清理。
func (l *List) Banned(ipStr string) (bool, string) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	dirty := false
	for target, e := range l.entries {
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			delete(l.entries, target)
			dirty = true
			continue
		}
		if e.ipnet != nil && e.ipnet.Contains(ip) {
			return true, e.Reason
		}
	}
	if dirty {
		_ = l.saveLocked()
	}
	return false, ""
}

// Strikes 返回 target 当前退避计数（自动 ban 算下次时长用）；无记录 = 0。
func (l *List) Strikes(target string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[target]; ok {
		return e.Strikes
	}
	return 0
}

// Entries 全部有效条目（管理后台查询；已按 target 排序）。
func (l *List) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	out := make([]Entry, 0, len(l.entries))
	for _, e := range l.entries {
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// saveLocked 持锁落盘（原子写：临时文件 + rename）。
func (l *List) saveLocked() error {
	if l.path == "" {
		return nil
	}
	list := make([]*Entry, 0, len(l.entries))
	for _, e := range l.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Target < list[j].Target })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}
