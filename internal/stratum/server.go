// Package stratum 连接层 + 端口热管理。
//
// 分层：Listener(端口,热启停) → Conn(限流/看门狗/PROXY protocol) → Dialect(协议方言)。
// 方言与连接层解耦：stratum1（BTC 系）、cryptonote（XMRig 系）、ntm（自有轻量协议）
// 共用同一套连接管理、vardiff、ban、计数器。
package stratum

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/metrics"
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
	coinID string
	cfg    config.PortConfig
	ln     net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup

	connMu    sync.Mutex
	conns     int            // 该端口当前在连数（MaxConns 上限用）
	perIP     map[string]int // verified IP → 在连数（MaxConnsPerIP/坍缩检测用）
	collapsed atomic.Bool    // true = 临时禁用该端口 IP-autoban
}

// BanChecker 连接准入检查（banlist.List 满足此签名；全池共享一份）。
type BanChecker interface {
	Banned(ip string) (bool, string)
	IsProtected(ip string) bool
}

type rejectLogWindow struct {
	start time.Time
	count int
}

const rejectLogMaxEntries = 4096

// Manager 端口热管理器：管理后台增/删/改端口时调用，不影响其他端口的存量连接。
// 每币一个 Manager，持有该币可用的方言（方言是 per-coin 的，含该币 ShareHandler）。
type Manager struct {
	mu        sync.Mutex
	coinID    string
	dialects  map[string]Dialect
	listeners map[int]*Listener
	ban       BanChecker  // 可选
	acceptNew atomic.Bool // 币开关 newConnectionsEnabled（热参数）：false 只拒新连，存量不断
	rejectMu  sync.Mutex
	rejectLog map[string]rejectLogWindow
}

func NewManager(coinID string, dialects map[string]Dialect) *Manager {
	m := &Manager{coinID: coinID, dialects: dialects, listeners: map[int]*Listener{}, rejectLog: map[string]rejectLogWindow{}}
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
	pc = config.WithPortDefaults(pc)
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
	l := &Listener{coinID: m.coinID, cfg: pc, ln: ln, cancel: cancel, perIP: map[string]int{}}
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
				peer := transportPeerIP(conn)
				if m.allowRejectLog(peer, pc.BanRejectLogPerMinute) {
					log.Printf("[%s] stratum_reject port=%d transport_peer_ip=%q reason=%q", m.coinID, pc.Port, peer, "new_connections_disabled")
				}
				metrics.ConnectionEvent(m.coinID, pc.Port, "reject")
				_ = conn.Close()
				continue
			}
			m.mu.Lock()
			ban := m.ban
			m.mu.Unlock()
			// TODO(M3+): 连接速率限制 / TLS
			l.wg.Add(1)
			go func(raw net.Conn) {
				defer l.wg.Done()
				defer raw.Close()
				// PROXY protocol 解包（R12：转发器后拿真实 IP）——头读取有超时，
				// 放独立 goroutine，慢连接不阻塞 accept 循环
				var trusted func(string) bool
				if ban != nil {
					trusted = ban.IsProtected // 受保护名单同时作为 trusted-forwarder 名单
				}
				conn, err := resolveProxy(raw, pc.Proxy, trusted)
				if err != nil {
					log.Printf("[P0] [%s] proxy_link_error port=%d transport_peer_ip=%q action=%q error=%q", m.coinID, pc.Port, transportPeerIP(raw), "connection_close", err)
					metrics.ConnectionEvent(m.coinID, pc.Port, "proxy_error")
					return
				}
				id := connectionIdentity(conn)
				// 真实 IP 再过一次 ban（转发流量在 accept 时只能查到转发器 IP）
				if ban != nil && id.VerifiedClientIP != "" {
					if banned, reason := ban.Banned(id.VerifiedClientIP); banned {
						if m.allowRejectLog(id.VerifiedClientIP, pc.BanRejectLogPerMinute) {
							log.Printf("[%s] stratum_ban_reject port=%d verified_client_ip=%q transport_peer_ip=%q ip_provenance=%q reason=%q", m.coinID, pc.Port, id.VerifiedClientIP, id.TransportPeerIP, id.IPProvenance, reason)
						}
						metrics.ConnectionEvent(m.coinID, pc.Port, "ban_reject")
						return
					}
				}
				if ok, reason := l.acquire(id, pc); !ok {
					key := id.VerifiedClientIP
					if key == "" {
						key = id.TransportPeerIP
					}
					if m.allowRejectLog(key, pc.BanRejectLogPerMinute) {
						log.Printf("[%s] stratum_reject port=%d verified_client_ip=%q transport_peer_ip=%q ip_provenance=%q reason=%q", m.coinID, pc.Port, id.VerifiedClientIP, id.TransportPeerIP, id.IPProvenance, reason)
					}
					metrics.ConnectionEvent(m.coinID, pc.Port, "limit_reject")
					return // 端口/每 IP 并发上限
				}
				defer l.release(id)
				conn = &governedConn{Conn: conn, identity: id, allowIPAutoBan: l.ipAutobanAllowed}
				started := time.Now()
				log.Printf("[%s] stratum_accept port=%d verified_client_ip=%q transport_peer_ip=%q ip_provenance=%q", m.coinID, pc.Port, id.VerifiedClientIP, id.TransportPeerIP, id.IPProvenance)
				metrics.ConnectionEvent(m.coinID, pc.Port, "accept")
				serveErr := d.Serve(ctx, conn, pc)
				log.Printf("[%s] stratum_close port=%d verified_client_ip=%q transport_peer_ip=%q ip_provenance=%q duration_ms=%d reason=%q", m.coinID, pc.Port, id.VerifiedClientIP, id.TransportPeerIP, id.IPProvenance, time.Since(started).Milliseconds(), errorString(serveErr))
				metrics.ConnectionEvent(m.coinID, pc.Port, "close")
			}(conn)
		}
	}()
	return nil
}

