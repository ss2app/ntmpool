package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

// fakeHandler 用一个固定 job 回放，并把提交结果按 nonce 奇偶模拟 accepted/lowdiff。
type fakeHandler struct {
	reg  *JobRegistry
	subs []Submission
}

func newFakeHandler() *fakeHandler {
	reg := NewJobRegistry()
	cb, _ := btcwork.BuildCoinbase(101, 5000000000,
		[]byte{0x76, 0xa9, 0x14, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x88, 0xac},
		nil, []byte("/NTMPool/"), 8)
	reg.Put(&Job{
		ID:            "job1",
		Height:        101,
		PrevHashBE:    "000000000000000000024e9be1c7b56cab6428f9920f957c380b45f664db316f",
		Coinbase:      cb,
		MerkleBranch:  nil,
		Version:       0x20000000,
		Bits:          0x1d00ffff,
		NTime:         0x6543ffff,
		NetworkTarget: btcwork.Diff1Target,
		CleanJobs:     true,
	})
	return &fakeHandler{reg: reg}
}

func (h *fakeHandler) Registry() *JobRegistry { return h.reg }
func (h *fakeHandler) ExtraNonce2Size() int   { return 4 }
func (h *fakeHandler) HandleSubmit(_ context.Context, s Submission) SubmitResult {
	h.subs = append(h.subs, s)
	if s.Nonce%2 == 0 {
		return SubmitResult{Outcome: core.OutcomeAccepted, CreditDiff: s.RequiredDiff}
	}
	return SubmitResult{Outcome: core.OutcomeLowDiff}
}

func testPort() config.PortConfig {
	return config.PortConfig{
		Port: 3401, Mode: "pplns", Dialect: "stratum1", Enabled: true,
		Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 16, MinDiff: 1, MaxDiff: 1e6, TargetSeconds: 12},
	}
}

// 驱动一次完整握手：subscribe → set_difficulty + notify → authorize → submit。
func TestV1Handshake(t *testing.T) {
	h := newFakeHandler()
	d := NewV1Dialect("test", h)
	cli, srv := net.Pipe()
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, srv, testPort()) }()

	br := bufio.NewReader(cli)
	send := func(s string) {
		_ = cli.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = cli.Write([]byte(s + "\n"))
	}
	readMsg := func() map[string]any {
		_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读响应失败: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("响应非法 JSON: %q", line)
		}
		return m
	}

	// 1) subscribe
	send(`{"id":1,"method":"mining.subscribe","params":["cpuminer/2.5.1"]}`)
	sub := readMsg()
	if sub["id"].(float64) != 1 {
		t.Fatalf("subscribe 响应 id 错误: %v", sub["id"])
	}
	// 三件套：result + error 键都要在
	if _, ok := sub["error"]; !ok {
		t.Fatal("响应缺 error 键（NiceHash 硬要求）")
	}
	res := sub["result"].([]any)
	en1 := res[1].(string)
	if en1 == "" {
		t.Fatal("未分配 extranonce1")
	}
	if res[2].(float64) != 4 {
		t.Fatalf("extranonce2_size 应为 4, got %v", res[2])
	}

	// 2) 订阅后应主动收到 set_difficulty 与 notify
	m2 := readMsg()
	if m2["method"] != "mining.set_difficulty" {
		t.Fatalf("期望 set_difficulty, got %v", m2["method"])
	}
	if diff := m2["params"].([]any)[0].(float64); diff != 16 {
		t.Fatalf("初始难度应为 16, got %v", diff)
	}
	m3 := readMsg()
	if m3["method"] != "mining.notify" {
		t.Fatalf("期望 notify, got %v", m3["method"])
	}
	np := m3["params"].([]any)
	if np[0].(string) != "job1" {
		t.Fatalf("job id 错误: %v", np[0])
	}
	if np[8].(bool) != true {
		t.Fatal("首个 job clean_jobs 应为 true")
	}

	// 3) authorize
	send(`{"id":2,"method":"mining.authorize","params":["bc1qtestaddr.rig1","x"]}`)
	auth := readMsg()
	if auth["result"] != true {
		t.Fatalf("authorize 应成功: %v", auth)
	}
	readMsg() // authorize 后补推的 job

	// 4) submit（偶数 nonce → accepted）
	send(`{"id":3,"method":"mining.submit","params":["bc1qtestaddr.rig1","job1","00000004","6543ffff","00000002"]}`)
	sr := readMsg()
	if sr["result"] != true {
		t.Fatalf("偶数 nonce 应 accepted: %v", sr)
	}
	if len(h.subs) != 1 {
		t.Fatalf("handler 应收到 1 次提交, got %d", len(h.subs))
	}
	got := h.subs[0]
	if got.Address != "bc1qtestaddr" || got.Worker != "rig1" {
		t.Fatalf("地址/矿工名解析错误: %+v", got)
	}
	if got.UserAgent != "cpuminer/2.5.1" {
		t.Fatalf("user-agent 未记录: %q", got.UserAgent)
	}
	if got.Nonce != 2 || got.NTime != 0x6543ffff {
		t.Fatalf("nonce/ntime 解析错误: %+v", got)
	}

	// 5) 重复提交 → duplicate
	send(`{"id":4,"method":"mining.submit","params":["bc1qtestaddr.rig1","job1","00000004","6543ffff","00000002"]}`)
	dup := readMsg()
	if dup["error"] == nil {
		t.Fatal("重复提交应被拒")
	}

	// 6) 低难度（奇数 nonce）→ 拒
	send(`{"id":5,"method":"mining.submit","params":["bc1qtestaddr.rig1","job1","00000005","6543ffff","00000003"]}`)
	low := readMsg()
	if low["error"] == nil {
		t.Fatal("奇数 nonce 应 lowdiff 拒绝")
	}

	cancel()
	<-done
}

// mining.configure 即便不支持也必须回（ASIC 首条消息）。
func TestV1ConfigureAlwaysReplies(t *testing.T) {
	h := newFakeHandler()
	d := NewV1Dialect("test", h)
	cli, srv := net.Pipe()
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go d.Serve(ctx, srv, testPort())

	_, _ = cli.Write([]byte(`{"id":1,"method":"mining.configure","params":[["version-rolling"],{"version-rolling.mask":"1fffe000"}]}` + "\n"))
	br := bufio.NewReader(cli)
	_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(line), &m)
	if m["result"] == nil {
		t.Fatalf("configure 必须有 result: %q", line)
	}
	cancel()
}
