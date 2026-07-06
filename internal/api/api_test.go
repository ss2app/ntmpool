package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hashrate"
	"github.com/scashcc/ntmpool/internal/payout"
)

var (
	tNow  = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	addrA = "bcrt1qminerminerminerminerminermineraaaa" // 长地址（前6…后4）
	addrB = "bcrt1qotherotherotherotherotherotherbbbb"
)

// fakePool 拼真实部件（MemLedger/Tracker/MemBatchStore），只有节点数据是造的。
type fakePool struct {
	cfg     config.CoinConfig
	ledger  accounting.Ledger
	tracker *hashrate.Tracker
	batches payout.BatchStore
	net     core.NetworkSnapshot
	conns   int
}

func (f *fakePool) Cfg() config.CoinConfig          { return f.cfg }
func (f *fakePool) Ledger() accounting.Ledger       { return f.ledger }
func (f *fakePool) Hashrate() *hashrate.Tracker     { return f.tracker }
func (f *fakePool) Batches() payout.BatchStore      { return f.batches }
func (f *fakePool) ConnectedMiners() int            { return f.conns }
func (f *fakePool) Network() core.NetworkSnapshot   { return f.net }

func newFakePool(t *testing.T) *fakePool {
	t.Helper()
	ctx := context.Background()
	l := accounting.NewMemLedger(8, 2)
	// 两条 share + 一个确认块 + 一个孤块
	_ = l.RecordShare(ctx, core.Share{Coin: "tst", Address: addrA, Worker: "rig1", At: tNow}, 3000)
	_ = l.RecordShare(ctx, core.Share{Coin: "tst", Address: addrB, Worker: "", At: tNow}, 1000)
	bConf := core.FoundBlock{Coin: "tst", Height: 101, Hash: "hashConfirmed", Finder: addrA,
		Reward: "50.00000000", NetDiff: 2000, FoundAt: tNow.Add(-time.Hour)}
	bOrph := core.FoundBlock{Coin: "tst", Height: 102, Hash: "hashOrphaned", Finder: addrB,
		Reward: "50.00000000", NetDiff: 2000, FoundAt: tNow.Add(-30 * time.Minute)}
	bPend := core.FoundBlock{Coin: "tst", Height: 103, Hash: "hashPending", Finder: addrA,
		Reward: "50.00000000", NetDiff: 2000, FoundAt: tNow.Add(-time.Minute)}
	for _, b := range []core.FoundBlock{bConf, bOrph, bPend} {
		_ = l.RecordBlock(ctx, b, "raw")
	}
	if err := l.ConfirmBlock(ctx, bConf, 5); err != nil {
		t.Fatal(err)
	}
	if err := l.OrphanBlock(ctx, bOrph); err != nil {
		t.Fatal(err)
	}

	// 一笔已发出的打款批次（addrA 拿 35.625 = 47.5×0.75）
	_ = l.DeductForPayout(ctx, "tst", map[string]string{addrA: "30.00000000"}, 1)
	bs := payout.NewMemBatchStore()
	_ = bs.Save(&payout.Batch{
		ID: bs.NextBatchID(), Kind: "payout",
		Outputs: map[string]string{addrA: "30.00000000"},
		Status:  core.PaymentSent, TxID: "txid-abc", CreatedAt: tNow.Add(-10 * time.Minute),
	})

	tr := hashrate.New(hashrate.Config{})
	tr.Record(addrA, "rig1", 3000, tNow.Add(-time.Minute))
	tr.Record(addrB, "", 1000, tNow.Add(-time.Minute))

	return &fakePool{
		cfg: config.CoinConfig{
			ID: "tst", Symbol: "TST", Algo: "sha256d", PoolAddress: "poolAddrXYZ",
			Ports: []config.PortConfig{{Port: 3333, Mode: "pplns", Enabled: true,
				Vardiff: config.VardiffConfig{Enabled: true, StartDiff: 1024, MinDiff: 16, MaxDiff: 1 << 20, TargetSeconds: 10}}},
			Payout: config.PayoutConfig{Enabled: true, Scheme: "pplns", PplnsFactor: 2,
				FeePercent: 5, MinPayout: "0.01", Confirmations: 101},
		},
		ledger: l, tracker: tr, batches: bs,
		net:   core.NetworkSnapshot{Height: 150, Difficulty: 2000, HashPS: 1.5e9},
		conns: 2,
	}
}

