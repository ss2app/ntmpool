package stratum

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

type fakeBanner struct {
	mu        sync.Mutex
	banned    map[string]int
	protected map[string]bool
}

func (f *fakeBanner) Ban(target, reason string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.banned == nil {
		f.banned = map[string]int{}
	}
	f.banned[target]++
	return nil
}
func (f *fakeBanner) Strikes(target string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.banned[target]
}
func (f *fakeBanner) IsProtected(target string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.protected[target]
}
func (f *fakeBanner) isBanned(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.banned[ip] > 0
}

func TestAutoBanPolicy(t *testing.T) {
	tests := []struct {
		name      string
		ip        string
		protected bool
		outcomes  []core.ShareOutcome
		wantTrip  bool
		wantIPBan bool
	}{
		{"样本不足", "9.9.9.9", false, repeatOutcome(core.OutcomeBadPow, autoBanMinSamples-1), false, false},
		{"verified 恶意源", "9.9.9.9", false, repeatOutcome(core.OutcomeBadPow, autoBanMinSamples), true, true},
		{"无 verified 仅断会话", "", false, repeatOutcome(core.OutcomeBadPow, autoBanMinSamples), true, false},
		{"受保护目标仅断会话", "10.0.0.2", true, repeatOutcome(core.OutcomeBadPow, autoBanMinSamples), true, false},
		{"良性 stale", "8.8.8.8", false, repeatOutcome(core.OutcomeStale, 100), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBanner{protected: map[string]bool{tt.ip: tt.protected}}
			ab := &autoBan{banner: b, ip: tt.ip, coin: "test"}
			tripped := false
			for _, outcome := range tt.outcomes {
				if ab.record(outcome) {
					tripped = true
					break
				}
			}
			if tripped != tt.wantTrip {
				t.Fatalf("tripped=%v want %v", tripped, tt.wantTrip)
			}
			if b.isBanned(tt.ip) != tt.wantIPBan {
				t.Fatalf("banned=%v want %v", b.isBanned(tt.ip), tt.wantIPBan)
			}
		})
	}
}

func repeatOutcome(outcome core.ShareOutcome, n int) []core.ShareOutcome {
	out := make([]core.ShareOutcome, n)
	for i := range out {
		out[i] = outcome
	}
	return out
}

func TestCNDialectAutoBanClosesVerifiedOnly(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0", outcome: core.OutcomeBadPow}
	d := NewCNDialect("test", h)
	b := &fakeBanner{}
	d.SetAutoBan(b)
	pc := config.PortConfig{Port: 1, Mode: "pplns", Dialect: "cryptonote",
		Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1000, MinDiff: 1, MaxDiff: 100000, TargetSeconds: 10}}
	rawSrv, cli := net.Pipe()
	srv := &proxyConn{Conn: rawSrv, identity: ConnectionIdentity{
		TransportPeerIP: "10.0.0.2", VerifiedClientIP: "9.9.9.9", IPProvenance: IPProvenanceProxyV1,
	}}
	done := make(chan struct{})
	go func() {
		_ = d.Serve(context.Background(), srv, pc)
		_ = srv.Close()
		close(done)
	}()
	enc := json.NewEncoder(cli)
	_ = enc.Encode(map[string]any{"id": 1, "method": "login", "params": map[string]any{"login": "a"}})
	dec := json.NewDecoder(cli)
	var resp map[string]any
	_ = dec.Decode(&resp)
	for i := 0; i < autoBanMinSamples+2; i++ {
		if err := enc.Encode(map[string]any{"id": 2, "method": "submit",
			"params": map[string]any{"job_id": "j1", "nonce": nonceHexFor(i), "result": "ab"}}); err != nil {
			break
		}
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			break
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("恶意连接未被断开")
	}
	if !b.isBanned("9.9.9.9") || b.isBanned("10.0.0.2") {
		t.Fatalf("必须只 ban verified client，banned=%v", b.banned)
	}
}

func nonceHexFor(i int) string {
	const hexd = "0123456789abcdef"
	return string([]byte{hexd[i%16], hexd[(i/16)%16]}) + "000000"
}

