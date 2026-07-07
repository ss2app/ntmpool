// Package stratum 连接层 + 端口热管理。
//
// 分层：Listener(端口,热启停) → Conn(限流/看门狗/PROXY protocol) → Dialect(协议方言)。
// 方言与连接层解耦：stratum1（BTC 系）、cryptonote（XMRig 系）、ntm（自有轻量协议）
// 共用同一套连接管理、vardiff、ban、计数器。
package stratum

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/scashcc/ntmpool/internal/config"
)

// Dialect 一种 stratum 协议方言。实现见 dialect_*.go（M1: stratum1）。
type Dialect interface {
	Name() string
	// Serve 接管一条已建立的连接，阻塞直到断开。
	// 实现负责：握手（记录 user-agent/版本/矿工名）、job 推送、share 解析上抛。
	Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error
}

// Listener 一个热管理的 stratum 端口。
type Listener struct {
	cfg    config.PortConfig
	ln     net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup

	connMu sync.Mutex
	conns  int            // 该端口当前在连数（MaxConns 上限用）
	perIP  map[string]int // 真实 IP → 在连数（MaxConnsPerIP 上限用）
}

// BanChecker 连接准入检查（banlist.List 满足此签名；全池共享一份）。
type BanChecker interface {
	Banned(ip string) (bool, string)
}

// Manager 端口热管理器：管理后台增/删/改端口时调用，不影响其他端口的存量连接。
// 每币一个 Manager，持有该币可用的方言（方言是 per-coin 的，含该币 ShareHandler）。
type Manager struct {
	mu        sync.Mutex
	coinID    string
	dialects  map[string]Dialect
	listeners map[int]*Listener
	ban       BanChecker  // 可选
	acceptNew atomic.Bool // 币开关 newConnectionsEnabled（热参数）：false 只拒新连，存量不断
}

func NewManager(coinID string, dialects map[string]Dialect) *Manager {
	m := &Manager{coinID: coinID, dialects: dialects, listeners: map[int]*Listener{}}
	m.acceptNew.Store(true)
	return m
}

// SetBanChecker 注入 ban 名单（启动时一次）。
func (m *Manager) SetBanChecker(b BanChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ban = b
}

// SetAcceptNew 热开关：是否接受新连接（存量连接不受影响）。
func (m *Manager) SetAcceptNew(v bool) { m.acceptNew.Store(v) }

// AcceptNew 当前是否接受新连接。
func (m *Manager) AcceptNew() bool { return m.acceptNew.Load() }

// StartPort 热启动一个端口。
func (m *Manager) StartPort(parent context.Context, pc config.PortConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.listeners[pc.Port]; ok {
		return fmt.Errorf("[%s] 端口 %d 已在监听", m.coinID, pc.Port)
	}
	d, ok := m.dialects[pc.Dialect]
	if !ok {
		return fmt.Errorf("[%s] 未知方言 %q", m.coinID, pc.Dialect)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", pc.Port))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	l := &Listener{cfg: pc, ln: ln, cancel: cancel, perIP: map[string]int{}}
	m.listeners[pc.Port] = l

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听器已关闭
			}
			// 准入最外圈：币开关 + ban 名单（命中直接断，零协议交互）
			if !m.acceptNew.Load() {
				_ = conn.Close()
				continue
			}
			m.mu.Lock()
			ban := m.ban
			m.mu.Unlock()
			if ban != nil {
				if banned, _ := ban.Banned(remoteHost(conn)); banned {
					_ = conn.Close()
					continue
				}
			}
			// TODO(M3+): 连接速率限制 / TLS
			l.wg.Add(1)
			go func(raw net.Conn) {
				defer l.wg.Done()
				defer raw.Close()
				// PROXY protocol 解包（R12：转发器后拿真实 IP）——头读取有超时，
				// 放独立 goroutine，慢连接不阻塞 accept 循环
				conn, err := resolveProxy(raw, pc.Proxy)
				if err != nil {
					return
				}
				ip := remoteHost(conn)
				// 真实 IP 再过一次 ban（转发流量在 accept 时只能查到转发器 IP）
				if ban != nil {
					if banned, _ := ban.Banned(ip); banned {
						return
					}
				}
				if !l.acquire(ip, pc) {
					return // 端口/每 IP 并发上限
				}
				defer l.release(ip)
				_ = d.Serve(ctx, conn, pc)
			}(conn)
		}
	}()
	return nil
}

// acquire 端口在连数 + 每 IP 在连数双上限（0 = 不限）。
func (l *Listener) acquire(ip string, pc config.PortConfig) bool {
	l.connMu.Lock()
	defer l.connMu.Unlock()
	if pc.MaxConns > 0 && l.conns >= pc.MaxConns {
		return false
	}
	if pc.MaxConnsPerIP > 0 && l.perIP[ip] >= pc.MaxConnsPerIP {
		return false
	}
	l.conns++
	l.perIP[ip]++
	return true
}

func (l *Listener) release(ip string) {
	l.connMu.Lock()
	defer l.connMu.Unlock()
	l.conns--
	if n := l.perIP[ip] - 1; n > 0 {
		l.perIP[ip] = n
	} else {
		delete(l.perIP, ip)
	}
}

// StopPort 热停止端口：停止 accept 并断开该端口全部连接。
func (m *Manager) StopPort(port int) error {
	m.mu.Lock()
	l, ok := m.listeners[port]
	if ok {
		delete(m.listeners, port)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("[%s] 端口 %d 未在监听", m.coinID, port)
	}
	l.cancel()
	_ = l.ln.Close()
	l.wg.Wait()
	return nil
}

// StopAll 关停该币全部端口（币下线/进程退出）。
func (m *Manager) StopAll() {
	m.mu.Lock()
	ports := make([]int, 0, len(m.listeners))
	for p := range m.listeners {
		ports = append(ports, p)
	}
	m.mu.Unlock()
	for _, p := range ports {
		_ = m.StopPort(p)
	}
}

// Ports 当前监听中的端口（管理后台查询用）。
func (m *Manager) Ports() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int, 0, len(m.listeners))
	for p := range m.listeners {
		out = append(out, p)
	}
	return out
}
