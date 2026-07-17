package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/banlist"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/minersettings"
)

// fakeCoin 记录调用的 CoinControl 假实现。
type fakeCoin struct {
	cfg                       config.CoinConfig
	frozen                    bool
	unfroze                   bool
	ranPay                    bool
	delta                     string
	sweepTx                   string
	collects                  []string
	ledger                    accounting.Ledger
	ledgerCalls               []string
	auditPath                 string
	auditBeforeAllLedgerCalls bool
}

func (f *fakeCoin) Cfg() config.CoinConfig { return f.cfg }
func (f *fakeCoin) ApplyPayout(p config.PayoutConfig) {
	f.cfg.Payout = p
}
func (f *fakeCoin) AddPort(pc config.PortConfig) error {
	for _, p := range f.cfg.Ports {
		if p.Port == pc.Port {
			return contextErr("端口已存在")
		}
	}
	f.cfg.Ports = append(f.cfg.Ports, pc)
	return nil
}
func (f *fakeCoin) RemovePort(port int) error {
	for i, p := range f.cfg.Ports {
		if p.Port == port {
			f.cfg.Ports = append(f.cfg.Ports[:i], f.cfg.Ports[i+1:]...)
			return nil
		}
	}
	return contextErr("不存在")
}
func (f *fakeCoin) SetPortEnabled(port int, enabled bool) error {
	for i := range f.cfg.Ports {
		if f.cfg.Ports[i].Port == port {
			f.cfg.Ports[i].Enabled = enabled
			return nil
		}
	}
	return contextErr("不存在")
}
func (f *fakeCoin) SetNewConns(v bool)                 { f.cfg.NewConnsEnabled = v }
func (f *fakeCoin) RunPayoutNow(context.Context) error { f.ranPay = true; return nil }
func (f *fakeCoin) RunReconcile(context.Context) (string, error) {
	return f.delta, nil
}
func (f *fakeCoin) PayoutFrozen() bool { return f.frozen }
func (f *fakeCoin) UnfreezePayout()    { f.frozen = false; f.unfroze = true }
func (f *fakeCoin) FeeSweep(_ context.Context, cold, amount string) (string, error) {
	f.sweepTx = cold + ":" + amount
	return "sweeptx", nil
}
func (f *fakeCoin) FeeCollect(_ context.Context, amount string) (string, error) {
	f.collects = append(f.collects, amount)
	return "collecttx", nil
}
func (f *fakeCoin) noteLedgerCall(name string) {
	f.ledgerCalls = append(f.ledgerCalls, name)
	b, err := os.ReadFile(f.auditPath)
	if err != nil || len(strings.Split(strings.TrimSpace(string(b)), "\n")) != len(f.ledgerCalls) {
		f.auditBeforeAllLedgerCalls = false
	}
}
func (f *fakeCoin) WriteOffDebt(ctx context.Context, address, amount, reason string) error {
	f.noteLedgerCall("debt.writeoff")
	return f.ledger.WriteOffDebt(ctx, f.cfg.ID, address, amount, reason)
}
func (f *fakeCoin) ManualAdjust(ctx context.Context, address, amount string, credit bool, reason string) error {
	f.noteLedgerCall("balance.adjust")
	return f.ledger.ManualAdjust(ctx, f.cfg.ID, address, amount, credit, reason)
}
func (f *fakeCoin) RecordIncident(ctx context.Context, id, kind, recipient, amount, memo string) error {
	f.noteLedgerCall("incident.record")
	return f.ledger.RecordIncident(ctx, f.cfg.ID, id, kind, recipient, amount, memo)
}
func (f *fakeCoin) ResolveIncident(ctx context.Context, id, outcome, amount, memo string) error {
	f.noteLedgerCall("incident.resolve")
	return f.ledger.ResolveIncident(ctx, f.cfg.ID, id, outcome, amount, memo)
}
func (f *fakeCoin) UncollectedFees(context.Context) (string, error) { return "1.50000000", nil }
func (f *fakeCoin) Connections() int                                { return 3 }
func (f *fakeCoin) Network() core.NetworkSnapshot                   { return core.NetworkSnapshot{Height: 42} }

type strErr string

func (e strErr) Error() string  { return string(e) }
func contextErr(s string) error { return strErr(s) }

