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

	"github.com/scashcc/ntmpool/internal/config"
)

// Dialect 一种 stratum 协议方言。实现见 dialect_*.go（M1: stratum1）。
type Dialect interface {
	Name() string
	// Serve 接管一条已建立的连接，阻塞直到断开。
	// 实现负责：握手（记录 user-agent/版本/矿工名）、job 推送、share 解析上抛。
	Serve(ctx context.Context, conn net.Conn, port config.PortConfig) error
}

var dialects = map[string]Dialect{}

func RegisterDialect(d Dialect) { dialects[d.Name()] = d }

// Listener 一个热管理的 stratum 端口。
type Listener struct {
	cfg    config.PortConfig
	ln     net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Manager 端口热管理器：管理后台增/删/改端口时调用，不影响其他端口的存量连接。
type Manager struct {
	mu        sync.Mutex
	coinID    string
	listeners map[int]*Listener
}

func NewManager(coinID string) *Manager {
	return &Manager{coinID: coinID, listeners: map[int]*Listener{}}
}

// StartPort 热启动一个端口。
func (m *Manager) StartPort(parent context.Context, pc config.PortConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.listeners[pc.Port]; ok {
		return fmt.Errorf("[%s] 端口 %d 已在监听", m.coinID, pc.Port)
	}
	d, ok := dialects[pc.Dialect]
	if !ok {
		return fmt.Errorf("[%s] 未知方言 %q", m.coinID, pc.Dialect)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", pc.Port))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	l := &Listener{cfg: pc, ln: ln, cancel: cancel}
	m.listeners[pc.Port] = l

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听器已关闭
			}
			// TODO(M1): 每 IP 并发上限 / 连接速率 / ban 名单 / PROXY protocol 解包 / TLS
			l.wg.Add(1)
			go func() {
				defer l.wg.Done()
				defer conn.Close()
				_ = d.Serve(ctx, conn, pc)
			}()
		}
	}()
	return nil
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
