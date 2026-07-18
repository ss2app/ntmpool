// Package admin 管理后台 API（docs/02 §7、docs/01 R14.1）。
//
// 一切热操作的唯一入口：打款参数/端口增删启停/币开关/payouts 开关、ban 管理、
// 对账触发、fee sweep/collect、矿工设置重置、实例状态。
//
// 安全模型：
//   - 独立端口（建议只绑内网/隧道），Bearer token（constant-time 比较）；
//     token 未配置 = 整个管理面拒绝服务（宁可不可用也不裸奔）。
//   - 每次变更：先应用到运行实例 → 写 config_audit.jsonl（谁/何时/改了什么）
//     → 落盘 config.state.json（重启以最后热状态为准）。审计写失败只告警不回滚
//     （变更已生效，审计尽力而为并大声记日志）。
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/banlist"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/minersettings"
)

// CoinControl 管理面对一个运行中币实例的操作视图（coininstance.Instance 实现）。
type CoinControl interface {
	Cfg() config.CoinConfig
	ApplyPayout(config.PayoutConfig)
	AddPort(config.PortConfig) error
	RemovePort(port int) error
	SetPortEnabled(port int, enabled bool) error
	SetNewConns(bool)
	RunPayoutNow(context.Context) error
	RunReconcile(context.Context) (delta string, err error)
	PayoutFrozen() bool
	UnfreezePayout()
	FeeSweep(ctx context.Context, coldAddress, amount string) (txid string, err error)
	FeeCollect(ctx context.Context, amount string) (txid string, err error)
	WriteOffDebt(ctx context.Context, address, amount, reason string) error
	ManualAdjust(ctx context.Context, address, amount string, credit bool, reason string) error
	// ForceOrphanBlock 人工按孤块回滚已确认块。
	ForceOrphanBlock(ctx context.Context, hash string, height int64) error
	// VoidPayoutBatch 退款并作废从未上链的已确认打款批次。
	VoidPayoutBatch(ctx context.Context, batchID int64) error
	RecordIncident(ctx context.Context, id, kind, recipient, amount, memo string) error
	ResolveIncident(ctx context.Context, id, outcome, amount, memo string) error
	UncollectedFees(ctx context.Context) (string, error)
	Connections() int
	Network() core.NetworkSnapshot
}

// Server 管理后台。
type Server struct {
	version    string
	instanceID string
	started    time.Time
	token      string
	pools      func() map[string]CoinControl
	bans       *banlist.List        // 可为 nil（未启用）
	settings   *minersettings.Store // 可为 nil

	statePath      string // config.state.json；空 = 不落盘
	auditPath      string // config_audit.jsonl；空 = 不审计
	mu             sync.Mutex
	now            func() time.Time
	actor          string
	lastAuditNanos int64
}

func New(version, instanceID, token string, pools func() map[string]CoinControl,
	bans *banlist.List, settings *minersettings.Store, statePath, auditPath string) *Server {
	return &Server{
		version: version, instanceID: instanceID, started: time.Now(),
		token: token, pools: pools, bans: bans, settings: settings,
		statePath: statePath, auditPath: auditPath, now: time.Now, actor: "admin-token",
	}
}

