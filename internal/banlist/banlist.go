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
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

// ErrProtected 表示目标命中受保护名单，调用方必须降级为仅断当前会话。
var ErrProtected = errors.New("目标命中受保护名单")

type protectedRule struct {
	target string
	ipnet  *net.IPNet
}

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
	mu        sync.Mutex
	entries   map[string]*Entry // target → entry
	protected []protectedRule   // 受保护 IP/CIDR；同时供 stratum 判 trusted edge
	path      string            // 持久化文件；空 = 不落盘
	now       func() time.Time
}

// New path 为空则纯内存。文件存在时自动加载（损坏文件报错，不静默吞）。
// protected 为受保护 IP/CIDR；可变参数保持旧调用兼容。
func New(path string, protected ...string) (*List, error) {
	l := &List{entries: map[string]*Entry{}, path: path, now: time.Now}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
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
		}
	}
	if err := l.SetProtected(protected); err != nil {
		return nil, err
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
	if rule, ok := l.protectedMatchLocked(n); ok {
		log.Printf("[P0] ban_protected_rejected target=%q reason=%q protected_rule=%q", target, reason, rule)
		return fmt.Errorf("%w: target=%s rule=%s", ErrProtected, target, rule)
	}
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

// SetProtected 原子替换受保护名单。新规则若覆盖历史 ban，会立即摘除并落盘，
// 保证“受保护目标永远 ban 不进去”在启动加载和后续热更新时都成立。
func (l *List) SetProtected(targets []string) error {
	rules := make([]protectedRule, 0, len(targets))
	for _, target := range targets {
		n, err := parseTarget(target)
		if err != nil {
			return fmt.Errorf("受保护名单: %w", err)
		}
		rules = append(rules, protectedRule{target: target, ipnet: n})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.protected = rules
	dirty := false
	for target, e := range l.entries {
		if rule, ok := l.protectedMatchLocked(e.ipnet); ok {
			delete(l.entries, target)
			dirty = true
			log.Printf("[P0] ban_protected_purged target=%q reason=%q protected_rule=%q", target, e.Reason, rule)
		}
	}
	if dirty {
		return l.saveLocked()
	}
	return nil
}

// IsProtected 查询 IP/CIDR 是否命中受保护名单。
func (l *List) IsProtected(target string) bool {
	n, err := parseTarget(target)
	if err != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.protectedMatchLocked(n)
	return ok
}

func (l *List) protectedMatchLocked(target *net.IPNet) (string, bool) {
	if target == nil {
		return "", false
	}
	for _, rule := range l.protected {
		// 两个 CIDR 有交集，当且仅当任一网络起点落在另一网络中。
		if rule.ipnet.Contains(target.IP) || target.Contains(rule.ipnet.IP) {
			return rule.target, true
		}
	}
	return "", false
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
