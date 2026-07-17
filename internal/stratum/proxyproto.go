package stratum

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// IPProvenance 描述矿工 IP 的来源。DIRECT 只表示没有可信 PROXY 身份，
// 不等于可拿 socket 对端做 IP 级处罚。
type IPProvenance string

const (
	IPProvenanceDirect  IPProvenance = "DIRECT"
	IPProvenanceProxyV1 IPProvenance = "PROXY_V1"
	IPProvenanceProxyV2 IPProvenance = "PROXY_V2"
)

// ConnectionIdentity 显式分离 transport 与经 trusted edge 验证的真实矿工 IP。
type ConnectionIdentity struct {
	TransportPeerIP  string
	VerifiedClientIP string
	IPProvenance     IPProvenance
}

const (
	proxyHeaderTimeout = 5 * time.Second
	proxyV2MaxPayload  = 64 * 1024
)

var proxyV2Magic = []byte{'\r', '\n', '\r', '\n', 0, '\r', '\n', 'Q', 'U', 'I', 'T', '\n'}

// proxyConn 保留头后已缓冲字节，并携带显式连接身份；RemoteAddr 始终仍是
// socket 直接对端，杜绝把 PROXY 声明地址混成 transport peer。
type proxyConn struct {
	net.Conn
	r        *bufio.Reader
	identity ConnectionIdentity
}

func (p *proxyConn) Read(b []byte) (int, error) {
	if p.r != nil {
		return p.r.Read(b)
	}
	return p.Conn.Read(b)
}

func (p *proxyConn) ConnectionIdentity() ConnectionIdentity { return p.identity }

type identityCarrier interface {
	ConnectionIdentity() ConnectionIdentity
}

// resolveProxy 同时解析 PROXY v1 文本头与 v2 二进制头。
// trustedForwarder 必须认可 socket 直接对端，声明的 client IP 才会进入 VerifiedClientIP。
func resolveProxy(conn net.Conn, mode string, trustedForwarder ...func(string) bool) (net.Conn, error) {
	switch mode {
	case "", "off":
		return directIdentityConn(conn, nil), nil
	case "optional", "required":
	default:
		return nil, fmt.Errorf("未知 proxyProtocol 模式 %q", mode)
	}

	transport := transportPeerIP(conn)
	trusted := len(trustedForwarder) > 0 && trustedForwarder[0] != nil && trustedForwarder[0](transport)
	if mode == "required" && !trusted {
		return nil, fmt.Errorf("proxyProtocol=required 但 transport_peer_ip=%s 不在 trusted-forwarder 名单", transport)
	}

	_ = conn.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	defer conn.SetReadDeadline(time.Time{})
	r := bufio.NewReaderSize(conn, 4096)
	first, err := r.Peek(1)
	if err != nil {
		return nil, fmt.Errorf("读 PROXY 头失败: %w", err)
	}

	provenance := IPProvenanceDirect
	switch first[0] {
	case 'P':
		peek, err := r.Peek(6)
		if err != nil || string(peek) != "PROXY " {
			return noProxyHeader(conn, r, mode, transport)
		}
		provenance = IPProvenanceProxyV1
	case '\r':
		peek, err := r.Peek(len(proxyV2Magic))
		if err != nil || !equalBytes(peek, proxyV2Magic) {
			return noProxyHeader(conn, r, mode, transport)
		}
		provenance = IPProvenanceProxyV2
	default:
		return noProxyHeader(conn, r, mode, transport)
	}
	if !trusted {
		return nil, fmt.Errorf("拒绝来自非受信 transport_peer_ip=%s 的 %s 头", transport, provenance)
	}

	var clientIP string
	if provenance == IPProvenanceProxyV1 {
		clientIP, err = parseProxyV1(r)
	} else {
		clientIP, err = parseProxyV2(r)
	}
	if err != nil {
		return nil, err
	}
	return &proxyConn{Conn: conn, r: r, identity: ConnectionIdentity{
		TransportPeerIP: transport, VerifiedClientIP: clientIP, IPProvenance: provenance,
	}}, nil
}

func directIdentityConn(conn net.Conn, r *bufio.Reader) net.Conn {
	return &proxyConn{Conn: conn, r: r, identity: ConnectionIdentity{
		TransportPeerIP: transportPeerIP(conn), IPProvenance: IPProvenanceDirect,
	}}
}

func noProxyHeader(conn net.Conn, r *bufio.Reader, mode, transport string) (net.Conn, error) {
	if mode == "required" {
		return nil, fmt.Errorf("proxyProtocol=required 但无 PROXY 头（transport_peer_ip=%s）", transport)
	}
	return directIdentityConn(conn, r), nil
}

func parseProxyV1(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil || len(line) > 108 {
		return "", fmt.Errorf("PROXY v1 头非法: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) >= 2 && fields[1] == "UNKNOWN" {
		return "", nil
	}
	if len(fields) != 6 || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return "", fmt.Errorf("PROXY v1 头非法: %q", strings.TrimSpace(line))
	}
	ip := net.ParseIP(fields[2])
	if ip == nil {
		return "", fmt.Errorf("PROXY v1 源 IP 非法: %q", fields[2])
	}
	if p, err := strconv.Atoi(fields[4]); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("PROXY v1 源端口非法: %q", fields[4])
	}
	return ip.String(), nil
}

func parseProxyV2(r *bufio.Reader) (string, error) {
	header := make([]byte, 16)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", fmt.Errorf("PROXY v2 固定头不完整: %w", err)
	}
	if !equalBytes(header[:12], proxyV2Magic) || header[12]>>4 != 2 {
		return "", fmt.Errorf("PROXY v2 签名/版本非法")
	}
	cmd := header[12] & 0x0f
	length := int(binary.BigEndian.Uint16(header[14:16]))
	if length > proxyV2MaxPayload {
		return "", fmt.Errorf("PROXY v2 payload 过大: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", fmt.Errorf("PROXY v2 payload 不完整: %w", err)
	}
	if cmd == 0 { // LOCAL：不采信地址块
		return "", nil
	}
	if cmd != 1 {
		return "", fmt.Errorf("PROXY v2 command 非法: %d", cmd)
	}
	switch header[13] {
	case 0x11: // AF_INET + STREAM
		if len(payload) < 12 {
			return "", fmt.Errorf("PROXY v2 TCP4 地址块过短: %d", len(payload))
		}
		return net.IP(payload[:4]).String(), nil
	case 0x21: // AF_INET6 + STREAM
		if len(payload) < 36 {
			return "", fmt.Errorf("PROXY v2 TCP6 地址块过短: %d", len(payload))
		}
		return net.IP(payload[:16]).String(), nil
	default:
		return "", fmt.Errorf("PROXY v2 不支持 family/protocol=0x%02x", header[13])
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
