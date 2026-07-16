package scashjob

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter/scashrpc"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/scashrx"
	"github.com/scashcc/ntmpool/internal/stratum"
)

type fixedEngine struct {
	result scashrx.Result
	keys   [][]byte
	calls  int
}

func (f *fixedEngine) Name() string    { return scashrx.Algorithm }
func (f *fixedEngine) SelfTest() error { return nil }
func (f *fixedEngine) Hash(key, header []byte) (scashrx.Result, error) {
	f.calls++
	f.keys = append(f.keys, append([]byte(nil), key...))
	if !scashrx.IsZeroedHeader(header) {
		panic("scashjob 向 hasher 传了非 zeroed112")
	}
	return f.result, nil
}

func TestEpochKeyGolden(t *testing.T) {
	for _, tc := range []struct {
		epoch uint32
		want  string
	}{
		{1, "ccbde830c787b2061cbd9515d9c83d411fcf04cc6e1e47dcc3903c0dee4b1536"},
		{999, "b8ea6d0f30d6f7250bd8f2f62c9d83a61e1391e14cff95888db3a89bbdd183d5"},
	} {
		key, err := EpochKey(tc.epoch, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := scashrx.DisplayHex(key[:]); got != tc.want {
			t.Fatalf("E=%d key display = %s, want %s", tc.epoch, got, tc.want)
		}
	}
}

func TestDifficultyNormalizationTarget(t *testing.T) {
	const wire = 131072.0
	effective, err := EffectiveDifficulty(wire)
	if err != nil {
		t.Fatal(err)
	}
	if effective != 2 {
		t.Fatalf("Deffective = %v, want 2", effective)
	}
	got, err := ShareTarget(wire)
	if err != nil {
		t.Fatal(err)
	}
	want := btcwork.DiffToTarget(wire / 65536.0)
	if got.Cmp(want) != 0 {
		t.Fatalf("share target = %x, want %x", got, want)
	}
	if got.Cmp(btcwork.DiffToTarget(wire)) == 0 {
		t.Fatal("错误地沿用了未归一化 Dwire target")
	}
}

func TestBuildJobConsumesTransactionsAndWitness(t *testing.T) {
	engine := &fixedEngine{}
	m := New("scash", nil, engine, stratum.NewJobRegistry(), 4, 8)
	m.poolScript = []byte{0x51}
	target, err := TargetFromBits(0x1e7fffff)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &scashrpc.Template{
		Version: 1, PreviousBlockHash: string(bytes.Repeat([]byte("0"), 64)),
		CoinbaseValue: 50_0000_0000, Target: fmt.Sprintf("%064x", target),
		MinTime: 99, CurTime: 100, Bits: "1e7fffff", Height: 1,
		DefaultWitnessCommitment: "6a24aa21a9ed" + string(bytes.Repeat([]byte("0"), 64)),
		RxEpochDuration:          604800,
		Transactions: []scashrpc.Transaction{{
			Data: "0100000000", TxID: string(bytes.Repeat([]byte("1"), 64)),
		}},
	}
	job, rules, err := m.buildJob(tpl, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.RawTxs) != 1 || len(job.MerkleBranch) != 1 {
		t.Fatalf("完整交易未进入 job: raw=%d branch=%d", len(job.RawTxs), len(job.MerkleBranch))
	}
	if !job.Coinbase.HasWitness {
		t.Fatal("default_witness_commitment 未进入 coinbase")
	}
	if rules.epochDuration != 604800 || rules.minTime != 99 {
		t.Fatalf("模板规则丢失: %+v", rules)
	}

	tpl.Target = string(bytes.Repeat([]byte("f"), 64))
	if _, _, err := m.buildJob(tpl, false); err == nil {
		t.Fatal("GBT target 与原始 nBits 不一致时未拒绝")
	}
}

func TestGenesisCommitmentMeetsCompactTarget(t *testing.T) {
	rawCM, _ := hex.DecodeString("6848a22489127b6c6cfca8b4052af8d3e1066106aae34ca1eaa50a6a8a380000")
	target, err := TargetFromBits(0x1e7fffff)
	if err != nil {
		t.Fatal(err)
	}
	if !btcwork.HashMeetsTarget(rawCM, target) {
		t.Fatal("genesis CM 未达到 nBits 0x1e7fffff")
	}
}

func TestHandleSubmitUsesOnlyRecomputedCM(t *testing.T) {
	// R 数值极大、CM=0：若错误地比较 R，本 share 会 lowdiff；正确路径必须接受。
	engine := &fixedEngine{}
	for i := range engine.result.R {
		engine.result.R[i] = 0xff
	}
	m, sub := testManager(t, engine)
	var credited float64
	m.onShare = func(_ context.Context, s core.Share) { credited = s.Difficulty }
	res := m.HandleSubmit(context.Background(), sub)
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("outcome = %s, want accepted", res.Outcome)
	}
	if res.CreditDiff != 1 || credited != 1 {
		t.Fatalf("归一化 credit = %v/%v, want 1", res.CreditDiff, credited)
	}
	if engine.calls != 1 {
		t.Fatalf("池端重算次数 = %d, want 1", engine.calls)
	}

	// R=0、CM 数值极大：若把 R 当备用接受路径会误收；正确路径必须 lowdiff。
	engine.result = scashrx.Result{}
	for i := range engine.result.CM {
		engine.result.CM[i] = 0xff
	}
	res = m.HandleSubmit(context.Background(), sub)
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("R 达标但 CM 不达标时 outcome = %s, want lowdiff", res.Outcome)
	}
}

