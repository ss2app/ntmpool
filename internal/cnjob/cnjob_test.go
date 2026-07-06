package cnjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// testKeyed 测试用带 key 哈希器：sha256(key||input)。与 e2e 假节点同式。
type testKeyed struct{}

func (testKeyed) Name() string { return "rx/test" }
func (testKeyed) HashKeyed(key, input []byte) ([]byte, error) {
	h := sha256.New()
	h.Write(key)
	h.Write(input)
	return h.Sum(nil), nil
}
func (testKeyed) SelfTest() error { return nil }

// fakeBlobNode 内存假节点。
type fakeBlobNode struct {
	mu        sync.Mutex
	height    uint64
	submitted int
	rejectSub bool
}

const seedHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func (f *fakeBlobNode) GetTemplate(_ context.Context) (*adapter.BlockTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob := make([]byte, 64) // 尾部 8 字节 nonce（zoka 式）
	for i := range blob {
		blob[i] = byte(f.height) // blob 随高度变
	}
	for i := 56; i < 64; i++ {
		blob[i] = 0
	}
	// 1 个前导零 bit 的目标：约一半 hash 命中（测试秒出块）
	target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))
	work := &adapter.BlobWork{
		HashingBlob: blob, NonceOffset: 56, NonceLen: 8, SearchLen: 4,
		SeedHash: seedHex, Algo: "rx/test",
		NetworkTarget: target, HashBigEndian: true,
		HeightHint: f.height + 1, SubmitRef: "tpl-1",
	}
	return &adapter.BlockTemplate{
		Height: f.height + 1, PrevHash: "prev", CoinbaseValue: "50.00000000",
		Raw: work,
	}, nil
}

func (f *fakeBlobNode) SubmitBlob(_ context.Context, sol *adapter.BlobSolution) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectSub {
		return "", context.DeadlineExceeded
	}
	f.submitted++
	return "blockhash-" + hex.EncodeToString(sol.Hash[:4]), nil
}

func judgeAt(required float64) func(float64) (float64, bool) {
	return func(d float64) (float64, bool) {
		if d >= required {
			return required, true
		}
		return 0, false
	}
}

// mineOne 在假节点模板上找一个满足条件的 search nonce。
// wantBlock=true 找命中全网目标的；false 找 1 bit 都不够的（lowdiff 用不上——
// 难度 1 任何 hash 都过，所以 lowdiff 用超高 required 难度制造）。
func mineOne(t *testing.T, m *Manager, connID uint32, wantBlock bool) (nonceHex, resultHex string) {
	t.Helper()
	job, ok := m.ConnJob(connID, 1)
	if !ok {
		t.Fatal("无 job")
	}
	blob, _ := hex.DecodeString(job.Blob)
	key, _ := hex.DecodeString(seedHex)
	target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))
	for n := uint64(0); n < 1<<20; n++ {
		cand := make([]byte, len(blob))
		copy(cand, blob)
		// 只滚低 4 字节（搜索区）
		for i := 0; i < 4; i++ {
			cand[56+i] = byte(n >> (8 * i))
		}
		h, _ := testKeyed{}.HashKeyed(key, cand)
		hit := cnwork.MeetsTarget(h, target, true)
		if hit == wantBlock {
			var nb [4]byte
			for i := 0; i < 4; i++ {
				nb[i] = byte(n >> (8 * i))
			}
			return hex.EncodeToString(nb[:]), hex.EncodeToString(h)
		}
	}
	t.Fatal("找不到合适 nonce")
	return "", ""
}

func newManager(t *testing.T) (*Manager, *fakeBlobNode) {
	t.Helper()
	node := &fakeBlobNode{height: 100}
	m := New("cn-test", "rx/test", node, testKeyed{})
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return m, node
}

func TestCNJobAcceptAndBlock(t *testing.T) {
	m, node := newManager(t)
	var blocks, shares int
	m.SetCallbacks(nil,
		func(_ context.Context, b core.FoundBlock, _ string) error {
			blocks++
			if b.Reward != "50.00000000" || !strings.HasPrefix(b.Hash, "blockhash-") {
				t.Fatalf("FoundBlock 字段错: %+v", b)
			}
			return nil
		},
		func(_ context.Context, _ core.Share) { shares++ },
	)

	// 非爆块 share（1 前导零 bit 不命中）
	nonce, result := mineOne(t, m, 7, false)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 7, Address: "a", Worker: "w", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("outcome = %v", res.Outcome)
	}
	if shares != 1 || blocks != 0 {
		t.Fatalf("shares=%d blocks=%d", shares, blocks)
	}

	// 爆块 share
	nonce, result = mineOne(t, m, 7, true)
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 7, Address: "a", Worker: "w", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBlock {
		t.Fatalf("outcome = %v", res.Outcome)
	}
	if blocks != 1 || node.submitted != 1 || shares != 2 {
		t.Fatalf("blocks=%d submitted=%d shares=%d", blocks, node.submitted, shares)
	}
}

func TestCNJobBadPowTripwire(t *testing.T) {
	m, _ := newManager(t)
	nonce, _ := mineOne(t, m, 1, false)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 1, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce,
		ResultHex: "deadbeef" + strings.Repeat("00", 28), // 伪造 hash
		Judge:     judgeAt(1),
	})
	if res.Outcome != core.OutcomeBadPow {
		t.Fatalf("伪造 hash outcome = %v, want badpow", res.Outcome)
	}
}

// 连接 tag 防伪造：矿工带回的 nonce 高 4 字节被忽略，池用自己记录的 connID 重建。
// 用 connID=5 物化的 blob 挖出的解，冒充 connID=9 提交 → 重算 hash 不同 → badpow。
func TestCNJobConnTagAntiForge(t *testing.T) {
	m, _ := newManager(t)
	nonce, result := mineOne(t, m, 5, false)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 9, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBadPow {
		t.Fatalf("冒充他人连接 outcome = %v, want badpow", res.Outcome)
	}
}

func TestCNJobLowDiffAndStale(t *testing.T) {
	m, _ := newManager(t)
	nonce, result := mineOne(t, m, 2, false)
	// 超高 required 难度 → lowdiff
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 2, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1e30),
	})
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("outcome = %v, want lowdiff", res.Outcome)
	}
	// 未知 job → stale
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 2, Address: "a", JobID: "nope",
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeStale {
		t.Fatalf("outcome = %v, want stale", res.Outcome)
	}
}

func TestCNJobRefreshDedupAndSnapshot(t *testing.T) {
	m, node := newManager(t)
	id1 := currentJobID(m)
	// 同模板再刷新：不发新 job
	if err := m.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if currentJobID(m) != id1 {
		t.Fatal("同模板不应换 job")
	}
	// 高度推进 → 新 job
	node.mu.Lock()
	node.height++
	node.mu.Unlock()
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if currentJobID(m) == id1 {
		t.Fatal("新高度应发新 job")
	}
	h, d, ok := m.Snapshot()
	if !ok || h != 102 || d < 1 {
		t.Fatalf("Snapshot = %d/%v/%v", h, d, ok)
	}
}

func currentJobID(m *Manager) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}