func newTestAdmin(t *testing.T) (*Server, *fakeCoin, string) {
	t.Helper()
	dir := t.TempDir()
	fc := &fakeCoin{
		cfg: config.CoinConfig{
			ID: "tst", Symbol: "TST",
			Nodes: []config.NodeEndpoint{{URL: "http://n", Pass: "secretpass"}},
			Ports: []config.PortConfig{{Port: 3333, Enabled: true, Dialect: "stratum1"}},
			Payout: config.PayoutConfig{Enabled: true, FeePercent: 10, MinPayout: "0.01",
				Confirmations: 100},
			NewConnsEnabled: true,
		},
		delta: "0.00000000",
	}
	ledger := accounting.NewMemLedger(8, 2)
	ctx := context.Background()
	_ = ledger.RecordShare(ctx, core.Share{Coin: "tst", Address: "debtor", At: time.Now()}, 1)
	b := core.FoundBlock{Coin: "tst", Hash: "admin-debt-source", Finder: "debtor",
		Reward: "10.00000000", NetDiff: 1, Status: core.BlockPending, FoundAt: time.Now()}
	_ = ledger.RecordBlock(ctx, b, "raw")
	_ = ledger.ConfirmBlock(ctx, b, 0)
	_ = ledger.DeductForPayout(ctx, "tst", map[string]string{"debtor": "8.00000000"}, 991)
	_ = ledger.OrphanBlock(ctx, b)
	fc.ledger = ledger
	fc.auditPath = filepath.Join(dir, "audit.jsonl")
	fc.auditBeforeAllLedgerCalls = true
	bans, _ := banlist.New(filepath.Join(dir, "bans.json"))
	settings, _ := minersettings.New(filepath.Join(dir, "settings.json"), []byte("salt"))
	s := New("test", "pool1", "tok123",
		func() map[string]CoinControl { return map[string]CoinControl{"tst": fc} },
		bans, settings, filepath.Join(dir, "config.state.json"), filepath.Join(dir, "audit.jsonl"))
	return s, fc, dir
}