// acquire 端口在连数 + 每 IP 在连数双上限（0 = 不限）。
func (l *Listener) acquire(id ConnectionIdentity, pc config.PortConfig) (bool, string) {
	l.connMu.Lock()
	defer l.connMu.Unlock()
	if pc.MaxConns > 0 && l.conns >= pc.MaxConns {
		return false, "max_connections"
	}
	if id.VerifiedClientIP != "" && pc.MaxConnsPerIP > 0 && l.perIP[id.VerifiedClientIP] >= pc.MaxConnsPerIP {
		return false, "max_connections_per_verified_ip"
	}
	l.conns++
	if id.VerifiedClientIP != "" {
		l.perIP[id.VerifiedClientIP]++
	}
	l.evaluateCollapseLocked(pc)
	return true, ""
}

func (l *Listener) release(id ConnectionIdentity) {
	l.connMu.Lock()
	defer l.connMu.Unlock()
	l.conns--
	if id.VerifiedClientIP != "" {
		if n := l.perIP[id.VerifiedClientIP] - 1; n > 0 {
			l.perIP[id.VerifiedClientIP] = n
		} else {
			delete(l.perIP, id.VerifiedClientIP)
		}
	}
	l.evaluateCollapseLocked(l.cfg)
}

func (l *Listener) evaluateCollapseLocked(pc config.PortConfig) {
	total, top := 0, 0
	var dominant string
	for ip, n := range l.perIP {
		total += n
		if n > top {
			top, dominant = n, ip
		}
	}
	next := total >= pc.CollapseMinConnections && top*100 >= total*pc.CollapsePercent
	old := l.collapsed.Swap(next)
	if old == next {
		return
	}
	if next {
		log.Printf("[P0] [%s] ip_autoban_degraded port=%d action=%q verified_connections=%d distinct_verified_ips=%d dominant_ip=%q dominant_percent=%d threshold_percent=%d", l.coinID, pc.Port, "session_only", total, len(l.perIP), dominant, top*100/total, pc.CollapsePercent)
		metrics.CollapseTransition(l.coinID, pc.Port, true)
	} else {
		log.Printf("[P0] [%s] ip_autoban_recovered port=%d action=%q verified_connections=%d distinct_verified_ips=%d", l.coinID, pc.Port, "ip_autoban_enabled", total, len(l.perIP))
		metrics.CollapseTransition(l.coinID, pc.Port, false)
	}
}

func (l *Listener) ipAutobanAllowed() bool { return !l.collapsed.Load() }

type governedConn struct {
	net.Conn
	identity       ConnectionIdentity
	allowIPAutoBan func() bool
}

func (c *governedConn) ConnectionIdentity() ConnectionIdentity { return c.identity }
func (c *governedConn) IPAutobanAllowed() bool {
	return c.allowIPAutoBan == nil || c.allowIPAutoBan()
}

type ipAutobanGate interface {
	IPAutobanAllowed() bool
}

func (m *Manager) allowRejectLog(ip string, limit int) bool {
	if limit <= 0 {
		return false
	}
	now := time.Now()
	m.rejectMu.Lock()
	defer m.rejectMu.Unlock()
	if _, exists := m.rejectLog[ip]; !exists && len(m.rejectLog) >= rejectLogMaxEntries {
		// 惰性清理：只有新 key 会突破上限时才扫描，正常拒连路径维持 O(1)。
		for key, old := range m.rejectLog {
			if old.start.IsZero() || now.Sub(old.start) >= time.Minute {
				delete(m.rejectLog, key)
			}
		}
		// 若 4096 个窗口都仍活跃，淘汰最老一条，确保 map 永远有硬上限。
		if len(m.rejectLog) >= rejectLogMaxEntries {
			var oldestKey string
			var oldest time.Time
			for key, candidate := range m.rejectLog {
				if oldestKey == "" || candidate.start.Before(oldest) {
					oldestKey, oldest = key, candidate.start
				}
			}
			delete(m.rejectLog, oldestKey)
		}
	}
	w := m.rejectLog[ip]
	if w.start.IsZero() || now.Sub(w.start) >= time.Minute {
		w = rejectLogWindow{start: now}
	}
	if w.count >= limit {
		m.rejectLog[ip] = w
		return false
	}
	w.count++
	m.rejectLog[ip] = w
	return true
}

func errorString(err error) string {
	if err == nil {
		return "peer_closed"
	}
	return err.Error()
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
