//go:build noid

package noidjob

import (
	"context"
	"encoding/hex"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/noidrpc"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/noidp2b"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// mockNode 打桩 nodeIface：GetTemplate 返回固定模板，SubmitNonce/ConsumeTemplate 计数。
// ★HandleSubmit 的核心安全断言 = SubmitNonce 只在爆块时被调用一次，普通 share 绝不调用。
type mockNode struct {
	tpl          *noidrpc.NoidTemplate
	mu           sync.Mutex
	submitCalls  int
	consumeCalls int
}

func (m *mockNode) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	return &adapter.BlockTemplate{
		Height:        m.tpl.Height,
		NetworkTarget: hex.EncodeToString(m.tpl.NetworkTarget[:]),
		Raw:           m.tpl,
		FetchedAt:     time.Now(),
	}, nil
}

func (m *mockNode) SubmitNonce(_ context.Context, templateID, _ string) (string, error) {
	m.mu.Lock()
	m.submitCalls++
	m.mu.Unlock()
	return "nodehash:" + templateID, nil
}

func (m *mockNode) ConsumeTemplate(string) {
	m.mu.Lock()
	m.consumeCalls++
	m.mu.Unlock()
}

func (m *mockNode) submits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.submitCalls
}

const testTemplateID = "tpl-test"

var (
	testFields [256]byte // 全零 pow_fields（field 10 由 nonce 覆写，内容无关）
	testNonce  = []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
)

// le32 big.Int → 32B 小端（与 noidp2b.Check 的 target 口径一致）。
func le32(v *big.Int) []byte {
	b := v.Bytes() // 大端
	out := make([]byte, 32)
	for i := 0; i < len(b) && i < 32; i++ {
		out[i] = b[len(b)-1-i] // BE → LE
	}
	return out
}

// bigLE 32B 小端 → big.Int 数值。
func bigLE(d []byte) *big.Int {
	be := make([]byte, len(d))
	for i := range d {
		be[i] = d[len(d)-1-i]
	}
	return new(big.Int).SetBytes(be)
}

// setup 建一个已装载固定模板的 Manager（模板全网 target = netVal 对应的 LE 字节）。
func setup(t *testing.T, netVal *big.Int) (*Manager, *mockNode) {
	t.Helper()
	tpl := &noidrpc.NoidTemplate{
		TemplateID:      testTemplateID,
		NonceFieldIndex: 10,
		Height:          100,
		ExpiresAt:       time.Now().Add(30 * time.Second),
	}
	tpl.PowFields = testFields
	copy(tpl.NetworkTarget[:], le32(netVal))
	mock := &mockNode{tpl: tpl}
	m := New("noid", mock)
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return m, mock
}

// judgeOK 恒接受、按实际难度计权（把 vardiff 从等式里摘掉，让门只由 noidp2b.Check 定）。
func judgeOK(d float64) (float64, bool) { return d, true }

// subFor 构造一条 submit（固定 nonce + 指定 share target）。
func subFor(shareTargetVal *big.Int) stratum.NoidSubmission {
	return stratum.NoidSubmission{
		Address:     "o1test",
		Worker:      "w0",
		TemplateID:  testTemplateID,
		NonceLE:     testNonce,
		ShareTarget: le32(shareTargetVal),
		Judge:       judgeOK,
	}
}

// digestVal 池端重算 digest 的 LE 数值（构造 target 用同一口径）。
func digestVal(t *testing.T) *big.Int {
	t.Helper()
	d := noidp2b.Digest(testFields[:], testNonce)
	return bigLE(d[:])
}

// 案例①：digest 落在 (network, share] 之间的合法 share（严格 <：share 侧满足、network 侧不满足）
// → OutcomeAccepted（普通有效 share）且★绝不调用 SubmitNonce（不烧模板槽）。
func TestHandleSubmit_NormalShare_NoSubmit(t *testing.T) {
	dv := digestVal(t)
	one := big.NewInt(1)
	// network = digest（digest<network 为假 → 非爆块）；share = digest+1（digest<share 为真 → 有效）
	m, mock := setup(t, dv)
	res := m.HandleSubmit(context.Background(), subFor(new(big.Int).Add(dv, one)))
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("outcome = %v, 期望 Accepted（普通 share）", res.Outcome)
	}
	if n := mock.submits(); n != 0 {
		t.Fatalf("普通 share 竟调用 SubmitNonce %d 次（必须 0，否则烧模板槽）", n)
	}
}