func req(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAuthRequired(t *testing.T) {
	s, _, _ := newTestAdmin(t)
	h := s.Handler()
	if rec := req(t, h, "GET", "/admin/v1/status", "", ""); rec.Code != 401 {
		t.Fatalf("无 token 应 401: %d", rec.Code)
	}
	if rec := req(t, h, "GET", "/admin/v1/status", "wrong", ""); rec.Code != 401 {
		t.Fatalf("错 token 应 401: %d", rec.Code)
	}
	if rec := req(t, h, "GET", "/admin/v1/status", "tok123", ""); rec.Code != 200 {
		t.Fatalf("对 token 应 200: %d", rec.Code)
	}
	// token 未配置 = 全拒
	s2, _, _ := newTestAdmin(t)
	s2.token = ""
	if rec := req(t, s2.Handler(), "GET", "/admin/v1/status", "", ""); rec.Code != 403 {
		t.Fatalf("无 token 配置应 403: %d", rec.Code)
	}
}

func TestStatusAndCoinRedaction(t *testing.T) {
	s, _, _ := newTestAdmin(t)
	h := s.Handler()
	rec := req(t, h, "GET", "/admin/v1/status", "tok123", "")
	if !strings.Contains(rec.Body.String(), `"blockHeight":42`) {
		t.Fatalf("status 缺高度: %s", rec.Body.String())
	}
	rec = req(t, h, "GET", "/admin/v1/coins/tst", "tok123", "")
	if strings.Contains(rec.Body.String(), "secretpass") {
		t.Fatal("节点密码泄漏")
	}
	if !strings.Contains(rec.Body.String(), "(redacted)") {
		t.Fatal("应有脱敏标记")
	}
	if rec := req(t, h, "GET", "/admin/v1/coins/nope", "tok123", ""); rec.Code != 404 {
		t.Fatalf("未知币应 404: %d", rec.Code)
	}
}

func TestPayoutPatchPartialAndAudit(t *testing.T) {
	s, fc, dir := newTestAdmin(t)
	h := s.Handler()
	// 只改费率：其余不动
	rec := req(t, h, "PATCH", "/admin/v1/coins/tst/payout", "tok123", `{"feePercent":5}`)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if fc.cfg.Payout.FeePercent != 5 || fc.cfg.Payout.MinPayout != "0.01" || fc.cfg.Payout.Confirmations != 100 {
		t.Fatalf("部分更新错: %+v", fc.cfg.Payout)
	}
	// 非法费率拒绝
	if rec := req(t, h, "PATCH", "/admin/v1/coins/tst/payout", "tok123", `{"feePercent":150}`); rec.Code != 400 {
		t.Fatalf("非法费率应 400: %d", rec.Code)
	}
	// feeCollect 热更（M5 生产教训：config.json 改了会被热状态 overlay 盖掉，
	// 唯一正确开法=走 PATCH 落热状态）——没配 feeAddress 时开归集必须拒绝
	if rec := req(t, h, "PATCH", "/admin/v1/coins/tst/payout", "tok123",
		`{"feeCollect":{"enabled":true,"minAmount":"0.05"}}`); rec.Code != 400 {
		t.Fatalf("无 feeAddress 开归集应 400: %d %s", rec.Code, rec.Body.String())
	}
	fc.cfg.FeeAddress = "feeAddr1"
	rec = req(t, h, "PATCH", "/admin/v1/coins/tst/payout", "tok123",
		`{"feeCollect":{"enabled":true,"minAmount":"0.05"}}`)
	if rec.Code != 200 {
		t.Fatalf("feeCollect 热更失败: %d %s", rec.Code, rec.Body.String())
	}
	if !fc.cfg.Payout.FeeCollect.Enabled || fc.cfg.Payout.FeeCollect.MinAmount != "0.05" {
		t.Fatalf("feeCollect 未生效: %+v", fc.cfg.Payout.FeeCollect)
	}
	if fc.cfg.Payout.FeePercent != 5 {
		t.Fatalf("feeCollect 热更不应动其他字段: %+v", fc.cfg.Payout)
	}
	// 审计已落盘
	b, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil || !strings.Contains(string(b), "payout.update") {
		t.Fatalf("审计缺失: %v %s", err, b)
	}
	// 热状态已落盘且含新费率
	sb, err := os.ReadFile(filepath.Join(dir, "config.state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st config.HotState
	if err := json.Unmarshal(sb, &st); err != nil {
		t.Fatal(err)
	}
	if st.Coins["tst"].Payout.FeePercent != 5 {
		t.Fatalf("热状态未反映新费率: %+v", st.Coins["tst"].Payout)
	}
}

func TestPortLifecycle(t *testing.T) {
	s, fc, _ := newTestAdmin(t)
	h := s.Handler()
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/ports", "tok123",
		`{"port":4444,"mode":"solo","enabled":true}`); rec.Code != 200 {
		t.Fatalf("加端口失败: %d %s", rec.Code, rec.Body.String())
	}
	if len(fc.cfg.Ports) != 2 {
		t.Fatalf("端口数=%d", len(fc.cfg.Ports))
	}
	// 重复加 → 409
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/ports", "tok123",
		`{"port":4444}`); rec.Code != 409 {
		t.Fatalf("重复端口应 409: %d", rec.Code)
	}
	// 停用
	if rec := req(t, h, "PATCH", "/admin/v1/coins/tst/ports/4444", "tok123",
		`{"enabled":false}`); rec.Code != 200 {
		t.Fatalf("停用失败: %d", rec.Code)
	}
	if fc.cfg.Ports[1].Enabled {
		t.Fatal("端口未停用")
	}
	// 删除
	if rec := req(t, h, "DELETE", "/admin/v1/coins/tst/ports/4444", "tok123", ""); rec.Code != 200 {
		t.Fatalf("删端口失败: %d", rec.Code)
	}
	if len(fc.cfg.Ports) != 1 {
		t.Fatalf("删除后端口数=%d", len(fc.cfg.Ports))
	}
}

func TestOpsEndpoints(t *testing.T) {
	s, fc, _ := newTestAdmin(t)
	h := s.Handler()
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/payout/run", "tok123", ""); rec.Code != 200 || !fc.ranPay {
		t.Fatalf("payout/run: %d ran=%v", rec.Code, fc.ranPay)
	}
	rec := req(t, h, "POST", "/admin/v1/coins/tst/reconcile", "tok123", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "0.00000000") {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body.String())
	}
	fc.frozen = true
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/unfreeze", "tok123", ""); rec.Code != 200 || fc.frozen {
		t.Fatalf("unfreeze: %d frozen=%v", rec.Code, fc.frozen)
	}
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/feesweep", "tok123",
		`{"coldAddress":"cold1","amount":"2.5"}`); rec.Code != 200 || fc.sweepTx != "cold1:2.5" {
		t.Fatalf("feesweep: %d %q", rec.Code, fc.sweepTx)
	}
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/feecollect", "tok123",
		`{"amount":"1.0"}`); rec.Code != 200 || len(fc.collects) != 1 {
		t.Fatalf("feecollect: %d %v", rec.Code, fc.collects)
	}
	if rec := req(t, h, "PATCH", "/admin/v1/coins/tst/newconns", "tok123",
		`{"enabled":false}`); rec.Code != 200 || fc.cfg.NewConnsEnabled {
		t.Fatalf("newconns: %d", rec.Code)
	}
}

