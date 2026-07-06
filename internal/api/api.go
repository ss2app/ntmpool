// Package api 公共只读 API（miningcore 形状，docs/02 §7）。
//
// 设计要点：
//   - 形状对齐 miningcore（/api/pools、/blocks、/payments、/miners、/miners/{addr}、
//     /performance），前端零改接入；NTMPool 超越点以【追加字段】体现（打款状态机、
//     孤块 debt 透明），多余字段 miningcore 前端自动忽略。
//   - 隐私（R14.6）：列表一律脱敏（前6…后4 + HMAC 匿名 ID）；矿工用完整地址自查
//     自己的明细（per-IP 限速防枚举）。
//   - 真实数据铁律：全部指标来自实测 share / 节点真值缓存；取不到的字段省略，不造 0。
//   - 金额铁律：内部十进制字符串直通为 JSON number（json.RawMessage），不过 float。
//   - API 层零 RPC：一切链上数据读 coininstance 的周期缓存。
package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hashrate"
	"github.com/scashcc/ntmpool/internal/payout"
)

// Pool 是 API 对一个运行中币实例的只读视图（coininstance.Instance 实现）。
type Pool interface {
	Cfg() config.CoinConfig
	Ledger() accounting.Ledger
	Hashrate() *hashrate.Tracker
	Batches() payout.BatchStore
	ConnectedMiners() int
	Network() core.NetworkSnapshot
}

// Server 公共 API。
type Server struct {
	pools   func() map[string]Pool // 快照函数：热添加币后 API 自动可见
	masker  *Masker
	limiter *rateLimiter
	now     func() time.Time // 可注入（测试）
}

func New(pools func() map[string]Pool, maskSecret []byte) *Server {
	return &Server{
		pools:   pools,
		masker:  NewMasker(maskSecret),
		limiter: newRateLimiter(60, time.Minute),
		now:     time.Now,
	}
}

// Handler 返回挂好全部路由的 http.Handler（含 CORS）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pools", s.handlePools)
	mux.HandleFunc("GET /api/pools/{id}", s.handlePool)
	mux.HandleFunc("GET /api/pools/{id}/blocks", s.handleBlocks)
	mux.HandleFunc("GET /api/pools/{id}/payments", s.handlePayments)
	mux.HandleFunc("GET /api/pools/{id}/miners", s.handleMiners)
	mux.HandleFunc("GET /api/pools/{id}/miners/{addr}", s.handleMinerDetail)
	mux.HandleFunc("GET /api/pools/{id}/performance", s.handlePerformance)
	return cors(mux)
}

// cors 公共 API 只读，放开跨域（前端网站直连）。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- 响应形状（miningcore 对齐 + 追加字段）----

type coinInfo struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Algorithm string `json:"algorithm"`
}

type varDiffInfo struct {
	MinDiff        float64 `json:"minDiff"`
	MaxDiff        float64 `json:"maxDiff"`
	TargetTime     float64 `json:"targetTime"`
	RetargetTime   float64 `json:"retargetTime"`
}

type portInfo struct {
	Difficulty float64      `json:"difficulty"`
	VarDiff    *varDiffInfo `json:"varDiff,omitempty"`
	TLS        bool         `json:"tls"`
	Solo       bool         `json:"solo"`
}

type paymentProcessingInfo struct {
	Enabled             bool            `json:"enabled"`
	MinimumPayment      json.RawMessage `json:"minimumPayment"`
	PayoutScheme        string          `json:"payoutScheme"`
	PayoutSchemeConfig  map[string]any  `json:"payoutSchemeConfig"`
}

type poolStatsInfo struct {
	ConnectedMiners  int     `json:"connectedMiners"`
	ConnectedWorkers int     `json:"connectedWorkers"`
	PoolHashrate     float64 `json:"poolHashrate"`
	SharesPerSecond  float64 `json:"sharesPerSecond"`
}

type networkStatsInfo struct {
	NetworkHashrate   float64 `json:"networkHashrate,omitempty"` // 0=尚未取到 → 省略，不造 0
	NetworkDifficulty float64 `json:"networkDifficulty"`
	BlockHeight       uint64  `json:"blockHeight"`
}