func TestResolveProxyV1V2AndIdentity(t *testing.T) {
	v2 := proxyV2TCP4("1.2.3.5", "5.6.7.8", 5555, 3333, []byte("hello\n"))
	tests := []struct {
		name, mode string
		payload    []byte
		trusted    bool
		wantErr    bool
		wantIP     string
		wantProv   IPProvenance
		wantTail   string
	}{
		{"v1", "required", []byte("PROXY TCP4 1.2.3.4 5.6.7.8 5555 3333\r\nhello\n"), true, false, "1.2.3.4", IPProvenanceProxyV1, "hello\n"},
		{"v2", "required", v2, true, false, "1.2.3.5", IPProvenanceProxyV2, "hello\n"},
		{"optional 直连无 verified", "optional", []byte("{\"id\":1}\n"), false, false, "", IPProvenanceDirect, "{\"id\":1}\n"},
		{"required 无头", "required", []byte("{\"id\":1}\n"), true, true, "", "", ""},
		{"非受信伪造 v1", "optional", []byte("PROXY TCP4 1.2.3.4 5.6.7.8 1 2\r\n"), false, true, "", "", ""},
		{"非法 v1", "required", []byte("PROXY TCP4 notanip x y z\r\n"), true, true, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, cli := net.Pipe()
			go func() { _, _ = cli.Write(tt.payload) }()
			trusted := func(string) bool { return tt.trusted }
			conn, err := resolveProxy(srv, tt.mode, trusted)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if err == nil {
				id := connectionIdentity(conn)
				if id.TransportPeerIP != "pipe" || id.VerifiedClientIP != tt.wantIP || id.IPProvenance != tt.wantProv {
					t.Fatalf("identity=%+v", id)
				}
				if tt.wantTail != "" {
					buf := make([]byte, len(tt.wantTail))
					_, _ = ioReadFull(conn, buf)
					if string(buf) != tt.wantTail {
						t.Fatalf("tail=%q", buf)
					}
				}
			}
			_ = cli.Close()
			_ = srv.Close()
		})
	}
}

func proxyV2TCP4(src, dst string, srcPort, dstPort uint16, tail []byte) []byte {
	payload := make([]byte, 12)
	copy(payload[0:4], net.ParseIP(src).To4())
	copy(payload[4:8], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(payload[8:10], srcPort)
	binary.BigEndian.PutUint16(payload[10:12], dstPort)
	header := append([]byte(nil), proxyV2Magic...)
	header = append(header, 0x21, 0x11, 0, byte(len(payload)))
	return append(append(header, payload...), tail...)
}

func ioReadFull(r net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestCollapseDetectorTriggersAndRecovers(t *testing.T) {
	pc := config.WithPortDefaults(config.PortConfig{Port: 3333, CollapseMinConnections: 20, CollapsePercent: 90})
	l := &Listener{coinID: "test", cfg: pc, perIP: map[string]int{}}
	ids := make([]ConnectionIdentity, 0, 20)
	for i := 0; i < 18; i++ {
		ids = append(ids, ConnectionIdentity{VerifiedClientIP: "1.1.1.1"})
	}
	ids = append(ids, ConnectionIdentity{VerifiedClientIP: "2.2.2.2"}, ConnectionIdentity{VerifiedClientIP: "3.3.3.3"})
	for _, id := range ids {
		if ok, reason := l.acquire(id, pc); !ok {
			t.Fatalf("acquire: %s", reason)
		}
	}
	if l.ipAutobanAllowed() {
		t.Fatal("90% 单 IP 应触发坍缩降级")
	}
	l.release(ConnectionIdentity{VerifiedClientIP: "1.1.1.1"}) // 17/19 < 90%
	if !l.ipAutobanAllowed() {
		t.Fatal("IP 分布恢复后应自动解除降级")
	}
}

func TestLayeredValidationBoundaries(t *testing.T) {
	t.Run("JSON 深度", func(t *testing.T) {
		for _, tt := range []struct {
			raw     string
			max     int
			wantErr bool
		}{
			{`{"x":[1]}`, 2, false},
			{`{"x":[{"y":1}]}`, 2, true},
			{`{"x":"[[["}`, 1, false},
		} {
			if got := validateJSONDepth([]byte(tt.raw), tt.max); (got != nil) != tt.wantErr {
				t.Fatalf("raw=%s err=%v", tt.raw, got)
			}
		}
	})
	t.Run("未授权预算边界", func(t *testing.T) {
		g := newConnectionGuard(config.PortConfig{UnauthMaxMessages: 2, UnauthMaxBytes: 8, JSONMaxDepth: 4, PreAuthViolationLimit: 2})
		if err := g.observe(`{}`, false); err != nil {
			t.Fatal(err)
		}
		if err := g.observe(`{}`, false); err != nil {
			t.Fatal(err)
		}
		if err := g.observe(`{}`, false); err == nil {
			t.Fatal("超过消息数应断")
		}
		if err := g.unauthorized("submit"); err != nil {
			t.Fatal(err)
		}
		if err := g.unauthorized("submit"); err != nil {
			t.Fatal(err)
		}
		if err := g.unauthorized("submit"); err == nil {
			t.Fatal("超过未授权 submit 次数应断")
		}
	})
	t.Run("消息字节上限", func(t *testing.T) {
		sc := newLineScanner(strings.NewReader(strings.Repeat("x", 17)+"\n"), 16)
		if sc.Scan() || sc.Err() == nil {
			t.Fatal("超过配置消息上限必须报错")
		}
		sc = newLineScanner(bytes.NewBufferString("1234567890\n"), config.HardMessageMaxBytes+1)
		if !sc.Scan() {
			t.Fatalf("硬上限钳制不应影响短消息: %v", sc.Err())
		}
	})
}