func TestLedgerOperationEndpointsAuditAndEndToEnd(t *testing.T) {
	s, fc, dir := newTestAdmin(t)
	h := s.Handler()
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/debt/writeoff", "wrong",
		`{"address":"debtor","amount":"1.00000000","reason":"坏账"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("新端点必须鉴权: %d", rec.Code)
	}
	if rec := req(t, h, "POST", "/admin/v1/coins/tst/debt/writeoff", "tok123",
		`{"address":"debtor","amount":"1e2","reason":"坏账"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("浮点/指数金额必须 400: %d", rec.Code)
	}
	if len(fc.ledgerCalls) != 0 {
		t.Fatalf("非法参数不得调用 Ledger: %v", fc.ledgerCalls)
	}

	requests := []struct {
		path, body string
	}{
		{"/admin/v1/coins/tst/debt/writeoff", `{"address":"debtor","amount":"2.00000000","reason":"确认无法追回"}`},
		{"/admin/v1/coins/tst/balance/adjust", `{"address":"admin-miner","amount":"3.00000000","credit":true,"reason":"人工补发"}`},
		{"/admin/v1/coins/tst/balance/adjust", `{"address":"admin-miner","amount":"1.00000000","credit":false,"reason":"人工冲回"}`},
		{"/admin/v1/coins/tst/incident/record", `{"id":"admin-incident-1","kind":"overpay","recipient":"recipient-X","amount":"5.00000000","memo":"重复打款"}`},
		{"/admin/v1/coins/tst/incident/resolve", `{"id":"admin-incident-1","outcome":"recovered","amount":"2.00000000","memo":"追回部分"}`},
	}
	for _, item := range requests {
		rec := req(t, h, "POST", item.path, "tok123", item.body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auditId"`) {
			t.Fatalf("%s: code=%d body=%s", item.path, rec.Code, rec.Body.String())
		}
	}
	if len(fc.ledgerCalls) != len(requests) {
		t.Fatalf("端到端 Ledger 调用=%v", fc.ledgerCalls)
	}
	if !fc.auditBeforeAllLedgerCalls {
		t.Fatal("每笔账本调用发生前必须已写入对应 config_audit")
	}
	snap, err := fc.ledger.Snapshot(context.Background(), "tst")
	if err != nil {
		t.Fatal(err)
	}
	if snap.DebtsNet != "6.00000000" || snap.Balances["admin-miner"] != "2.00000000" {
		t.Fatalf("端到端投影错误: debts=%s balance=%s", snap.DebtsNet, snap.Balances["admin-miner"])
	}
	if delta, err := fc.ledger.Reconcile(context.Background(), "tst"); err != nil || delta != "0.00000000" {
		t.Fatalf("端点操作后守恒: delta=%s err=%v", delta, err)
	}
	if mismatch, checked, err := fc.ledger.JournalShadowAudit(context.Background(), "tst"); err != nil || !checked || len(mismatch) != 0 {
		t.Fatalf("端点操作后 J3: checked=%t err=%v mismatch=%+v", checked, err, mismatch)
	}

	b, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != len(requests) {
		t.Fatalf("每次有效调用应先落一条 config_audit: got=%d want=%d\n%s", len(lines), len(requests), b)
	}
	seen := map[string]struct{}{}
	previousID := ""
	for _, line := range lines {
		var rec auditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.ID == "" || rec.Actor != "admin-token" || rec.Coin != "tst" || rec.Field == "" ||
			!strings.Contains(rec.NewValue, rec.ID) {
			t.Fatalf("config_audit 字段不完整: %+v", rec)
		}
		if _, duplicate := seen[rec.ID]; duplicate {
			t.Fatalf("config_audit id 重复: %s", rec.ID)
		}
		if previousID != "" && rec.ID <= previousID {
			t.Fatalf("config_audit id 非单调递增: previous=%s current=%s", previousID, rec.ID)
		}
		seen[rec.ID] = struct{}{}
		previousID = rec.ID
	}
}