type poolInfo struct {
	ID                    string                `json:"id"`
	Coin                  coinInfo              `json:"coin"`
	Ports                 map[string]portInfo   `json:"ports"`
	PaymentProcessing     paymentProcessingInfo `json:"paymentProcessing"`
	PoolFeePercent        float64               `json:"poolFeePercent"`
	Address               string                `json:"address"` // 池地址链上公开，无需脱敏
	PoolStats             poolStatsInfo         `json:"poolStats"`
	NetworkStats          networkStatsInfo      `json:"networkStats"`
	TotalBlocks           int                   `json:"totalBlocks"`
	TotalConfirmedBlocks  int                   `json:"totalConfirmedBlocks"`
	TotalOrphanedBlocks   int                   `json:"totalOrphanedBlocks"`
	TotalPaid             json.RawMessage       `json:"totalPaid"`
}

type blockInfo struct {
	PoolID               string          `json:"poolId"`
	BlockHeight          uint64          `json:"blockHeight"`
	NetworkDifficulty    float64         `json:"networkDifficulty"`
	Status               string          `json:"status"`
	ConfirmationProgress float64         `json:"confirmationProgress"`
	Reward               json.RawMessage `json:"reward"`
	Hash                 string          `json:"hash"`
	Miner                string          `json:"miner"`   // 脱敏展示
	MinerID              string          `json:"minerId"` // HMAC 匿名 ID
	Worker               string          `json:"worker,omitempty"`
	Solo                 bool            `json:"solo,omitempty"`
	Created              string          `json:"created"`
}

type paymentInfo struct {
	Coin                        string          `json:"coin"`
	Address                     string          `json:"address"`   // 脱敏展示
	AddressID                   string          `json:"addressId"` // HMAC 匿名 ID
	Amount                      json.RawMessage `json:"amount"`
	TransactionConfirmationData string          `json:"transactionConfirmationData"` // txid
	Status                      string          `json:"status"`                      // 超越点：打款状态机对外透明
	Created                     string          `json:"created"`
}

type minerRow struct {
	Miner           string  `json:"miner"`
	MinerID         string  `json:"minerId"`
	Hashrate        float64 `json:"hashrate"`
	SharesPerSecond float64 `json:"sharesPerSecond"`
}

type workerPerf struct {
	Hashrate        float64 `json:"hashrate"`
	SharesPerSecond float64 `json:"sharesPerSecond"`
}

type perfSample struct {
	Created         string                `json:"created"`
	Hashrate        float64               `json:"hashrate"`
	SharesPerSecond float64               `json:"sharesPerSecond"`
	Workers         map[string]workerPerf `json:"workers,omitempty"`
}

type minerDetail struct {
	PendingBalance     json.RawMessage `json:"pendingBalance"`
	TotalPaid          json.RawMessage `json:"totalPaid"`
	Debt               json.RawMessage `json:"debt,omitempty"` // 超越点：孤块追缴余欠透明
	LastPayment        string          `json:"lastPayment,omitempty"`
	LastPaymentTxid    string          `json:"lastPaymentTxid,omitempty"`
	LastPaymentAmount  json.RawMessage `json:"lastPaymentAmount,omitempty"`
	LastPaymentStatus  string          `json:"lastPaymentStatus,omitempty"`
	Performance        *perfSample     `json:"performance,omitempty"`
	PerformanceSamples []perfSample    `json:"performanceSamples"`
}

// ---- handlers ----

func (s *Server) pool(r *http.Request) (Pool, bool) {
	p, ok := s.pools()[r.PathValue("id")]
	return p, ok
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	all := s.pools()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := struct {
		Pools []poolInfo `json:"pools"`
	}{Pools: []poolInfo{}}
	for _, id := range ids {
		out.Pools = append(out.Pools, s.poolInfo(r.Context(), all[id]))
	}
	writeJSON(w, out)
}

func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, struct {
		Pool poolInfo `json:"pool"`
	}{s.poolInfo(r.Context(), p)})
}