// Handler 全部路由（自带鉴权中间件）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "pong")
	})
	mux.HandleFunc("GET /admin/v1/status", s.handleStatus)
	mux.HandleFunc("GET /admin/v1/coins/{id}", s.handleCoin)
	mux.HandleFunc("PATCH /admin/v1/coins/{id}/payout", s.handlePayoutPatch)
	mux.HandleFunc("POST /admin/v1/coins/{id}/payout/run", s.handlePayoutRun)
	mux.HandleFunc("POST /admin/v1/coins/{id}/reconcile", s.handleReconcile)
	mux.HandleFunc("POST /admin/v1/coins/{id}/unfreeze", s.handleUnfreeze)
	mux.HandleFunc("POST /admin/v1/coins/{id}/feesweep", s.handleFeeSweep)
	mux.HandleFunc("POST /admin/v1/coins/{id}/feecollect", s.handleFeeCollect)
	mux.HandleFunc("POST /admin/v1/coins/{id}/debt/writeoff", s.handleDebtWriteOff)
	mux.HandleFunc("POST /admin/v1/coins/{id}/balance/adjust", s.handleBalanceAdjust)
	mux.HandleFunc("POST /admin/v1/coins/{id}/blocks/orphan", s.handleBlockOrphan)
	mux.HandleFunc("POST /admin/v1/coins/{id}/payout/void", s.handlePayoutVoid)
	mux.HandleFunc("POST /admin/v1/coins/{id}/incident/record", s.handleIncidentRecord)
	mux.HandleFunc("POST /admin/v1/coins/{id}/incident/resolve", s.handleIncidentResolve)
	mux.HandleFunc("POST /admin/v1/coins/{id}/ports", s.handlePortAdd)
	mux.HandleFunc("PATCH /admin/v1/coins/{id}/ports/{port}", s.handlePortPatch)
	mux.HandleFunc("DELETE /admin/v1/coins/{id}/ports/{port}", s.handlePortDelete)
	mux.HandleFunc("PATCH /admin/v1/coins/{id}/newconns", s.handleNewConns)
	mux.HandleFunc("GET /admin/v1/bans", s.handleBansList)
	mux.HandleFunc("POST /admin/v1/bans", s.handleBanAdd)
	mux.HandleFunc("POST /admin/v1/bans/delete", s.handleBanDelete) // target 可含 '/'（CIDR），不走路径参数
	mux.HandleFunc("GET /admin/v1/coins/{id}/miners/{addr}/settings", s.handleMinerSettingsGet)
	mux.HandleFunc("PUT /admin/v1/coins/{id}/miners/{addr}/settings", s.handleMinerSettingsPut)
	return s.auth(mux)
}

// auth Bearer token 鉴权。token 未配置 = 全拒。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			http.Error(w, "admin API disabled (no token configured)", http.StatusForbidden)
			return
		}
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.token
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- 审计 + 热状态落盘 ----

type auditRecord struct {
	ID       string `json:"id,omitempty"`
	Time     string `json:"time"`
	Remote   string `json:"remote"`
	Actor    string `json:"actor,omitempty"`
	Action   string `json:"action"`
	Coin     string `json:"coin,omitempty"`
	Field    string `json:"field,omitempty"`
	NewValue string `json:"newvalue,omitempty"`
	Detail   any    `json:"detail,omitempty"`
}

// commit 变更后置动作：审计 + 落盘热状态。失败不回滚（变更已生效），大声记日志。
func (s *Server) commit(r *http.Request, action, coin string, detail any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditPath != "" {
		rec := auditRecord{
			Time: s.now().UTC().Format(time.RFC3339), Remote: r.RemoteAddr,
			Actor: s.actor, Action: action, Coin: coin, Detail: detail,
		}
		if b, err := json.Marshal(rec); err == nil {
			f, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				_, _ = f.Write(append(b, '\n'))
				_ = f.Close()
			} else {
				log.Printf("[admin] ⚠ 审计写入失败: %v", err)
			}
		}
	}
	if s.statePath != "" {
		st := config.HotState{Coins: map[string]config.CoinHotState{}}
		for id, p := range s.pools() {
			c := p.Cfg()
			st.Coins[id] = config.CoinHotState{
				Payout: c.Payout, Ports: c.Ports, NewConnsEnabled: c.NewConnsEnabled,
			}
		}
		if err := config.SaveState(s.statePath, st); err != nil {
			log.Printf("[admin] ⚠ 热状态落盘失败: %v", err)
		}
	}
	log.Printf("[admin] %s coin=%s from=%s", action, coin, r.RemoteAddr)
}