func newTestServer(t *testing.T) (*Server, *fakePool) {
	t.Helper()
	fp := newFakePool(t)
	s := New(func() map[string]Pool { return map[string]Pool{"tst": fp} }, []byte("test-secret"))
	s.now = func() time.Time { return tNow }
	return s, fp
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

func TestPoolsShape(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools")
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, body)
	}
	var out struct {
		Pools []struct {
			ID   string `json:"id"`
			Coin struct {
				Algorithm string `json:"algorithm"`
			} `json:"coin"`
			Ports     map[string]json.RawMessage `json:"ports"`
			PoolStats struct {
				ConnectedMiners int     `json:"connectedMiners"`
				PoolHashrate    float64 `json:"poolHashrate"`
			} `json:"poolStats"`
			NetworkStats struct {
				NetworkHashrate   float64 `json:"networkHashrate"`
				NetworkDifficulty float64 `json:"networkDifficulty"`
				BlockHeight       uint64  `json:"blockHeight"`
			} `json:"networkStats"`
			TotalBlocks          int             `json:"totalBlocks"`
			TotalConfirmedBlocks int             `json:"totalConfirmedBlocks"`
			TotalOrphanedBlocks  int             `json:"totalOrphanedBlocks"`
			TotalPaid            json.RawMessage `json:"totalPaid"`
			PoolFeePercent       float64         `json:"poolFeePercent"`
		} `json:"pools"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, body)
	}
	if len(out.Pools) != 1 {
		t.Fatalf("pools=%d", len(out.Pools))
	}
	p := out.Pools[0]
	if p.ID != "tst" || p.Coin.Algorithm != "sha256d" || p.PoolFeePercent != 5 {
		t.Fatalf("基本字段错: %+v", p)
	}
	if _, ok := p.Ports["3333"]; !ok {
		t.Fatalf("ports 应含 3333: %v", p.Ports)
	}
	if p.PoolStats.ConnectedMiners != 2 || p.PoolStats.PoolHashrate <= 0 {
		t.Fatalf("poolStats 错: %+v", p.PoolStats)
	}
	if p.NetworkStats.BlockHeight != 150 || p.NetworkStats.NetworkHashrate != 1.5e9 {
		t.Fatalf("networkStats 错: %+v", p.NetworkStats)
	}
	if p.TotalBlocks != 3 || p.TotalConfirmedBlocks != 1 || p.TotalOrphanedBlocks != 1 {
		t.Fatalf("块计数错: %+v", p)
	}
	// 金额是 JSON number（30.00000000），不是字符串
	if string(p.TotalPaid) != "30.00000000" {
		t.Fatalf("totalPaid 应为裸数字: %s", p.TotalPaid)
	}
	// 完整地址绝不出现在列表输出（PoolAddress 除外）
	if strings.Contains(body, addrA) || strings.Contains(body, addrB) {
		t.Fatal("池列表泄漏完整矿工地址")
	}
}

func TestBlocksMaskedAndPaged(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools/tst/blocks?page=0&pageSize=2")
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if rec.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("X-Total-Count=%s", rec.Header().Get("X-Total-Count"))
	}
	var blocks []struct {
		BlockHeight          uint64  `json:"blockHeight"`
		Status               string  `json:"status"`
		ConfirmationProgress float64 `json:"confirmationProgress"`
		Miner                string  `json:"miner"`
		MinerID              string  `json:"minerId"`
		Hash                 string  `json:"hash"`
	}
	if err := json.Unmarshal([]byte(body), &blocks); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if len(blocks) != 2 {
		t.Fatalf("分页应 2 条: %d", len(blocks))
	}
	// 新→旧：103 pending 在前
	if blocks[0].BlockHeight != 103 || blocks[0].Status != "pending" {
		t.Fatalf("排序错: %+v", blocks[0])
	}
	// pending 进度 = (150-103+1)/101
	want := 48.0 / 101.0
	if diff := blocks[0].ConfirmationProgress - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("confirmationProgress=%v want %v", blocks[0].ConfirmationProgress, want)
	}
	if strings.Contains(body, addrA) {
		t.Fatal("blocks 泄漏完整地址")
	}
	if !strings.Contains(blocks[0].Miner, "…") {
		t.Fatalf("miner 应为脱敏形式: %s", blocks[0].Miner)
	}
	// 第二页只剩 1 条（confirmed，progress=1）
	_, body2 := get(t, s.Handler(), "/api/pools/tst/blocks?page=1&pageSize=2")
	var page2 []struct {
		Status               string  `json:"status"`
		ConfirmationProgress float64 `json:"confirmationProgress"`
	}
	_ = json.Unmarshal([]byte(body2), &page2)
	if len(page2) != 1 || page2[0].Status != "confirmed" || page2[0].ConfirmationProgress != 1 {
		t.Fatalf("第二页错: %+v", page2)
	}
}

func TestPaymentsMasked(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools/tst/payments")
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var pays []struct {
		Address                     string          `json:"address"`
		AddressID                   string          `json:"addressId"`
		Amount                      json.RawMessage `json:"amount"`
		TransactionConfirmationData string          `json:"transactionConfirmationData"`
		Status                      string          `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &pays); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if len(pays) != 1 {
		t.Fatalf("payments=%d", len(pays))
	}
	if pays[0].TransactionConfirmationData != "txid-abc" || pays[0].Status != "sent" {
		t.Fatalf("payment 内容错: %+v", pays[0])
	}
	if string(pays[0].Amount) != "30.00000000" {
		t.Fatalf("amount 应为裸数字: %s", pays[0].Amount)
	}
	if strings.Contains(body, addrA) {
		t.Fatal("payments 泄漏完整地址")
	}
}