func TestSubmitNTimeSelectsEpochAndValidatesRange(t *testing.T) {
	engine := &fixedEngine{}
	m, sub := testManager(t, engine)
	job, _ := m.reg.Get(sub.JobID)
	job.NTime = 10
	m.mu.Lock()
	m.rules[sub.JobID] = jobRules{epochDuration: 10, minTime: 10}
	m.mu.Unlock()
	m.now = func() time.Time { return time.Unix(1000, 0) }
	sub.NTime = 19
	if got := m.HandleSubmit(context.Background(), sub).Outcome; got != core.OutcomeAccepted {
		t.Fatalf("ntime 19: %s", got)
	}
	sub.NTime = 20
	if got := m.HandleSubmit(context.Background(), sub).Outcome; got != core.OutcomeAccepted {
		t.Fatalf("ntime 20: %s", got)
	}
	if len(engine.keys) != 2 || bytes.Equal(engine.keys[0], engine.keys[1]) {
		t.Fatal("epoch 边界两次 submit 未选择不同 key")
	}
	before := engine.calls
	sub.NTime = 9
	if got := m.HandleSubmit(context.Background(), sub).Outcome; got != core.OutcomeMalformed {
		t.Fatalf("过早 ntime: %s", got)
	}
	if engine.calls != before {
		t.Fatal("非法 ntime 仍进入 RandomX")
	}
}

func testManager(t *testing.T, engine *fixedEngine) (*Manager, stratum.Submission) {
	t.Helper()
	reg := stratum.NewJobRegistry()
	m := New("scash", nil, engine, reg, 4, 8)
	m.now = func() time.Time { return time.Unix(200, 0) }
	cb, err := btcwork.BuildCoinbase(1, 50_0000_0000, []byte{0x51}, nil, poolTag, 8)
	if err != nil {
		t.Fatal(err)
	}
	job := &stratum.Job{
		ID: "1", Height: 1, PrevHashBE: string(bytes.Repeat([]byte("0"), 64)), Coinbase: cb,
		Version: 1, Bits: 0x1e7fffff, NTime: 100,
		NetworkTarget: big.NewInt(-1), NetDiff: 1, RewardSat: 50_0000_0000,
	}
	reg.Put(job)
	m.putRules(job.ID, jobRules{epochDuration: 100, minTime: 90})
	return m, stratum.Submission{
		JobID: job.ID, ExtraNonce1: make([]byte, 4), ExtraNonce2: make([]byte, 4),
		NTime: 100, Nonce: 1, RequiredDiff: 65536,
	}
}
