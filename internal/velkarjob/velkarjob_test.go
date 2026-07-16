package velkarjob

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc"
	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc/protowire"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// KAT（velkarhash SelfTest 同源）：这组 prePow/target/ts/nonce 出确定 pow=000382fb…（> target，非块）。
const (
	katPrePowLE  = "f92b22c908fe912ef58501e2baa9253373d5d7f641232e8d4b386f61f4156a19"
	katTargetBE  = "00003d647c000000000000000000000000000000000000000000000000000000"
	katTimestamp = uint64(1781329471115)
	katNonce     = uint64(0x3a333445c075fa3d)
)

type mockNode struct {
	tpl       *adapter.BlockTemplate
	submitted []uint64
}

func (m *mockNode) GetTemplate(context.Context) (*adapter.BlockTemplate, error) { return m.tpl, nil }
func (m *mockNode) SubmitSolved(_ context.Context, _ *velkarrpc.VelkarWork, nonce uint64) (string, error) {
	m.submitted = append(m.submitted, nonce)
	return "fakehash", nil
}

// katWork 构造合成 work：targetBEHex 决定 block target（也进 CalculatePow 的 stage4）。
func katWork(targetBEHex string) *velkarrpc.VelkarWork {
	prePow, _ := hex.DecodeString(katPrePowLE)
	targetBE, _ := new(big.Int).SetString(targetBEHex, 16)
	full := make([]byte, 32)
	be := targetBE.Bytes()
	copy(full[32-len(be):], be)
	le := make([]byte, 32)
	for i := 0; i < 32; i++ {
		le[i] = full[31-i]
	}
	return &velkarrpc.VelkarWork{
		PrePowHash:    prePow,
		Timestamp:     katTimestamp,
		BlockTargetLE: le,
		BlockTargetBE: targetBE,
		Height:        1,
		Block:         &protowire.RpcBlock{Header: &protowire.RpcBlockHeader{Version: 1}},
		JobKey:        hex.EncodeToString(prePow),
	}
}

func newMgrWithJob(t *testing.T, work *velkarrpc.VelkarWork) (*Manager, *mockNode, uint64) {
	t.Helper()
	node := &mockNode{tpl: &adapter.BlockTemplate{Height: 1, CoinbaseValue: "2.00000000", Raw: work}}
	m := New("velkartest", "velkarhash", node)
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	j, ok := m.CurrentJob()
	if !ok {
		t.Fatalf("CurrentJob 未就绪")
	}
	return m, node, j.JobID
}

func acceptAll(d float64) (float64, bool) { return d, true }
func rejectAll(float64) (float64, bool)   { return 0, false }

// 份额被接受（pow=000382fb > target=00003d64，非块；Judge 接受 → OutcomeAccepted）。
func TestHandleSubmit_Accepted(t *testing.T) {
	var accepted *core.Share
	m, _, jobID := newMgrWithJob(t, katWork(katTargetBE))
	m.SetCallbacks(func() {}, nil, func(_ context.Context, s core.Share) { accepted = &s })

	res := m.HandleSubmit(context.Background(), stratum.KaspaSubmission{
		JobID: jobID, Nonce: katNonce, Address: "velkartest:qz", Judge: acceptAll,
	})
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("期望 Accepted, got %v", res.Outcome)
	}
	if accepted == nil || accepted.Address != "velkartest:qz" {
		t.Fatalf("onShare 未回调或地址错: %+v", accepted)
	}
}

// Judge 拒绝 → OutcomeLowDiff（且不回调 onShare）。
func TestHandleSubmit_LowDiff(t *testing.T) {
	called := false
	m, _, jobID := newMgrWithJob(t, katWork(katTargetBE))
	m.SetCallbacks(func() {}, nil, func(context.Context, core.Share) { called = true })

	res := m.HandleSubmit(context.Background(), stratum.KaspaSubmission{JobID: jobID, Nonce: katNonce, Judge: rejectAll})
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("期望 LowDiff, got %v", res.Outcome)
	}
	if called {
		t.Fatalf("LowDiff 不应回调 onShare")
	}
}

// 未知 jobID → OutcomeStale。
func TestHandleSubmit_Stale(t *testing.T) {
	m, _, jobID := newMgrWithJob(t, katWork(katTargetBE))
	res := m.HandleSubmit(context.Background(), stratum.KaspaSubmission{JobID: jobID + 999, Nonce: katNonce, Judge: acceptAll})
	if res.Outcome != core.OutcomeStale {
		t.Fatalf("期望 Stale, got %v", res.Outcome)
	}
}

// 命中网络 target → OutcomeBlock + onBlock/SubmitSolved 触发。用大 target(2^254) 暴力找块 nonce。
func TestHandleSubmit_Block(t *testing.T) {
	// target = 0x4000…00 = 2^254：pow <= target 概率 ~1/4，几个 nonce 即中。
	easyBE := "4000000000000000000000000000000000000000000000000000000000000000"
	work := katWork(easyBE)
	m, node, jobID := newMgrWithJob(t, work)

	var found *core.FoundBlock
	m.SetCallbacks(func() {},
		func(ctx context.Context, b core.FoundBlock, _ string, submit SubmitFunc) error {
			found = &b
			_, _ = submit(ctx) // 触发 SubmitSolved
			return nil
		}, func(context.Context, core.Share) {})

	// 暴力找一个 pow <= target 的 nonce（份额同时命中网络 target = 块）
	var blockNonce uint64
	got := false
	for n := uint64(0); n < 2000; n++ {
		res := m.HandleSubmit(context.Background(), stratum.KaspaSubmission{JobID: jobID, Nonce: n, Judge: acceptAll})
		if res.Outcome == core.OutcomeBlock {
			blockNonce = n
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("2000 nonce 内未命中 2^254 target（概率上不该发生）")
	}
	if found == nil || found.Height != 1 || found.Reward != "2.00000000" {
		t.Fatalf("onBlock 未回调或字段错: %+v", found)
	}
	if len(node.submitted) != 1 || node.submitted[0] != blockNonce {
		t.Fatalf("SubmitSolved 未按块 nonce 触发: %v (want %d)", node.submitted, blockNonce)
	}
}
