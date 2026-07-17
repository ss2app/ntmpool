package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/metrics"
)

// newLineScanner 行扫描器（抗超大行洪水，各方言共用）。
func newLineScanner(r io.Reader, limits ...int) *bufio.Scanner {
	max := config.DefaultMessageMaxBytes
	if len(limits) > 0 && limits[0] > 0 {
		max = limits[0]
	}
	if max > config.HardMessageMaxBytes {
		max = config.HardMessageMaxBytes
	}
	initial := 4096
	if max < initial {
		initial = max
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, initial), max)
	return sc
}

type scannedLine struct {
	text string
	err  error
}

// scanLines 把 Scanner 的 Err 显式传回协议循环，超大消息不再静默表现成 EOF。
func scanLines(ctx context.Context, r io.Reader, maxBytes int) <-chan scannedLine {
	out := make(chan scannedLine, 16)
	go func() {
		defer close(out)
		sc := newLineScanner(r, maxBytes)
		for sc.Scan() {
			select {
			case out <- scannedLine{text: sc.Text()}:
			case <-ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil {
			select {
			case out <- scannedLine{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return out
}

// parseHexU32 解析 8 hex 字符（大端）为 uint32。stratum 的 ntime/nonce/version_bits 均此格式。
func parseHexU32(s string) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) == 0 || len(s) > 8 {
		return 0, fmt.Errorf("非法 u32 hex: %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// u32hex 编码为 8 hex 字符大端（notify 的 version/bits/ntime）。
func u32hex(v uint32) string {
	return fmt.Sprintf("%08x", v)
}

func transportPeerIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

func connectionIdentity(conn net.Conn) ConnectionIdentity {
	if c, ok := conn.(identityCarrier); ok {
		id := c.ConnectionIdentity()
		if id.IPProvenance == "" {
			id.IPProvenance = IPProvenanceDirect
		}
		return id
	}
	return ConnectionIdentity{TransportPeerIP: transportPeerIP(conn), IPProvenance: IPProvenanceDirect}
}

func verifiedClientIP(conn net.Conn) string { return connectionIdentity(conn).VerifiedClientIP }

// remoteHost 仅保留给非治理展示/旧测试；IP 处罚调用方必须显式用 verifiedClientIP。
func remoteHost(conn net.Conn) string {
	id := connectionIdentity(conn)
	if id.VerifiedClientIP != "" {
		return id.VerifiedClientIP
	}
	return id.TransportPeerIP
}

// validateJSONDepth 在 Unmarshal 前做无分配深度检查；正确跳过字符串与转义字符。
func validateJSONDepth(b []byte, max int) error {
	if max <= 0 {
		max = config.DefaultJSONMaxDepth
	}
	depth := 0
	inString := false
	escaped := false
	for _, ch := range b {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > max {
				return fmt.Errorf("JSON 嵌套深度 %d 超过上限 %d", depth, max)
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

// connectionGuard 维护授权前累计预算与违规次数；每连接串行使用。
type connectionGuard struct {
	cfg        config.PortConfig
	messages   int
	bytes      int
	violations int
}

func newConnectionGuard(pc config.PortConfig) *connectionGuard {
	return &connectionGuard{cfg: config.WithPortDefaults(pc)}
}

func (g *connectionGuard) observe(line string, authorized bool) error {
	if err := validateJSONDepth([]byte(line), g.cfg.JSONMaxDepth); err != nil {
		return err
	}
	if authorized {
		return nil
	}
	g.messages++
	g.bytes += len(line) + 1
	if g.messages > g.cfg.UnauthMaxMessages {
		return fmt.Errorf("未授权消息数 %d 超过上限 %d", g.messages, g.cfg.UnauthMaxMessages)
	}
	if g.bytes > g.cfg.UnauthMaxBytes {
		return fmt.Errorf("未授权累计字节 %d 超过上限 %d", g.bytes, g.cfg.UnauthMaxBytes)
	}
	return nil
}

func (g *connectionGuard) unauthorized(kind string) error {
	g.violations++
	if g.violations > g.cfg.PreAuthViolationLimit {
		return fmt.Errorf("未授权 %s 次数 %d 超过上限 %d", kind, g.violations, g.cfg.PreAuthViolationLimit)
	}
	return nil
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

type powGate struct {
	slots   chan struct{}
	waiters chan struct{}
}

var powGates sync.Map // coin:port:concurrency:queue → *powGate

// acquirePowSlot 对昂贵校验施加跨连接有界并发。队列满时当前连接会按配置短暂
// 停读形成 backpressure，之后明确拒绝；不会把提交对象堆进无界内存队列。
func acquirePowSlot(ctx context.Context, coin string, pc config.PortConfig) (func(), bool) {
	pc = config.WithPortDefaults(pc)
	key := fmt.Sprintf("%s:%d:%d:%d", coin, pc.Port, pc.PowVerifyConcurrency, pc.PowVerifyQueue)
	v, _ := powGates.LoadOrStore(key, &powGate{
		slots: make(chan struct{}, pc.PowVerifyConcurrency), waiters: make(chan struct{}, pc.PowVerifyQueue),
	})
	g := v.(*powGate)
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	default:
	}
	wait := time.Duration(pc.PowBackpressureMillis) * time.Millisecond
	select {
	case g.waiters <- struct{}{}:
		defer func() { <-g.waiters }()
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case g.slots <- struct{}{}:
			return func() { <-g.slots }, true
		case <-t.C:
			logPowQueueBackpressure(coin, pc)
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	default:
		// 队列已满：低信任连接在本 goroutine 上停读一小段时间，形成有界反压。
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
			logPowQueueBackpressure(coin, pc)
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

func logPowQueueBackpressure(coin string, pc config.PortConfig) {
	log.Printf("[%s] pow_verification_degraded port=%d action=%q concurrency=%d queue=%d backpressure_ms=%d reason=%q", coin, pc.Port, "share_reject", pc.PowVerifyConcurrency, pc.PowVerifyQueue, pc.PowBackpressureMillis, "verification queue full")
	metrics.ConnectionEvent(coin, pc.Port, "pow_queue_reject")
}

// marshalStratum 序列化一条 stratum 消息，保证响应类（非 method）result+error+id 三件套齐全。
// NiceHash pool verificator 因缺 "error":null 判不合格，故显式补齐。
func marshalStratum(m rpcMsg) ([]byte, error) {
	if m.Method != "" {
		// 请求/通知：{id, method, params}
		return json.Marshal(struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}{m.ID, m.Method, m.Params})
	}
	// 响应：{id, result, error}——result/error 即便为 nil 也输出显式 null
	return json.Marshal(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
		Error  any             `json:"error"`
	}{m.ID, m.Result, m.Error})
}