func TestMinersListMaskedSorted(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools/tst/miners")
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var miners []minerRow
	if err := json.Unmarshal([]byte(body), &miners); err != nil {
		t.Fatal(err)
	}
	if len(miners) != 2 {
		t.Fatalf("miners=%d", len(miners))
	}
	if miners[0].Hashrate <= miners[1].Hashrate {
		t.Fatal("榜单未按算力降序")
	}
	if strings.Contains(body, addrA) || strings.Contains(body, addrB) {
		t.Fatal("miners 泄漏完整地址")
	}
	// 匿名 ID 稳定：与 blocks 里同矿工的 minerId 一致
	_, bbody := get(t, s.Handler(), "/api/pools/tst/blocks")
	if !strings.Contains(bbody, miners[0].MinerID) {
		t.Fatal("同一矿工的匿名 ID 在不同端点应一致")
	}
}

func TestMinerSelfQuery(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools/tst/miners/"+addrA)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, body)
	}
	var d struct {
		PendingBalance     json.RawMessage `json:"pendingBalance"`
		TotalPaid          json.RawMessage `json:"totalPaid"`
		LastPaymentTxid    string          `json:"lastPaymentTxid"`
		LastPaymentStatus  string          `json:"lastPaymentStatus"`
		Performance        *perfSample     `json:"performance"`
		PerformanceSamples []perfSample    `json:"performanceSamples"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	// addrA：块 101 confirm 后 PPLNS 3000/4000 × 47.5 = 35.625，已付 30 → 余 5.625
	if string(d.PendingBalance) != "5.62500000" {
		t.Fatalf("pendingBalance=%s", d.PendingBalance)
	}
	if string(d.TotalPaid) != "30.00000000" {
		t.Fatalf("totalPaid=%s", d.TotalPaid)
	}
	if d.LastPaymentTxid != "txid-abc" || d.LastPaymentStatus != "sent" {
		t.Fatalf("lastPayment 错: %+v", d)
	}
	if d.Performance == nil || d.Performance.Workers["rig1"].Hashrate <= 0 {
		t.Fatalf("performance 错: %+v", d.Performance)
	}
	if len(d.PerformanceSamples) == 0 {
		t.Fatal("performanceSamples 空")
	}
	// 未知地址 404
	rec2, _ := get(t, s.Handler(), "/api/pools/tst/miners/unknownaddr")
	if rec2.Code != 404 {
		t.Fatalf("未知矿工应 404: %d", rec2.Code)
	}
}

func TestMinerDetailRateLimit(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	var last int
	for i := 0; i < 70; i++ {
		rec, _ := get(t, h, "/api/pools/tst/miners/"+addrA)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("超 60 次/分应 429: %d", last)
	}
}

func TestPerformanceCurve(t *testing.T) {
	s, _ := newTestServer(t)
	rec, body := get(t, s.Handler(), "/api/pools/tst/performance")
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Stats []struct {
			PoolHashrate float64 `json:"poolHashrate"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Stats) == 0 || out.Stats[0].PoolHashrate <= 0 {
		t.Fatalf("曲线错: %+v", out.Stats)
	}
}

func TestUnknownPool404AndCORS(t *testing.T) {
	s, _ := newTestServer(t)
	rec, _ := get(t, s.Handler(), "/api/pools/nope")
	if rec.Code != 404 {
		t.Fatalf("未知池应 404: %d", rec.Code)
	}
	rec2, _ := get(t, s.Handler(), "/api/pools")
	if rec2.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("缺 CORS 头")
	}
}

func TestNumHelper(t *testing.T) {
	cases := map[string]string{
		"":            "0",
		"1.23":        "1.23",
		"-0.50000000": "-0.50000000",
		"abc":         `"abc"`, // 防御：非数字退化为字符串，不产非法 JSON
		"1.":          `"1."`,
		"-":           `"-"`,
	}
	for in, want := range cases {
		if got := string(num(in)); got != want {
			t.Fatalf("num(%q)=%s want %s", in, got, want)
		}
	}
}
