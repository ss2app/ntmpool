package stratum

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

// fakeBanner 内存 Banner。
type fakeBanner struct {
	mu     sync.Mutex
	banned map[string]int
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
func (f *fakeBanner) isBanned(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.banned[ip] > 0
}

// 自动 ban 策略单元：良性拒绝（stale/lowdiff）不计，恶意（badpow/dup/malformed）
// 占比超阈值触发，且连接被断开。
func TestAutoBanPolicy(t *testing.T) {
	b := &fakeBanner{}
	ab := &autoBan{banner: b, ip: "9.9.9.9"}
	// 9 条 badpow 不够样本数
	for i := 0; i < autoBanMinSamples-1; i++ {
		if ab.record(core.OutcomeBadPow) {
			t.Fatalf("样本不足不应触发（第 %d 条）", i+1)
		}
	}
	if !ab.record(core.OutcomeBadPow) {
		t.Fatal("样本够且 100% 恶意应触发")
	}
	if !b.isBanned("9.9.9.9") {
		t.Fatal("应已写入 ban 名单")
	}

	// 全 stale/lowdiff：永不触发
	ab2 := &autoBan{banner: b, ip: "8.8.8.8"}
	for i := 0; i < 100; i++ {
		if ab2.record(core.OutcomeStale) || ab2.record(core.OutcomeLowDiff) {
			t.Fatal("良性拒绝不应触发自动 ban")
		}
	}
	if b.isBanned("8.8.8.8") {
		t.Fatal("良性流量被误 ban")
	}

	// 掺一半 accepted：50% 阈值边界（violent*100 >= total*50 才触发）
	ab3 := &autoBan{banner: b, ip: "7.7.7.7"}
	for i := 0; i < 20; i++ {
		ab3.record(core.OutcomeAccepted)
	}
	trig := false
	for i := 0; i < 25 && !trig; i++ {
		trig = ab3.record(core.OutcomeBadPow)
	}
	if !trig {
		t.Fatal("恶意占比过半应触发")
	}
}

// CN 方言端到端：连发伪 share → 连接被断 + IP 进名单。
func TestCNDialectAutoBanCloses(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0", outcome: core.OutcomeBadPow}
	d := NewCNDialect("test", h)
	b := &fakeBanner{}
	d.SetAutoBan(b)

	pc := config.PortConfig{
		Port: 0, Mode: "pplns", Dialect: "cryptonote",
		Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1000, MinDiff: 1, MaxDiff: 100000, TargetSeconds: 10},
	}
	srv, cli := net.Pipe()
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
		if err := enc.Encode(map[string]any{
			"id": 2, "method": "submit",
			"params": map[string]any{"job_id": "j1", "nonce": nonceHexFor(i), "result": "ab"},
		}); err != nil {
			break // 连接已被断 = 预期
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
	if !b.isBanned("pipe") { // net.Pipe 的 RemoteAddr 是 "pipe"
		t.Fatal("恶意 IP 未进名单")
	}
}

func nonceHexFor(i int) string {
	const hexd = "0123456789abcdef"
	return string([]byte{hexd[i%16], hexd[(i/16)%16]}) + "000000"
}

// PROXY protocol v1 解包：required/optional/伪造头/UNKNOWN 四路。
func TestResolveProxy(t *testing.T) {
	pipeWith := func(payload string) (net.Conn, net.Conn) {
		srv, cli := net.Pipe()
		go func() { _, _ = cli.Write([]byte(payload)) }()
		return srv, cli
	}

	// 带头：真实 IP 换成头里的源地址，头后的字节原样可读
	srv, cli := pipeWith("PROXY TCP4 1.2.3.4 5.6.7.8 5555 3333\r\nhello\n")
	c, err := resolveProxy(srv, "required")
	if err != nil {
		t.Fatalf("required 带头应成功: %v", err)
	}
	if got := remoteHost(c); got != "1.2.3.4" {
		t.Fatalf("真实 IP = %s, want 1.2.3.4", got)
	}
	buf := make([]byte, 6)
	if n, _ := c.Read(buf); string(buf[:n]) != "hello\n" {
		t.Fatalf("头后数据丢失: %q", buf[:n])
	}
	_ = cli.Close()

	// required 无头：拒绝
	srv, cli = pipeWith(`{"id":1,"method":"login"}` + "\n")
	if _, err := resolveProxy(srv, "required"); err == nil {
		t.Fatal("required 无头应拒绝")
	}
	_ = cli.Close()

	// optional 无头：当直连，首字节不丢
	srv, cli = pipeWith(`{"id":1}` + "\n")
	c, err = resolveProxy(srv, "optional")
	if err != nil {
		t.Fatalf("optional 无头应放行: %v", err)
	}
	buf = make([]byte, 8)
	if n, _ := c.Read(buf); string(buf[:n]) != `{"id":1}` {
		t.Fatalf("直连首字节被吃: %q", buf[:n])
	}
	_ = cli.Close()

	// 伪造/非法头：拒绝
	srv, cli = pipeWith("PROXY TCP4 notanip x y z\r\n")
	if _, err := resolveProxy(srv, "required"); err == nil {
		t.Fatal("非法头应拒绝")
	}
	_ = cli.Close()

	// UNKNOWN（转发器健康检查）：放行、保留原地址
	srv, cli = pipeWith("PROXY UNKNOWN\r\nping\n")
	c, err = resolveProxy(srv, "optional")
	if err != nil {
		t.Fatalf("UNKNOWN 应放行: %v", err)
	}
	buf = make([]byte, 5)
	if n, _ := c.Read(buf); string(buf[:n]) != "ping\n" {
		t.Fatalf("UNKNOWN 后数据丢失: %q", buf[:n])
	}
	_ = cli.Close()
}