// 案例②：digest < network → OutcomeBlock 且 SubmitNonce 恰好被调用一次；BlockHash = hex(digest)。
func TestHandleSubmit_Block_SubmitsOnce(t *testing.T) {
	dv := digestVal(t)
	one, two := big.NewInt(1), big.NewInt(2)
	// network = digest+1（digest<network 为真 → 爆块）；share = digest+2（更宽松 → 也满足）
	m, mock := setup(t, new(big.Int).Add(dv, one))
	res := m.HandleSubmit(context.Background(), subFor(new(big.Int).Add(dv, two)))
	if res.Outcome != core.OutcomeBlock {
		t.Fatalf("outcome = %v, 期望 Block", res.Outcome)
	}
	if n := mock.submits(); n != 1 {
		t.Fatalf("爆块 SubmitNonce 调用 %d 次，期望恰好 1", n)
	}
	d := noidp2b.Digest(testFields[:], testNonce)
	if res.BlockHash != hex.EncodeToString(d[:]) {
		t.Fatalf("BlockHash = %s, 期望 hex(digest) = %s", res.BlockHash, hex.EncodeToString(d[:]))
	}
	if mock.consumeCalls != 1 {
		t.Fatalf("ConsumeTemplate 调用 %d 次，期望 1", mock.consumeCalls)
	}
	// 爆块后 m.cur 已清（停发已消费模板）。
	if _, _, ok := m.Snapshot(); ok {
		t.Fatalf("爆块后 Snapshot 仍 ok（m.cur 应已清空）")
	}
}

// 案例③：digest > share（digest<share 为假）→ OutcomeLowDiff（不满足宽松 share target）。
func TestHandleSubmit_LowDiff(t *testing.T) {
	dv := digestVal(t)
	one := big.NewInt(1)
	m, mock := setup(t, dv)
	res := m.HandleSubmit(context.Background(), subFor(new(big.Int).Sub(dv, one))) // share = digest-1
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("outcome = %v, 期望 LowDiff", res.Outcome)
	}
	if n := mock.submits(); n != 0 {
		t.Fatalf("LowDiff 竟调用 SubmitNonce %d 次", n)
	}
}

// 案例④：同 (template_id, nonce) 第二次提交 → OutcomeDup（全池维度去重）。
func TestHandleSubmit_Dup(t *testing.T) {
	dv := digestVal(t)
	one := big.NewInt(1)
	m, mock := setup(t, dv) // network = digest（首发是普通 share，不清 m.cur）
	first := m.HandleSubmit(context.Background(), subFor(new(big.Int).Add(dv, one)))
	if first.Outcome != core.OutcomeAccepted {
		t.Fatalf("首发 outcome = %v, 期望 Accepted", first.Outcome)
	}
	second := m.HandleSubmit(context.Background(), subFor(new(big.Int).Add(dv, one)))
	if second.Outcome != core.OutcomeDup {
		t.Fatalf("重复提交 outcome = %v, 期望 Dup", second.Outcome)
	}
	if n := mock.submits(); n != 0 {
		t.Fatalf("dup 路径竟调用 SubmitNonce %d 次", n)
	}
}

// 案例⑤：严格 < 边界——share target == digest → 不满足（等于拒绝，le256_lt 语义）→ LowDiff。
func TestHandleSubmit_StrictBoundary(t *testing.T) {
	dv := digestVal(t)
	m, _ := setup(t, dv)
	// share target 恰等于 digest：digest < share 为假 → LowDiff（若误用 <= 会错判为有效 share）。
	res := m.HandleSubmit(context.Background(), subFor(new(big.Int).Set(dv)))
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("share==digest 边界 outcome = %v, 期望 LowDiff（严格 <，等于拒绝）", res.Outcome)
	}
}

// 附加：template_id 不匹配（NOID 单飞行槽，别的模板无法出块）→ OutcomeStale（非 violent）。
func TestHandleSubmit_StaleTemplate(t *testing.T) {
	dv := digestVal(t)
	one := big.NewInt(1)
	m, mock := setup(t, dv)
	sub := subFor(new(big.Int).Add(dv, one))
	sub.TemplateID = "some-old-template"
	res := m.HandleSubmit(context.Background(), sub)
	if res.Outcome != core.OutcomeStale {
		t.Fatalf("旧模板 outcome = %v, 期望 Stale", res.Outcome)
	}
	if n := mock.submits(); n != 0 {
		t.Fatalf("stale 竟调用 SubmitNonce %d 次", n)
	}
}
