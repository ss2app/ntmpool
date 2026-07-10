package cnjob

import (
	"bytes"
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

// twoStageKeyed 测试用双段哈希器（形状对齐 rx/dragonx：result=内层、pow=外层）。
type twoStageKeyed struct{}

func (twoStageKeyed) Name() string { return "rx/twostage-test" }
func (twoStageKeyed) HashKeyedTwoStage(key, input []byte) (result, pow []byte, err error) {
	r := sha256.Sum256(append(append([]byte("R"), key...), input...))
	p := sha256.Sum256(append(append([]byte("P"), key...), input...))
	return r[:], p[:], nil
}
func (h twoStageKeyed) HashKeyed(key, input []byte) ([]byte, error) {
	_, pow, err := h.HashKeyedTwoStage(key, input)
	return pow, err
}
func (twoStageKeyed) SelfTest() error { return nil }

var _ hasher.TwoStageKeyedHasher = twoStageKeyed{}

// fakeDrgNode dragonx 形状假节点：140B blob、nonce 字段 [108:140]（滚 [108:112]、
// tag [112:116]、保留区 [116:140] 带模板盐）、PowIsBlockHash。
type fakeDrgNode struct {
	mu          sync.Mutex
	height      uint64
	lastSol     *adapter.BlobSolution
	curtimeByte byte // 每次 GetTemplate 抖动一次（模拟 dragonx GBT curtime 每秒变）
}

func drgTarget() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))
}

func (f *fakeDrgNode) GetTemplate(_ context.Context) (*adapter.BlockTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob := make([]byte, 140)
	for i := range blob {
		blob[i] = byte(f.height + uint64(i))
	}
	for i := 108; i < 140; i++ {
		blob[i] = 0
	}
	blob[120] = 0xAB          // 保留区模板盐（实例隔离位，矿工必须原样回显）
	blob[104] = f.curtimeByte // 模拟 curtime 抖动：blob 变但同高度同 job
	work := &adapter.BlobWork{
		HashingBlob: blob, NonceOffset: 108, NonceLen: 8, SearchLen: 4,
		WireNonceLen: 32, PowIsBlockHash: true,
		SeedHash: seedHex, Algo: "rx/twostage-test",
		NetworkTarget: drgTarget(), HashBigEndian: false,
		TargetCompactLE: true,
		HeightHint:      f.height + 1,
		// JobKey = 高度（忽略 blob 里随查询变化的 curtimeByte）→ 同高度不换 job
		JobKey: fmt.Sprintf("%d", f.height+1),
	}
	return &adapter.BlockTemplate{
		Height: f.height + 1, PrevHash: "prev", CoinbaseValue: "3.00000000",
		Raw: work,
	}, nil
}

func (f *fakeDrgNode) SubmitBlob(_ context.Context, sol *adapter.BlobSolution) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSol = sol
	return "node-authoritative-hash", nil
}

func newDrgManager(t *testing.T) (*Manager, *fakeDrgNode) {
	t.Helper()
	node := &fakeDrgNode{height: 200}
	m := New("drg-test", "rx/twostage-test", node, twoStageKeyed{})
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return m, node
}

// drgMine 在物化 blob 上滚低 4 字节找满足条件的解，返回 (32B wire nonce hex, result hex, pow)。
// cond 以 (powDiff 达标?, resultDiff 达标?) 挑选。
func drgMine(t *testing.T, m *Manager, connID uint32, required float64, wantPowOK, wantAuxOK bool) (nonceHex, resultHex string, pow []byte) {
	t.Helper()
	job, ok := m.ConnJob(connID, 1)
	if !ok {
		t.Fatal("无 job")
	}
	blob, _ := hex.DecodeString(job.Blob)
	key, _ := hex.DecodeString(seedHex)
	for n := uint64(0); n < 1<<20; n++ {
		cand := make([]byte, len(blob))
		copy(cand, blob)
		for i := 0; i < 4; i++ {
			cand[108+i] = byte(n >> (8 * i))
		}
		r, p, _ := twoStageKeyed{}.HashKeyedTwoStage(key, cand)
		powOK := cnwork.ShareDiff(p, false) >= required
		auxOK := cnwork.ShareDiff(r, false) >= required
		if powOK == wantPowOK && auxOK == wantAuxOK {
			// 非爆块场景必须同时避开全网目标（爆块判定只看 pow）
			if !wantPowOK && cnwork.MeetsTarget(p, drgTarget(), false) {
				continue
			}
			return hex.EncodeToString(cand[108:140]), hex.EncodeToString(r), p
		}
	}
	t.Fatal("找不到满足条件的 nonce")
	return "", "", nil
}

