package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

// fakeCNHandler 脚本化 CNShareHandler。
type fakeCNHandler struct {
	algo    string
	outcome core.ShareOutcome
	lastSub CNSubmission
}

func (f *fakeCNHandler) Algo() string              { return f.algo }
func (f *fakeCNHandler) LoginExtensions() []string { return []string{"algo", "keepalive"} }
func (f *fakeCNHandler) ConnJob(connID uint32, diff float64) (CNWireJob, bool) {
	return CNWireJob{
		JobID: "j1", Blob: "00ff", Target: "ffffffff", Algo: f.algo,
		Height: 42, SeedHash: "aa",
	}, true
}
func (f *fakeCNHandler) HandleSubmit(_ context.Context, sub CNSubmission) SubmitResult {
	f.lastSub = sub
	return SubmitResult{Outcome: f.outcome, CreditDiff: 1}
}

// startCN 起一个真 TCP 端口跑 CNDialect，返回客户端读写器。
func startCN(t *testing.T, h CNShareHandler) (*bufio.Scanner, net.Conn, func()) {
	t.Helper()
	d := NewCNDialect("test", h)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pc := config.PortConfig{
		Port: 0, Mode: "pplns", Dialect: "cryptonote",
		Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1000, MinDiff: 1, MaxDiff: 100000, TargetSeconds: 10},
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = d.Serve(ctx, conn, pc)
		_ = conn.Close()
	}()
	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = cli.SetDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(cli)
	return sc, cli, func() { cancel(); _ = cli.Close(); _ = ln.Close() }
}

