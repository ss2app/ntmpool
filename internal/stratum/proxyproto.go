package stratum

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// PROXY protocol v1 解包（docs/01 R12：藏在转发器后拿矿工真实 IP）。
// 行格式：`PROXY TCP4 <srcIP> <dstIP> <srcPort> <dstPort>\r\n`（haproxy 规范）。
//
// 模式（PortConfig.Proxy）：
//   off      —— 不解包（默认）
//   optional —— 有头就解、没头当直连（本机调试与转发流量共用一个端口）
//   required —— 必须有头，没有直接断（端口只被转发器访问时用，防伪造直连）

const proxyHeaderTimeout = 5 * time.Second

// proxyConn 包装原始连接：RemoteAddr 换成 PROXY 头里的真实源地址，
// 读走 bufio（头之后可能已缓冲的字节不能丢）。
type proxyConn struct {
	net.Conn
	r    *bufio.Reader
	real net.Addr
}

func (p *proxyConn) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *proxyConn) RemoteAddr() net.Addr {
	if p.real != nil {
		return p.real
	}
	return p.Conn.RemoteAddr()
}

// resolveProxy 按模式处理一条新连接。返回（可能被包装的）连接；错误 = 调用方断开。
func resolveProxy(conn net.Conn, mode string) (net.Conn, error) {
	switch mode {
	case "", "off":
		return conn, nil
	case "optional", "required":
	default:
		return conn, fmt.Errorf("未知 proxyProtocol 模式 %q", mode)
	}

	_ = conn.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	defer conn.SetReadDeadline(time.Time{})

	r := bufio.NewReaderSize(conn, 4096)
	peek, err := r.Peek(6)
	if err != nil {
		return nil, fmt.Errorf("读 PROXY 头失败: %w", err)
	}
	if string(peek) != "PROXY " {
		if mode == "required" {
			return nil, fmt.Errorf("proxyProtocol=required 但无 PROXY 头（来自 %s 的直连？）", conn.RemoteAddr())
		}
		// optional：无头当直连，缓冲字节原样交给协议层
		return &proxyConn{Conn: conn, r: r}, nil
	}
	line, err := r.ReadString('\n')
	if err != nil || len(line) > 108 { // 规范上限 107 字节 + \n
		return nil, fmt.Errorf("PROXY 头非法: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(line))
	// PROXY UNKNOWN（转发器本地健康检查）：无源地址，保留原样
	if len(fields) >= 2 && fields[1] == "UNKNOWN" {
		return &proxyConn{Conn: conn, r: r}, nil
	}
	if len(fields) != 6 || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return nil, fmt.Errorf("PROXY 头非法: %q", strings.TrimSpace(line))
	}
	ip := net.ParseIP(fields[2])
	if ip == nil {
		return nil, fmt.Errorf("PROXY 头源 IP 非法: %q", fields[2])
	}
	real := &net.TCPAddr{IP: ip}
	if p, err := strconv.Atoi(fields[4]); err == nil && p > 0 && p < 65536 {
		real.Port = p
	}
	return &proxyConn{Conn: conn, r: r, real: real}, nil
}