func TestLedgerOperationEndpointValidation(t *testing.T) {
	s, fc, dir := newTestAdmin(t)
	h := s.Handler()
	cases := []struct {
		path, body string
	}{
		{"/admin/v1/coins/tst/debt/writeoff", `{"address":"debtor","amount":"1.00000000","reason":" "}`},
		{"/admin/v1/coins/tst/balance/adjust", `{"address":"A","amount":"1.00000000","reason":"缺 credit"}`},
		{"/admin/v1/coins/tst/incident/record", `{"id":"x","kind":"other","recipient":"R","amount":"1.00000000","memo":"x"}`},
		{"/admin/v1/coins/tst/incident/resolve", `{"id":"x","outcome":"writeoff","amount":"0","memo":"x"}`},
	}
	for _, tc := range cases {
		if rec := req(t, h, "POST", tc.path, "tok123", tc.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，得到 %d", tc.path, rec.Code)
		}
	}
	if len(fc.ledgerCalls) != 0 {
		t.Fatalf("参数校验失败不得调用 Ledger: %v", fc.ledgerCalls)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "audit.jsonl")); err == nil && len(b) != 0 {
		t.Fatalf("未进入业务的非法请求不应产生操作审计: %s", b)
	}
}

func TestBanEndpoints(t *testing.T) {
	s, _, _ := newTestAdmin(t)
	h := s.Handler()
	if rec := req(t, h, "POST", "/admin/v1/bans", "tok123",
		`{"target":"10.0.0.0/24","reason":"abuse","ttlSeconds":600}`); rec.Code != 200 {
		t.Fatalf("加 ban: %d %s", rec.Code, rec.Body.String())
	}
	rec := req(t, h, "GET", "/admin/v1/bans", "tok123", "")
	if !strings.Contains(rec.Body.String(), "10.0.0.0/24") {
		t.Fatalf("ban 列表缺条目: %s", rec.Body.String())
	}
	if banned, _ := s.bans.Banned("10.0.0.7"); !banned {
		t.Fatal("ban 未生效")
	}
	if rec := req(t, h, "POST", "/admin/v1/bans/delete", "tok123",
		`{"target":"10.0.0.0/24"}`); rec.Code != 200 {
		t.Fatalf("解 ban: %d", rec.Code)
	}
	if banned, _ := s.bans.Banned("10.0.0.7"); banned {
		t.Fatal("解 ban 未生效")
	}
	if rec := req(t, h, "POST", "/admin/v1/bans", "tok123",
		`{"target":"not-an-ip"}`); rec.Code != 400 {
		t.Fatalf("非法 target 应 400: %d", rec.Code)
	}
}

func TestMinerSettingsAdmin(t *testing.T) {
	s, _, _ := newTestAdmin(t)
	h := s.Handler()
	// 先模拟矿工带密码设了 mp=50
	_, _ = s.settings.ApplyPassword("tst", "addrX", minersettings.ParsePassword("pwd,mp=50"), 0.01)

	rec := req(t, h, "GET", "/admin/v1/coins/tst/miners/addrX/settings", "tok123", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"minPayout":50`) {
		t.Fatalf("查设置: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "passwordHash") {
		t.Fatal("密码 hash 不应外泄")
	}
	// 管理后台 bypass 重置（矿工手滑设太高的解法，R5）
	if rec := req(t, h, "PUT", "/admin/v1/coins/tst/miners/addrX/settings", "tok123",
		`{"minPayout":0}`); rec.Code != 200 {
		t.Fatalf("重置: %d %s", rec.Code, rec.Body.String())
	}
	if mp := s.settings.MinPayouts("tst"); len(mp) != 0 {
		t.Fatalf("重置后应无覆盖: %v", mp)
	}
}