// JobKey 去重：curtime 抖动（blob 变但同高度）不换 job，避免矿工 job 过期 stale。
func TestDrgJobKeyNoChurnOnCurtime(t *testing.T) {
	m, node := newDrgManager(t)
	id1 := currentJobID(m)
	// curtime 抖动 3 次（blob 每次都变），高度不变 → job 必须不换
	for i := 0; i < 3; i++ {
		node.mu.Lock()
		node.curtimeByte = byte(i + 1)
		node.mu.Unlock()
		if err := m.Refresh(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if currentJobID(m) != id1 {
			t.Fatalf("curtime 抖动第 %d 次换了 job（应保持 %s，得 %s）—— stale 洪峰根因未修", i, id1, currentJobID(m))
		}
	}
	// 高度推进 → 必须换 job
	node.mu.Lock()
	node.height++
	node.mu.Unlock()
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if currentJobID(m) == id1 {
		t.Fatal("高度变化必须换 job")
	}
}

// 双段路径：badpow 比内层 result、爆块 hash 用外层 pow 反转（PowIsBlockHash）、
// BlobSolution 带 AuxHash。
func TestDrgTwoStageBlockPath(t *testing.T) {
	m, node := newDrgManager(t)
	var got core.FoundBlock
	m.SetCallbacks(nil,
		func(ctx context.Context, b core.FoundBlock, _ string, submit SubmitFunc) error {
			got = b
			if _, err := submit(ctx); err != nil {
				t.Fatalf("submit: %v", err)
			}
			return nil
		},
		func(_ context.Context, _ core.Share) {},
	)
	// 全网目标 2^255-1 ≈ 难度 2：找 pow 命中的解
	nonce, result, pow := drgMine(t, m, 3, 2, true, true)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 3, Address: "zs1test", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBlock {
		t.Fatalf("outcome = %v, want block", res.Outcome)
	}
	wantHash := hex.EncodeToString(reverseBytes(pow))
	if got.Hash != wantHash {
		t.Fatalf("FoundBlock.Hash = %s, want 反转 pow %s（PowIsBlockHash）", got.Hash, wantHash)
	}
	node.mu.Lock()
	sol := node.lastSol
	node.mu.Unlock()
	if sol == nil || hex.EncodeToString(sol.AuxHash) != result {
		t.Fatalf("BlobSolution.AuxHash 未带内层 rx 结果")
	}
	if !bytes.Equal(sol.Hash, pow) {
		t.Fatalf("BlobSolution.Hash 应为外层 pow")
	}
}

// 双接受：pow 难度不达标但内层 result 达标 → 仍计 share（legacy 锄头兼容，miningcore 同款）。
func TestDrgDualAcceptByAux(t *testing.T) {
	m, _ := newDrgManager(t)
	m.SetCallbacks(nil, nil, func(_ context.Context, _ core.Share) {})
	const required = 4
	nonce, result, _ := drgMine(t, m, 5, required, false, true)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 5, Address: "zs1test", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(required),
	})
	if res.Outcome != core.OutcomeAccepted {
		t.Fatalf("outcome = %v, want accepted（内层达标即计）", res.Outcome)
	}
	// 两个口径都不达标 → lowdiff
	nonce, result, _ = drgMine(t, m, 5, required, false, false)
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 5, Address: "zs1test", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(required),
	})
	if res.Outcome != core.OutcomeLowDiff {
		t.Fatalf("outcome = %v, want lowdiff", res.Outcome)
	}
}

// 宽 wire nonce 回显校验：改保留区任一字节 → malformed；长度不对 → malformed；
// 伪造 result → badpow。
func TestDrgWireNonceEchoAndBadPow(t *testing.T) {
	m, _ := newDrgManager(t)
	nonce, result, _ := drgMine(t, m, 8, 1, true, true)

	// 篡改保留区（模板盐位 [120] 对应 wire nonce 的第 12 字节 = hex [24:26]）
	nb, _ := hex.DecodeString(nonce)
	nb[12] ^= 0xFF
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 8, Address: "a", JobID: currentJobID(m),
		NonceHex: hex.EncodeToString(nb), ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeMalformed {
		t.Fatalf("篡改保留区 outcome = %v, want malformed", res.Outcome)
	}

	// 长度不对（只交 8 字节）
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 8, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce[:16], ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeMalformed {
		t.Fatalf("短 nonce outcome = %v, want malformed", res.Outcome)
	}

	// 伪造内层 result
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 8, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: "deadbeef" + hex.EncodeToString(make([]byte, 28)),
		Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBadPow {
		t.Fatalf("伪造 result outcome = %v, want badpow", res.Outcome)
	}

	// 连接 tag 防伪造：connID=8 的解冒充 connID=9 → 重建 blob 不同 → 回显校验直接拦
	res = m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 9, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeMalformed {
		t.Fatalf("跨连接重放 outcome = %v, want malformed（回显与重建 tag 不符）", res.Outcome)
	}
}