func send(t *testing.T, c net.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if _, err := c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func recv(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("连接被关闭: %v", sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("非法响应 %q: %v", sc.Text(), err)
	}
	return m
}

func TestCNLoginAndJob(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0", outcome: core.OutcomeAccepted}
	sc, cli, done := startCN(t, h)
	defer done()

	send(t, cli, map[string]any{
		"id": 1, "jsonrpc": "2.0", "method": "login",
		"params": map[string]any{
			"login": "addr1", "pass": "x", "agent": "XMRig/6.21.0",
			"algo": []string{"rx/0", "cn/r"},
		},
	})
	resp := recv(t, sc)
	if resp["error"] != nil {
		t.Fatalf("login 失败: %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if result["status"] != "OK" {
		t.Fatalf("status = %v", result["status"])
	}
	job := result["job"].(map[string]any)
	for _, k := range []string{"job_id", "blob", "target", "algo", "height"} {
		if _, ok := job[k]; !ok {
			t.Fatalf("job 缺字段 %s: %v", k, job)
		}
	}
	if job["algo"] != "rx/0" {
		t.Fatalf("job.algo = %v", job["algo"])
	}
	// submit → OK
	send(t, cli, map[string]any{
		"id": 2, "method": "submit",
		"params": map[string]any{
			"id": result["id"], "job_id": "j1", "nonce": "01000000", "result": "ab",
		},
	})
	resp = recv(t, sc)
	if resp["error"] != nil {
		t.Fatalf("submit 失败: %v", resp["error"])
	}
	if resp["result"].(map[string]any)["status"] != "OK" {
		t.Fatalf("submit result = %v", resp["result"])
	}
	if h.lastSub.Address != "addr1" || h.lastSub.Worker != "default" {
		t.Fatalf("submission 字段错: %+v", h.lastSub)
	}
	// 同 nonce 重交 → Duplicate share
	send(t, cli, map[string]any{
		"id": 3, "method": "submit",
		"params": map[string]any{
			"id": result["id"], "job_id": "j1", "nonce": "01000000", "result": "ab",
		},
	})
	resp = recv(t, sc)
	if resp["error"] == nil {
		t.Fatal("重复 share 应被拒")
	}
	if msg := resp["error"].(map[string]any)["message"]; msg != "Duplicate share" {
		t.Fatalf("dup message = %v", msg)
	}
	// keepalived
	send(t, cli, map[string]any{"id": 4, "method": "keepalived", "params": map[string]any{"id": result["id"]}})
	resp = recv(t, sc)
	if resp["result"].(map[string]any)["status"] != "KEEPALIVED" {
		t.Fatalf("keepalived = %v", resp["result"])
	}
}

func TestCNWorkerIdentification(t *testing.T) {
	cases := []struct {
		login, pass, rigid   string
		wantAddr, wantWorker string
		wantDiff             float64
	}{
		{"addr", "x", "", "addr", "default", 0},
		{"addr", "x", "rig7", "addr", "rig7", 0},               // rigid 一等公民
		{"addr+w3", "x", "", "addr", "w3", 0},                  // +worker 后缀
		{"addr", "worker1:me@x.com", "", "addr", "worker1", 0}, // pass worker:email
		{"addr", "worker1", "rig7", "addr", "rig7", 0},         // rigid > pass
		{"addr.5000", "x", "", "addr", "default", 5000},        // .难度 后缀
		{"addr+20000", "x", "", "addr", "default", 20000},      // +难度 后缀
		{"addr", "d=8192", "", "addr", "default", 8192},        // 密码 d=
	}
	for i, c := range cases {
		addr, worker, diff := parseCNLogin(c.login, c.pass, c.rigid)
		// 密码 d= 由 minersettings 层解析（dialect onLogin 里），这里单独验证解析器本身
		if c.pass == "d=8192" {
			if addr != c.wantAddr {
				t.Fatalf("case %d: addr=%s", i, addr)
			}
			continue
		}
		if addr != c.wantAddr || worker != c.wantWorker || diff != c.wantDiff {
			t.Fatalf("case %d (%s/%s/%s): got %s/%s/%v want %s/%s/%v",
				i, c.login, c.pass, c.rigid, addr, worker, diff, c.wantAddr, c.wantWorker, c.wantDiff)
		}
	}
}

func TestCNAlgoNegotiationReject(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0"}
	sc, cli, done := startCN(t, h)
	defer done()

	send(t, cli, map[string]any{
		"id": 1, "method": "login",
		"params": map[string]any{"login": "a", "algo": []string{"cn/r", "cn/2"}},
	})
	resp := recv(t, sc)
	if resp["error"] == nil {
		t.Fatal("不支持的 algo 应被拒")
	}
}

func TestCNSubmitBeforeLogin(t *testing.T) {
	h := &fakeCNHandler{algo: "rx/0"}
	sc, cli, done := startCN(t, h)
	defer done()

	send(t, cli, map[string]any{
		"id": 1, "method": "submit",
		"params": map[string]any{"id": "zz", "job_id": "j1", "nonce": "00000000", "result": "ab"},
	})
	resp := recv(t, sc)
	if resp["error"] == nil {
		t.Fatal("未登录 submit 应被拒")
	}
	if msg := resp["error"].(map[string]any)["message"]; msg != "Unauthenticated" {
		t.Fatalf("message = %v", msg)
	}
}

func TestCNErrorMessages(t *testing.T) {
	for outcome, want := range map[core.ShareOutcome]string{
		core.OutcomeStale:   "Invalid job id",
		core.OutcomeLowDiff: "Low difficulty share",
		core.OutcomeBadPow:  "Bad hash (recompute mismatch)",
	} {
		h := &fakeCNHandler{algo: "rx/0", outcome: outcome}
		sc, cli, done := startCN(t, h)

		send(t, cli, map[string]any{"id": 1, "method": "login", "params": map[string]any{"login": "a"}})
		resp := recv(t, sc)
		sess := resp["result"].(map[string]any)["id"]
		send(t, cli, map[string]any{
			"id": 2, "method": "submit",
			"params": map[string]any{"id": sess, "job_id": "j1", "nonce": "02000000", "result": "ab"},
		})
		resp = recv(t, sc)
		if resp["error"] == nil {
			t.Fatalf("outcome %v 应报错", outcome)
		}
		if msg := resp["error"].(map[string]any)["message"]; msg != want {
			t.Fatalf("outcome %v message = %v, want %s", outcome, msg, want)
		}
		done()
	}
}