func (s *Server) poolInfo(ctx context.Context, p Pool) poolInfo {
	cfg := p.Cfg()
	now := s.now()
	hr := p.Hashrate().Pool(now)
	stats, _ := p.Ledger().Snapshot(ctx, cfg.ID)
	net := p.Network()

	ports := map[string]portInfo{}
	for _, pc := range cfg.Ports {
		if !pc.Enabled {
			continue
		}
		pi := portInfo{Difficulty: pc.Vardiff.StartDiff, TLS: pc.TLS, Solo: pc.Mode == "solo"}
		if pc.Vardiff.Enabled {
			pi.VarDiff = &varDiffInfo{
				MinDiff: pc.Vardiff.MinDiff, MaxDiff: pc.Vardiff.MaxDiff,
				TargetTime: pc.Vardiff.TargetSeconds, RetargetTime: pc.Vardiff.RetargetMinSec,
			}
		}
		ports[strconv.Itoa(pc.Port)] = pi
	}

	return poolInfo{
		ID: cfg.ID,
		Coin: coinInfo{
			Type: cfg.Symbol, Name: cfg.Symbol, Symbol: cfg.Symbol, Algorithm: cfg.Algo,
		},
		Ports: ports,
		PaymentProcessing: paymentProcessingInfo{
			Enabled:        cfg.Payout.Enabled,
			MinimumPayment: num(cfg.Payout.MinPayout),
			PayoutScheme:   strings.ToUpper(cfg.Payout.Scheme),
			PayoutSchemeConfig: map[string]any{
				"factor": cfg.Payout.PplnsFactor,
			},
		},
		PoolFeePercent: cfg.Payout.FeePercent,
		Address:        cfg.PoolAddress,
		PoolStats: poolStatsInfo{
			ConnectedMiners:  p.ConnectedMiners(),
			ConnectedWorkers: hr.Workers,
			PoolHashrate:     hr.Hashrate,
			SharesPerSecond:  hr.SharesPerSecond,
		},
		NetworkStats: networkStatsInfo{
			NetworkHashrate:   net.HashPS,
			NetworkDifficulty: net.Difficulty,
			BlockHeight:       net.Height,
		},
		TotalBlocks:          stats.BlocksFound,
		TotalConfirmedBlocks: stats.Confirmed,
		TotalOrphanedBlocks:  stats.Orphaned,
		TotalPaid:            num(stats.TotalPaid),
	}
}

func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	page, size := paging(r)
	blocks, total, err := p.Ledger().Blocks(r.Context(), p.Cfg().ID, page*size, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	net := p.Network()
	maturity := p.Cfg().Payout.Confirmations
	out := make([]blockInfo, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockInfo{
			PoolID:               b.Coin,
			BlockHeight:          b.Height,
			NetworkDifficulty:    b.NetDiff,
			Status:               string(b.Status),
			ConfirmationProgress: confirmProgress(b, net.Height, maturity),
			Reward:               num(b.Reward),
			Hash:                 b.Hash,
			Miner:                s.masker.Display(b.Finder),
			MinerID:              s.masker.ID(b.Finder),
			Worker:               b.Worker,
			Solo:                 b.Solo,
			Created:              b.FoundAt.UTC().Format(time.RFC3339),
		})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, out)
}

// confirmProgress 确认进度：pending 按 (链高-块高+1)/maturity 截断到 [0,1]；
// confirmed=1；orphaned=0。链高未知（缓存未就绪）时 pending 报 0，不瞎猜。
func confirmProgress(b core.FoundBlock, tip uint64, maturity int64) float64 {
	switch b.Status {
	case core.BlockConfirmed:
		return 1
	case core.BlockOrphaned:
		return 0
	}
	if maturity <= 0 || tip == 0 || tip < b.Height {
		return 0
	}
	prog := float64(tip-b.Height+1) / float64(maturity)
	if prog > 1 {
		prog = 1
	}
	return prog
}

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	page, size := paging(r)
	rows := flattenPayments(p, s.masker, "")
	total := len(rows)
	lo, hi := pageBounds(total, page, size)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, rows[lo:hi])
}