// prepareLedgerAudit 在任何账本人工操作之前同步写入 config_audit.jsonl，并把唯一 id
// 放入 context 供 Ledger 构造 business_key。现有审计是文件而非数据库自增行，因此这里
// 使用单调纳秒时间戳 + 加密随机后缀；newvalue 同时保存 id 与完整参数摘要。
func (s *Server) prepareLedgerAudit(r *http.Request, coin, field string, detail any) (context.Context, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditPath == "" {
		return nil, "", fmt.Errorf("config_audit 未配置，拒绝未审计账本操作")
	}
	nanos := s.now().UTC().UnixNano()
	if nanos <= s.lastAuditNanos {
		nanos = s.lastAuditNanos + 1
	}
	s.lastAuditNanos = nanos
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return nil, "", fmt.Errorf("生成 config_audit id: %w", err)
	}
	auditID := fmt.Sprintf("%019d-%x", nanos, random)
	newValue, err := json.Marshal(map[string]any{"auditId": auditID, "params": detail})
	if err != nil {
		return nil, "", fmt.Errorf("编码 config_audit 参数: %w", err)
	}
	rec := auditRecord{
		ID: auditID, Time: s.now().UTC().Format(time.RFC3339Nano), Remote: r.RemoteAddr,
		Actor: s.actor, Action: "ledger.operation.requested", Coin: coin,
		Field: field, NewValue: string(newValue), Detail: detail,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, "", fmt.Errorf("编码 config_audit: %w", err)
	}
	f, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, "", fmt.Errorf("打开 config_audit: %w", err)
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, "", fmt.Errorf("写 config_audit: %w", err)
	}
	if closeErr != nil {
		return nil, "", fmt.Errorf("关闭 config_audit: %w", closeErr)
	}
	return accounting.WithConfigAuditID(r.Context(), auditID), auditID, nil
}

func validPositiveDecimal(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	digits, dots, nonzero := 0, 0, false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
			nonzero = nonzero || c != '0'
		case c == '.':
			dots++
		default:
			return false
		}
	}
	return digits > 0 && dots <= 1 && nonzero
}

func (s *Server) finishLedgerOperation(w http.ResponseWriter, r *http.Request, coin, field, auditID string, err error) {
	if err != nil {
		log.Printf("[admin] ledger_operation actor=%s coin=%s field=%s audit_id=%s result=error err=%q from=%s",
			s.actor, coin, field, auditID, err.Error(), r.RemoteAddr)
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"result": "error", "auditId": auditID, "error": err.Error(),
		})
		return
	}
	log.Printf("[admin] ledger_operation actor=%s coin=%s field=%s audit_id=%s result=ok from=%s",
		s.actor, coin, field, auditID, r.RemoteAddr)
	writeJSON(w, map[string]string{"result": "ok", "auditId": auditID})
}

// ---- handlers ----

