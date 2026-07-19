package domjob

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter/domrpc"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
)

type fakeNode struct {
	work        *domrpc.Work
	submitHash  string
	submitError *string
	mu          sync.Mutex
	jobID       string
	nonce       string
}

func (n *fakeNode) GetWork(context.Context) (*domrpc.Work, error) { return cloneWork(n.work), nil }

func (n *fakeNode) SubmitWork(_ context.Context, jobID, nonce string) (*domrpc.SubmitResult, error) {
	n.mu.Lock()
	n.jobID, n.nonce = jobID, nonce
	n.mu.Unlock()
	return &domrpc.SubmitResult{
		Accepted: n.submitError == nil, BlockHash: n.submitHash,
		Height: n.work.Height, Error: n.submitError,
	}, nil
}

type fakeHasher struct {
	hash    []byte
	mu      sync.Mutex
	prewarm []string
}

func (h *fakeHasher) Name() string    { return "rx/0" }
func (h *fakeHasher) SelfTest() error { return nil }
func (h *fakeHasher) HashKeyed(_, _ []byte) ([]byte, error) {
	return append([]byte(nil), h.hash...), nil
}
func (h *fakeHasher) PrewarmKey(key []byte) error {
	h.mu.Lock()
	h.prewarm = append(h.prewarm, hex.EncodeToString(key))
	h.mu.Unlock()
	return nil
}

func makeWork(target *big.Int) *domrpc.Work {
	return &domrpc.Work{
		JobID: "0123456789abcdef", Height: 42,
		PrevHash: strings.Repeat("11", 32), Preimage: make([]byte, domrpc.PreimageLen),
		SeedHash: strings.Repeat("22", 32), NextSeedHash: strings.Repeat("33", 32),
		TargetHex: cnwork.EncodeTargetHex256BE(target), NetworkTarget: new(big.Int).Set(target),
		TargetCompact: 0x207fffff,
	}
}

func TestJobConstructionKeepsZeroNonceAndDOMFields(t *testing.T) {
	node := &fakeNode{work: makeWork(new(big.Int).Set(cnwork.Diff1))}
	h := &fakeHasher{hash: bytes.Repeat([]byte{0x01}, 32)}
	m := New("dom", "rx/0", node, h)
	broadcasts := 0
	m.SetCallbacks(func() { broadcasts++ }, nil, nil)
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if broadcasts != 1 {
		t.Fatalf("broadcasts=%d", broadcasts)
	}
	j, ok := m.ConnJobWithClean(0x010203, 1, false)
	if !ok {
		t.Fatal("job missing")
	}
	if j.JobID != node.work.JobID || j.Preimage != strings.Repeat("00", domrpc.PreimageLen) {
		t.Fatalf("job id/preimage mismatch: %+v", j)
	}
	if j.SeedHash != node.work.SeedHash || j.ShareTarget != strings.Repeat("ff", 32) {
		t.Fatalf("seed/target mismatch: %+v", j)
	}
	if j.ExtraNonce1 != "010203" || j.CleanJobs == nil || *j.CleanJobs {
		t.Fatalf("extranonce/clean mismatch: %+v", j)
	}
	wire, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"job_id", "preimage", "seed_hash", "share_target", "clean_jobs", "extranonce1"} {
		if _, ok := fields[required]; !ok {
			t.Fatalf("wire job 缺少 %s: %s", required, wire)
		}
	}
	if _, exists := fields["blob"]; exists {
		t.Fatalf("DOM wire job 不应携带 CryptoNote blob: %s", wire)
	}
	if len(h.prewarm) != 2 || h.prewarm[0] != node.work.SeedHash || h.prewarm[1] != node.work.NextSeedHash {
		t.Fatalf("prewarm=%v", h.prewarm)
	}
	// 相同 node job_id 不换活、不再次广播。
	if err := m.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if broadcasts != 1 {
		t.Fatalf("same job rebroadcasted: %d", broadcasts)
	}
}

