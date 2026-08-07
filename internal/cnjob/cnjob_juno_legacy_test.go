package cnjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/cnwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// singleKeyed 单段哈希器（形状对齐 rx/juno：pow = result = 同一个 hash）。
type singleKeyed struct{}

func (singleKeyed) Name() string { return "rx/juno-test" }
func (singleKeyed) HashKeyed(key, input []byte) ([]byte, error) {
	h := sha256.Sum256(append(append([]byte("J"), key...), input...))
	return h[:], nil
}
func (singleKeyed) SelfTest() error { return nil }

var _ hasher.KeyedHasher = singleKeyed{}

// fakeJunoNode juno 形状假节点：140B blob、nonce 字段 [108:140]
// （矿工滚 [108:112]、连接 tag [112:116]、实例盐 [116:120]），单段 PowIsBlockHash。
type fakeJunoNode struct {
	mu     sync.Mutex
	height uint64
}

func junoTarget() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 250), big.NewInt(1))
}

func (f *fakeJunoNode) GetTemplate(_ context.Context) (*adapter.BlockTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob := make([]byte, 140)
	for i := range blob {
		blob[i] = byte(f.height + uint64(i))
	}
	for i := 108; i < 140; i++ {
		blob[i] = 0
	}
	copy(blob[116:120], []byte{0xDE, 0xAD, 0xBE, 0xEF}) // 实例盐（老锄头会把它回传成 0）
	work := &adapter.BlobWork{
		HashingBlob: blob, NonceOffset: 108, NonceLen: 8, SearchLen: 4,
		WireNonceLen: 32, WireNonceLenient: true, PowIsBlockHash: true,
		SeedHash: seedHex, Algo: "rx/juno-test",
		NetworkTarget: junoTarget(), HashBigEndian: false,
		TargetCompactLE: true,
		HeightHint:      f.height + 1,
		JobKey:          fmt.Sprintf("%d", f.height+1),
	}
	return &adapter.BlockTemplate{
		Height: f.height + 1, PrevHash: "prev", CoinbaseValue: "6.25000000", Raw: work,
	}, nil
}

func (f *fakeJunoNode) SubmitBlob(_ context.Context, _ *adapter.BlobSolution) (string, error) {
	return "node-hash", nil
}

func newJunoManager(t *testing.T) *Manager {
	t.Helper()
	m := New("juno-test", "rx/juno-test", &fakeJunoNode{height: 400}, singleKeyed{})
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return m
}

// junoMine 在物化 blob 上滚低 4 字节找一个够 required 难度、又不命中全网目标的解。
// 返回矿工实际哈希的 candidate 与结果 hex。
func junoMine(t *testing.T, m *Manager, connID uint32, required float64) (cand []byte, resultHex string) {
	t.Helper()
	job, ok := m.ConnJob(connID, 1)
	if !ok {
		t.Fatal("无 job")
	}
	blob, _ := hex.DecodeString(job.Blob)
	key, _ := hex.DecodeString(seedHex)
	for n := uint64(0); n < 1<<20; n++ {
		c := make([]byte, len(blob))
		copy(c, blob)
		for i := 0; i < 4; i++ {
			c[108+i] = byte(n >> (8 * i))
		}
		h, _ := singleKeyed{}.HashKeyed(key, c)
		if cnwork.ShareDiff(h, false) >= required && !cnwork.MeetsTarget(h, junoTarget(), false) {
			return c, hex.EncodeToString(h)
		}
	}
	t.Fatal("找不到满足条件的 nonce")
	return nil, ""
}

// ★2026-08-07 JUNO 生产实抓的回归：官方锄头 junorig < v6.24.0-juno.6 上报 share 时
// 把 nonce 字段重建成「低 8 字节 + 其余 24 字节全零」——连接 tag 与实例盐被抹零，
// 但它【实际参与哈希的是池下发的完整 blob】。严格回显校验会把这些 share 全判
// malformed → autoban → 矿工被踢下线（"no active pools"）。
// WireNonceLenient 下必须照常接受，且哈希校验仍然生效。
func TestJunoLegacyMinerZeroedTagAccepted(t *testing.T) {
	m := newJunoManager(t)
	var shares int
	m.SetCallbacks(nil, nil, func(_ context.Context, _ core.Share) { shares++ })

	const connID = 7
	cand, result := junoMine(t, m, connID, 2)

	// 复刻 juno.4 的上报形状：32 字节里只保留低 8 字节，tag/盐位置全零
	legacy := make([]byte, 32)
	copy(legacy[:8], cand[108:116])
	for i := 4; i < 8; i++ {
		legacy[i] = 0 // tag 字节也被老锄头抹掉（它只写 uint32 nonce）
	}

	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: connID, Address: "j1test", JobID: currentJobID(m),
		NonceHex: hex.EncodeToString(legacy), ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("老锄头 share outcome = %v, want accepted（malformed = 又踢矿工）", res.Outcome)
	}
	if shares != 1 {
		t.Fatalf("share 未计数: %d", shares)
	}
}

// 老锄头若只回传 4 字节 nonce（第三方 patched xmrig 常见），同样应被接受。
func TestJunoShortNonceAccepted(t *testing.T) {
	m := newJunoManager(t)
	m.SetCallbacks(nil, nil, func(_ context.Context, _ core.Share) {})
	const connID = 9
	cand, result := junoMine(t, m, connID, 2)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: connID, Address: "j1test", JobID: currentJobID(m),
		NonceHex: hex.EncodeToString(cand[108:112]), ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("4 字节 nonce outcome = %v, want accepted", res.Outcome)
	}
}

// 放宽回显 ≠ 放弃校验：哈希对不上仍须 badpow（真正的防线）。
func TestJunoLenientStillChecksHash(t *testing.T) {
	m := newJunoManager(t)
	m.SetCallbacks(nil, nil, func(_ context.Context, _ core.Share) {})
	const connID = 11
	cand, _ := junoMine(t, m, connID, 2)
	bogus := make([]byte, 32) // 编造的结果
	for i := range bogus {
		bogus[i] = 0x11
	}
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: connID, Address: "j1test", JobID: currentJobID(m),
		NonceHex: hex.EncodeToString(cand[108:140]), ResultHex: hex.EncodeToString(bogus),
		Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBadPow {
		t.Fatalf("伪造 result outcome = %v, want badpow", res.Outcome)
	}
}

// 长度非法（既非 wire 长度、也不落在 [SearchLen, wireLen) 区间）仍判 malformed。
func TestJunoOversizeNonceStillMalformed(t *testing.T) {
	m := newJunoManager(t)
	m.SetCallbacks(nil, nil, func(_ context.Context, _ core.Share) {})
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 13, Address: "j1test", JobID: currentJobID(m),
		NonceHex: hex.EncodeToString(make([]byte, 40)), ResultHex: hex.EncodeToString(make([]byte, 32)),
		Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeMalformed {
		t.Fatalf("超长 nonce outcome = %v, want malformed", res.Outcome)
	}
}