// flattenPayments 批次 → 逐地址打款行（新→旧）。onlyAddr 非空时只留该地址（自查）。
func flattenPayments(p Pool, m *Masker, onlyAddr string) []paymentInfo {
	coin := p.Cfg().ID
	rows := []paymentInfo{}
	for _, b := range p.Batches().All() {
		if b.Kind != "payout" {
			continue
		}
		addrs := make([]string, 0, len(b.Outputs))
		for a := range b.Outputs {
			if onlyAddr != "" && a != onlyAddr {
				continue
			}
			addrs = append(addrs, a)
		}
		sort.Strings(addrs) // map 序随机，排序保证分页稳定
		for _, a := range addrs {
			rows = append(rows, paymentInfo{
				Coin:                        coin,
				Address:                     m.Display(a),
				AddressID:                   m.ID(a),
				Amount:                      num(b.Outputs[a]),
				TransactionConfirmationData: b.TxID,
				Status:                      string(b.Status),
				Created:                     b.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
	}
	return rows
}

func (s *Server) handleMiners(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	page, size := paging(r)
	miners := p.Hashrate().Miners(s.now())
	total := len(miners)
	lo, hi := pageBounds(total, page, size)
	out := make([]minerRow, 0, hi-lo)
	for _, m := range miners[lo:hi] {
		out = append(out, minerRow{
			Miner:           s.masker.Display(m.Addr),
			MinerID:         s.masker.ID(m.Addr),
			Hashrate:        m.Hashrate,
			SharesPerSecond: m.SharesPerSecond,
		})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, out)
}

func (s *Server) handleMinerDetail(w http.ResponseWriter, r *http.Request) {
	// 完整地址自查密链（R14.6）：限速防枚举
	if !s.limiter.allow(clientIP(r), s.now()) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	addr := r.PathValue("addr")
	now := s.now()

	sum, hasLedger, err := p.Ledger().MinerSummary(r.Context(), p.Cfg().ID, addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hr, hasShares := p.Hashrate().Miner(addr, now)
	if !hasLedger && !hasShares {
		http.NotFound(w, r)
		return
	}

	d := minerDetail{
		PendingBalance:     num(sum.Balance),
		TotalPaid:          num(sum.TotalPaid),
		PerformanceSamples: []perfSample{},
	}
	if sum.Debt != "" && !isZeroNum(sum.Debt) {
		d.Debt = num(sum.Debt)
	}
	if hasShares {
		perf := perfSample{
			Created:         now.UTC().Format(time.RFC3339),
			Hashrate:        hr.Hashrate,
			SharesPerSecond: hr.SharesPerSecond,
			Workers:         map[string]workerPerf{},
		}
		for name, ws := range hr.Workers {
			perf.Workers[name] = workerPerf{Hashrate: ws.Hashrate, SharesPerSecond: ws.SharesPerSecond}
		}
		d.Performance = &perf
	}
	for _, sm := range p.Hashrate().MinerSamples(addr, now) {
		ps := perfSample{
			Created:         sm.Created.UTC().Format(time.RFC3339),
			Hashrate:        sm.Hashrate,
			SharesPerSecond: sm.SharesPerSecond,
			Workers:         map[string]workerPerf{},
		}
		for name, ws := range sm.Workers {
			ps.Workers[name] = workerPerf{Hashrate: ws.Hashrate, SharesPerSecond: ws.SharesPerSecond}
		}
		d.PerformanceSamples = append(d.PerformanceSamples, ps)
	}
	// 最近一笔打款（自查看全量，不脱敏自己的数据）
	if pays := flattenPayments(p, s.masker, addr); len(pays) > 0 {
		last := pays[0]
		d.LastPayment = last.Created
		d.LastPaymentTxid = last.TransactionConfirmationData
		d.LastPaymentAmount = last.Amount
		d.LastPaymentStatus = last.Status
	}
	writeJSON(w, d)
}

func (s *Server) handlePerformance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pool(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	samples := p.Hashrate().PoolSamples(s.now())
	type stat struct {
		Created         string  `json:"created"`
		PoolHashrate    float64 `json:"poolHashrate"`
		SharesPerSecond float64 `json:"sharesPerSecond"`
	}
	out := struct {
		Stats []stat `json:"stats"`
	}{Stats: []stat{}}
	for _, sm := range samples {
		out.Stats = append(out.Stats, stat{
			Created:         sm.Created.UTC().Format(time.RFC3339),
			PoolHashrate:    sm.Hashrate,
			SharesPerSecond: sm.SharesPerSecond,
		})
	}
	writeJSON(w, out)
}

// ---- 工具 ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// num 十进制字符串直通为 JSON number（金额铁律：不过 float）。
// 空串补 0；防御：非数字形状退化为带引号字符串，绝不输出非法 JSON。
func num(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("0")
	}
	dot := false
	for i, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c == '-' && i == 0:
		case c == '.' && !dot && i > 0 && i < len(s)-1:
			dot = true
		default:
			return json.RawMessage(strconv.Quote(s))
		}
	}
	if s == "-" {
		return json.RawMessage(strconv.Quote(s))
	}
	return json.RawMessage(s)
}

func isZeroNum(s string) bool {
	for _, c := range s {
		if c != '0' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// paging ?page=0&pageSize=20（miningcore 惯例，0 起）。pageSize 封顶 100。
func paging(r *http.Request) (page, size int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 0 {
		page = 0
	}
	size, _ = strconv.Atoi(r.URL.Query().Get("pageSize"))
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func pageBounds(total, page, size int) (lo, hi int) {
	lo = page * size
	if lo > total {
		lo = total
	}
	hi = lo + size
	if hi > total {
		hi = total
	}
	return lo, hi
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter 每 IP 滑动窗口限速（矿工自查端点防枚举，R14.6）。
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	// 顺手清理别的过期 key，防 map 无限涨
	if len(l.hits) > 4096 {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true
}