func TestExtraNonceAllocationAndNonceOffset216(t *testing.T) {
	en1, ok := extraNonce1ForConn(0x010203)
	if !ok || hex.EncodeToString(en1) != "010203" {
		t.Fatalf("en1=%x ok=%v", en1, ok)
	}
	if _, ok := extraNonce1ForConn(0); ok {
		t.Fatal("conn 0 must not allocate")
	}
	if _, ok := extraNonce1ForConn(maxConnID + 1); ok {
		t.Fatal("24-bit overflow must not allocate")
	}

	const counter = uint64(0x0405060708)
	nonceValue := uint64(0x010203)<<counterBits | counter
	nonce := make([]byte, 8)
	binary.LittleEndian.PutUint64(nonce, nonceValue)
	if !nonceBelongsToConn(nonce, 0x010203) || nonceBelongsToConn(nonce, 0x010204) {
		t.Fatalf("nonce ownership failed: %x", nonce)
	}
	preimage := bytes.Repeat([]byte{0x5a}, domrpc.PreimageLen)
	for i := domrpc.NonceOffset; i < domrpc.NonceOffset+domrpc.NonceLen; i++ {
		preimage[i] = 0
	}
	materialized, err := materializeNonce(preimage, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(materialized[domrpc.NonceOffset:domrpc.NonceOffset+8], nonce) {
		t.Fatalf("nonce not written at 216: got %x want %x", materialized[domrpc.NonceOffset:], nonce)
	}
	if materialized[domrpc.NonceOffset-1] != 0x5a || !bytes.Equal(preimage[domrpc.NonceOffset:], make([]byte, 8)) {
		t.Fatal("materialize mutated bytes outside nonce or source preimage")
	}
}

func TestExtraNonceAllocatorWrapsAndReusesReleasedID(t *testing.T) {
	a := newExtraNonceAllocator()
	a.next = maxConnID - 1
	last, ok := a.acquire()
	if !ok || last != maxConnID {
		t.Fatalf("last=%06x ok=%v", last, ok)
	}
	first, ok := a.acquire()
	if !ok || first != 1 {
		t.Fatalf("wrapped first=%06x ok=%v", first, ok)
	}
	a.release(last)
	a.next = maxConnID - 1
	reused, ok := a.acquire()
	if !ok || reused != maxConnID {
		t.Fatalf("reused=%06x ok=%v", reused, ok)
	}
}

func TestTargetComparisonIsBigEndianAndInclusive(t *testing.T) {
	target := big.NewInt(0x0100)
	equal := make([]byte, 32)
	equal[30], equal[31] = 0x01, 0x00
	less := append([]byte(nil), equal...)
	less[30], less[31] = 0x00, 0xff
	greater := append([]byte(nil), equal...)
	greater[31] = 0x01
	if !meetsTarget(equal, target) || !meetsTarget(less, target) || meetsTarget(greater, target) {
		t.Fatalf("BE <= target comparison failed")
	}
}

func TestHandleSubmitWritesRawNonceAndSubmitsBlock(t *testing.T) {
	target := new(big.Int).Set(cnwork.Diff1)
	node := &fakeNode{work: makeWork(target), submitHash: strings.Repeat("ab", 32)}
	hash := make([]byte, 32)
	hash[31] = 1
	h := &fakeHasher{hash: hash}
	m := New("dom", "rx/0", node, h)
	shares := 0
	blocks := 0
	m.SetCallbacks(nil, func(ctx context.Context, b core.FoundBlock, raw string, submit SubmitFunc) error {
		blocks++
		if b.Height != 42 || b.Reward != "" || len(raw) != domrpc.PreimageLen*2 {
			t.Fatalf("found block=%+v rawlen=%d", b, len(raw))
		}
		final, err := submit(ctx)
		if err != nil || final != node.submitHash {
			t.Fatalf("submit final=%s err=%v", final, err)
		}
		return nil
	}, func(context.Context, core.Share) { shares++ })
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	nonceValue := uint64(0x010203)<<counterBits | 7
	var nonce [8]byte
	binary.LittleEndian.PutUint64(nonce[:], nonceValue)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 0x010203, Address: "dom1miner", Worker: "cpu0",
		JobID: node.work.JobID, NonceHex: hex.EncodeToString(nonce[:]), ResultHex: "",
		Judge: func(d float64) (float64, bool) { return d, true },
	})
	if res.Outcome != core.OutcomeBlock || shares != 1 || blocks != 1 {
		t.Fatalf("result=%v shares=%d blocks=%d", res.Outcome, shares, blocks)
	}
	if node.jobID != node.work.JobID || node.nonce != hex.EncodeToString(nonce[:]) {
		t.Fatalf("node submit job=%s nonce=%s", node.jobID, node.nonce)
	}
}

func TestOptionalResultBadPowAndWrongExtranonce(t *testing.T) {
	node := &fakeNode{work: makeWork(big.NewInt(1)), submitHash: strings.Repeat("ab", 32)}
	hash := make([]byte, 32)
	hash[31] = 2
	m := New("dom", "rx/0", node, &fakeHasher{hash: hash})
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	binary.LittleEndian.PutUint64(nonce[:], uint64(1)<<counterBits|9)
	base := stratum.CNSubmission{
		ConnID: 1, JobID: node.work.JobID, NonceHex: hex.EncodeToString(nonce[:]),
		Judge: func(float64) (float64, bool) { return 1, true },
	}
	bad := base
	bad.ResultHex = strings.Repeat("00", 32)
	if got := m.HandleSubmit(context.Background(), bad).Outcome; got != core.OutcomeBadPow {
		t.Fatalf("bad result outcome=%v", got)
	}
	wrongConn := base
	wrongConn.ConnID = 2
	if got := m.HandleSubmit(context.Background(), wrongConn).Outcome; got != core.OutcomeMalformed {
		t.Fatalf("wrong extranonce outcome=%v", got)
	}
	if got := m.HandleSubmit(context.Background(), base).Outcome; got != core.OutcomeAccepted {
		t.Fatalf("optional empty result outcome=%v", got)
	}
}