func (s *Server) coin(w http.ResponseWriter, r *http.Request) (CoinControl, string, bool) {
	id := r.PathValue("id")
	p, ok := s.pools()[id]
	if !ok {
		http.Error(w, "unknown coin", http.StatusNotFound)
		return nil, id, false
	}
	return p, id, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	type coinStatus struct {
		ID              string   `json:"id"`
		ConnectedMiners int      `json:"connectedMiners"`
		BlockHeight     uint64   `json:"blockHeight"`
		PayoutsEnabled  bool     `json:"payoutsEnabled"`
		Frozen          bool     `json:"frozen"`
		NewConns        bool     `json:"newConnectionsEnabled"`
		FeePercent      float64  `json:"feePercent"`
		SoloFeePercent  *float64 `json:"soloFeePercent,omitempty"`
	}
	all := s.pools()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := struct {
		InstanceID string       `json:"instanceId"`
		Version    string       `json:"version"`
		StartedAt  string       `json:"startedAt"`
		Coins      []coinStatus `json:"coins"`
	}{InstanceID: s.instanceID, Version: s.version, StartedAt: s.started.UTC().Format(time.RFC3339)}
	for _, id := range ids {
		p := all[id]
		c := p.Cfg()
		out.Coins = append(out.Coins, coinStatus{
			ID: id, ConnectedMiners: p.Connections(), BlockHeight: p.Network().Height,
			PayoutsEnabled: c.Payout.Enabled, Frozen: p.PayoutFrozen(),
			NewConns: c.NewConnsEnabled, FeePercent: c.Payout.FeePercent,
			SoloFeePercent: c.Payout.SoloFeePercent,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleCoin(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.coin(w, r)
	if !ok {
		return
	}
	cfg := p.Cfg()
	// 节点凭据不外泄（哪怕是管理面，日志/浏览器历史都可能沾）
	for i := range cfg.Nodes {
		cfg.Nodes[i].Pass = "(redacted)"
	}
	// 未归集费（计提 − 已归集批次）：feecollect 操作前的把关数字。读失败给空串不阻断视图。
	unc, err := p.UncollectedFees(r.Context())
	if err != nil {
		unc = ""
	}
	writeJSON(w, struct {
		Config          config.CoinConfig `json:"config"`
		Frozen          bool              `json:"frozen"`
		UncollectedFees string            `json:"uncollectedFees"`
	}{cfg, p.PayoutFrozen(), unc})
}

// handlePayoutPatch 部分更新打款热参数（未给的字段不动）。
func (s *Server) handlePayoutPatch(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		FeePercent     *float64 `json:"feePercent"`
		SoloFeePercent *float64 `json:"soloFeePercent"`
		MinPayout      *string  `json:"minPayout"`
		Confirmations  *int64   `json:"confirmations"`
		Enabled        *bool    `json:"enabled"`
		FeeCollect     *struct {
			Enabled   *bool   `json:"enabled"`
			MinAmount *string `json:"minAmount"`
		} `json:"feeCollect"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.FeePercent != nil && (*body.FeePercent < 0 || *body.FeePercent > 100) {
		http.Error(w, "feePercent 须在 0~100", http.StatusBadRequest)
		return
	}
	if body.SoloFeePercent != nil && (*body.SoloFeePercent < 0 || *body.SoloFeePercent > 100) {
		http.Error(w, "soloFeePercent 须在 0~100", http.StatusBadRequest)
		return
	}
	next := p.Cfg().Payout
	if body.FeePercent != nil {
		next.FeePercent = *body.FeePercent
	}
	if body.SoloFeePercent != nil {
		next.SoloFeePercent = body.SoloFeePercent
	}
	if body.MinPayout != nil {
		next.MinPayout = *body.MinPayout
	}
	if body.Confirmations != nil {
		next.Confirmations = *body.Confirmations
	}
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}
	if body.FeeCollect != nil {
		if body.FeeCollect.Enabled != nil {
			// 没配费地址时开归集 = engine 每轮静默跳过，等于没开——直接拒绝，别让人误以为开了
			if *body.FeeCollect.Enabled && p.Cfg().FeeAddress == "" {
				http.Error(w, "feeAddress 未配置，无法启用手续费归集", http.StatusBadRequest)
				return
			}
			next.FeeCollect.Enabled = *body.FeeCollect.Enabled
		}
		if body.FeeCollect.MinAmount != nil {
			next.FeeCollect.MinAmount = *body.FeeCollect.MinAmount
		}
	}
	p.ApplyPayout(next)
	s.commit(r, "payout.update", id, body)
	writeJSON(w, p.Cfg().Payout)
}

func (s *Server) handlePayoutRun(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	if err := p.RunPayoutNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "payout.run", id, nil)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	delta, err := p.RunReconcile(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "reconcile.run", id, map[string]string{"delta": delta})
	writeJSON(w, map[string]any{"delta": delta, "frozen": p.PayoutFrozen()})
}

func (s *Server) handleUnfreeze(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	p.UnfreezePayout()
	s.commit(r, "payout.unfreeze", id, nil)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleFeeSweep(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		ColdAddress string `json:"coldAddress"`
		Amount      string `json:"amount"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.ColdAddress == "" || body.Amount == "" {
		http.Error(w, "缺 coldAddress/amount", http.StatusBadRequest)
		return
	}
	txid, err := p.FeeSweep(r.Context(), body.ColdAddress, body.Amount)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "fee.sweep", id, map[string]string{"cold": body.ColdAddress, "amount": body.Amount, "txid": txid})
	writeJSON(w, map[string]string{"txid": txid})
}

func (s *Server) handleFeeCollect(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		Amount string `json:"amount"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Amount == "" {
		http.Error(w, "缺 amount", http.StatusBadRequest)
		return
	}
	txid, err := p.FeeCollect(r.Context(), body.Amount)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "fee.collect", id, map[string]string{"amount": body.Amount, "txid": txid})
	writeJSON(w, map[string]string{"txid": txid})
}

func (s *Server) handleDebtWriteOff(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		Reason  string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Address) == "" || strings.TrimSpace(body.Reason) == "" || !validPositiveDecimal(body.Amount) {
		http.Error(w, "address/reason 必填，amount 须为大于 0 的十进制字符串", http.StatusBadRequest)
		return
	}
	ctx, auditID, err := s.prepareLedgerAudit(r, coin, "debt.writeoff", body)
	if err != nil {
		log.Printf("[admin] ledger_audit_failed actor=%s coin=%s field=debt.writeoff err=%q", s.actor, coin, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = p.WriteOffDebt(ctx, body.Address, body.Amount, body.Reason)
	s.finishLedgerOperation(w, r, coin, "debt.writeoff", auditID, err)
}

func (s *Server) handleBalanceAdjust(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		Credit  *bool  `json:"credit"`
		Reason  string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Address) == "" || strings.TrimSpace(body.Reason) == "" || body.Credit == nil || !validPositiveDecimal(body.Amount) {
		http.Error(w, "address/credit/reason 必填，amount 须为大于 0 的十进制字符串", http.StatusBadRequest)
		return
	}
	detail := struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		Credit  bool   `json:"credit"`
		Reason  string `json:"reason"`
	}{body.Address, body.Amount, *body.Credit, body.Reason}
	ctx, auditID, err := s.prepareLedgerAudit(r, coin, "balance.adjust", detail)
	if err != nil {
		log.Printf("[admin] ledger_audit_failed actor=%s coin=%s field=balance.adjust err=%q", s.actor, coin, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = p.ManualAdjust(ctx, body.Address, body.Amount, *body.Credit, body.Reason)
	s.finishLedgerOperation(w, r, coin, "balance.adjust", auditID, err)
}

func (s *Server) handleBlockOrphan(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		Hash   string `json:"hash"`
		Height int64  `json:"height"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Hash) == "" || body.Height <= 0 || strings.TrimSpace(body.Reason) == "" {
		http.Error(w, "hash/reason 必填，height 须大于 0", http.StatusBadRequest)
		return
	}
	if err := p.ForceOrphanBlock(r.Context(), body.Hash, body.Height); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "blocks/orphan", coin, body)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handlePayoutVoid(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		BatchID int64  `json:"batchId"`
		Reason  string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.BatchID <= 0 || strings.TrimSpace(body.Reason) == "" {
		http.Error(w, "batchId 须大于 0，reason 必填", http.StatusBadRequest)
		return
	}
	if err := p.VoidPayoutBatch(r.Context(), body.BatchID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "payout/void", coin, body)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleIncidentRecord(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Recipient string `json:"recipient"`
		Amount    string `json:"amount"`
		Memo      string `json:"memo"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ID) == "" || strings.TrimSpace(body.Recipient) == "" ||
		strings.TrimSpace(body.Memo) == "" || (body.Kind != "overpay" && body.Kind != "wrong_address") ||
		!validPositiveDecimal(body.Amount) {
		http.Error(w, "id/recipient/memo 必填，kind 或 amount 非法", http.StatusBadRequest)
		return
	}
	ctx, auditID, err := s.prepareLedgerAudit(r, coin, "incident.record", body)
	if err != nil {
		log.Printf("[admin] ledger_audit_failed actor=%s coin=%s field=incident.record err=%q", s.actor, coin, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = p.RecordIncident(ctx, body.ID, body.Kind, body.Recipient, body.Amount, body.Memo)
	s.finishLedgerOperation(w, r, coin, "incident.record", auditID, err)
}

func (s *Server) handleIncidentResolve(w http.ResponseWriter, r *http.Request) {
	p, coin, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
		Amount  string `json:"amount"`
		Memo    string `json:"memo"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ID) == "" || strings.TrimSpace(body.Memo) == "" ||
		(body.Outcome != "recovered" && body.Outcome != "writeoff") || !validPositiveDecimal(body.Amount) {
		http.Error(w, "id/memo 必填，outcome 或 amount 非法", http.StatusBadRequest)
		return
	}
	ctx, auditID, err := s.prepareLedgerAudit(r, coin, "incident.resolve", body)
	if err != nil {
		log.Printf("[admin] ledger_audit_failed actor=%s coin=%s field=incident.resolve err=%q", s.actor, coin, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = p.ResolveIncident(ctx, body.ID, body.Outcome, body.Amount, body.Memo)
	s.finishLedgerOperation(w, r, coin, "incident.resolve", auditID, err)
}

func (s *Server) handlePortAdd(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	var pc config.PortConfig
	if !readJSON(w, r, &pc) {
		return
	}
	if pc.Port <= 0 || pc.Port > 65535 {
		http.Error(w, "非法端口", http.StatusBadRequest)
		return
	}
	if pc.Dialect == "" {
		pc.Dialect = "stratum1"
	}
	if err := p.AddPort(pc); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.commit(r, "port.add", id, pc)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) portParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	var port int
	if _, err := fmt.Sscanf(r.PathValue("port"), "%d", &port); err != nil || port <= 0 {
		http.Error(w, "非法端口", http.StatusBadRequest)
		return 0, false
	}
	return port, true
}

func (s *Server) handlePortPatch(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	port, ok := s.portParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !readJSON(w, r, &body) || body.Enabled == nil {
		if body.Enabled == nil {
			http.Error(w, "缺 enabled", http.StatusBadRequest)
		}
		return
	}
	if err := p.SetPortEnabled(port, *body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.commit(r, "port.toggle", id, map[string]any{"port": port, "enabled": *body.Enabled})
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handlePortDelete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	port, ok := s.portParam(w, r)
	if !ok {
		return
	}
	if err := p.RemovePort(port); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.commit(r, "port.remove", id, map[string]int{"port": port})
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleNewConns(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !readJSON(w, r, &body) || body.Enabled == nil {
		if body.Enabled == nil {
			http.Error(w, "缺 enabled", http.StatusBadRequest)
		}
		return
	}
	p.SetNewConns(*body.Enabled)
	s.commit(r, "coin.newconns", id, map[string]bool{"enabled": *body.Enabled})
	writeJSON(w, map[string]string{"result": "ok"})
}

// ---- ban 管理 ----

func (s *Server) handleBansList(w http.ResponseWriter, _ *http.Request) {
	if s.bans == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.bans.Entries())
}

func (s *Server) handleBanAdd(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		http.Error(w, "ban 名单未启用", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Target     string `json:"target"`
		Reason     string `json:"reason"`
		TTLSeconds int64  `json:"ttlSeconds"` // 0 = 永久
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.bans.Ban(body.Target, body.Reason, time.Duration(body.TTLSeconds)*time.Second); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.commit(r, "ban.add", "", body)
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleBanDelete(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		http.Error(w, "ban 名单未启用", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.bans.Unban(body.Target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.commit(r, "ban.remove", "", body)
	writeJSON(w, map[string]string{"result": "ok"})
}

// ---- 矿工设置（面板可见 + 可重置，R5）----

func (s *Server) handleMinerSettingsGet(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		http.Error(w, "矿工设置未启用", http.StatusServiceUnavailable)
		return
	}
	_, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	rec, found := s.settings.Get(id, r.PathValue("addr"))
	if !found {
		http.Error(w, "无记录", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"minPayout":   rec.MinPayout,
		"hasPassword": rec.PasswordHash != "", // hash 本身不外泄
		"updatedAt":   rec.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// handleMinerSettingsPut 管理后台改/重置矿工设置（bypass 密码；矿工自助走公共面板凭密码）。
func (s *Server) handleMinerSettingsPut(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		http.Error(w, "矿工设置未启用", http.StatusServiceUnavailable)
		return
	}
	_, id, ok := s.coin(w, r)
	if !ok {
		return
	}
	addr := r.PathValue("addr")
	var body struct {
		MinPayout float64 `json:"minPayout"` // 0 = 清除覆盖（回池默认）
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.settings.SetMinPayout(id, addr, "", body.MinPayout, true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.commit(r, "minersettings.set", id, map[string]any{"addr": addr, "minPayout": body.MinPayout})
	writeJSON(w, map[string]string{"result": "ok"})
}
