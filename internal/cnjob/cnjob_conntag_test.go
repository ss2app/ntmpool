package cnjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// narrowTagNode 复现 BRVA 的窄 nonce 布局：比特币 80 字节头，nonce@76，
// NonceLen=4 / SearchLen=3 → 连接 tag 只有最高 1 字节。
type narrowTagNode struct{ height uint64 }

func (n *narrowTagNode) GetTemplate(_ context.Context) (*adapter.BlockTemplate, error) {
	blob := make([]byte, 80)
	for i := range blob {
		blob[i] = byte(0x30 + i%7)
	}
	for i := 76; i < 80; i++ {
		blob[i] = 0
	}
	target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))
	return &adapter.BlockTemplate{
		Height: n.height + 1, PrevHash: "prev", CoinbaseValue: "50.00000000",
		Raw: &adapter.BlobWork{
			HashingBlob: blob, NonceOffset: 76, NonceLen: 4, SearchLen: 3,
			SeedHash: seedHex, Algo: "rx/test",
			NetworkTarget: target, HashBigEndian: true,
			// 与 brisviarpc 生产模板对齐：nicehash 分片声明 + pplns 任务模式
			// （xmrig-brisvia 兼容双件套，见 brisviarpc.GetTemplate 注释）。
			Nicehash: true, BrvaJobMode: "pplns",
			HeightHint: n.height + 1, SubmitRef: "tpl-narrow",
		},
	}, nil
}

func (n *narrowTagNode) SubmitBlob(_ context.Context, _ *adapter.BlobSolution) (string, error) {
	return "blockhash-narrow", nil
}

func newNarrowManager(t *testing.T) *Manager {
	t.Helper()
	m := New("brva-test", "rx/brva", &narrowTagNode{height: 100}, testKeyed{})
	if err := m.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return m
}

// 不同连接必须拿到不同的 blob——tag 是矿工之间唯一的区分手段。
// tagBytes=0 时两份 blob 逐字节相同，矿工从 nonce 0 起扫会算出完全相同的 hash 序列，
// 池的有效算力被压成单机（重复 share 因 per-connection 去重照样被接受，指标看不出异常）。
func TestNarrowTagDistinctConnsGetDistinctBlobs(t *testing.T) {
	m := newNarrowManager(t)
	j1, ok1 := m.ConnJob(0x11, 1)
	j2, ok2 := m.ConnJob(0x22, 1)
	if !ok1 || !ok2 {
		t.Fatal("无 job")
	}
	if j1.Blob == j2.Blob {
		t.Fatal("两个连接拿到逐字节相同的 blob → 会扫完全相同的 nonce 空间")
	}
	b1, _ := hex.DecodeString(j1.Blob)
	b2, _ := hex.DecodeString(j2.Blob)
	if b1[79] != 0x11 || b2[79] != 0x22 {
		t.Fatalf("tag 字节未按 connID 写入：b1[79]=0x%02x b2[79]=0x%02x", b1[79], b2[79])
	}
	// 搜索区（低 3 字节）必须清零留给矿工。
	for i := 76; i < 79; i++ {
		if b1[i] != 0 || b2[i] != 0 {
			t.Fatalf("搜索区未清零：b1[%d]=0x%02x b2[%d]=0x%02x", i, b1[i], i, b2[i])
		}
	}
	// 除 nonce 字段外其余 76 字节必须完全一致（同一模板）。
	for i := 0; i < 76; i++ {
		if b1[i] != b2[i] {
			t.Fatalf("模板体第 %d 字节不一致，两连接不在挖同一个块", i)
		}
	}
}

// mineNarrow 模拟【正确实现的锄头】：只滚低 3 字节，原样保留池写的 tag 字节。
func mineNarrow(t *testing.T, m *Manager, connID uint32, keepTag bool) (nonceHex, resultHex string) {
	t.Helper()
	job, ok := m.ConnJob(connID, 1)
	if !ok {
		t.Fatal("无 job")
	}
	blob, _ := hex.DecodeString(job.Blob)
	key, _ := hex.DecodeString(seedHex)
	cand := make([]byte, len(blob))
	copy(cand, blob)
	cand[76], cand[77], cand[78] = 0x01, 0x02, 0x03
	if !keepTag {
		cand[79] = 0 // 错误实现：把 tag 也当搜索区覆盖掉
	}
	h := sha256.New()
	h.Write(key)
	h.Write(cand)
	sum := h.Sum(nil)
	// 回传完整 4 字节 nonce（低 3 = 搜索值，高 1 = 带回的 tag）
	return hex.EncodeToString(cand[76:80]), hex.EncodeToString(sum)
}

// 正确保留 tag 的锄头，share 必须被接受——这是锄头↔池的字节级契约。
func TestNarrowTagMinerKeepingTagIsAccepted(t *testing.T) {
	m := newNarrowManager(t)
	nonce, result := mineNarrow(t, m, 0x5a, true)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 0x5a, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeAccepted && res.Outcome != core.OutcomeBlock {
		t.Fatalf("保留 tag 的 share outcome = %v，want accepted/block", res.Outcome)
	}
}

// 覆盖掉 tag 的锄头必被拒——池用自己记录的 tag 重建候选并重算 hash，对不上。
// 这条锁住的是「锄头必须原样保留 nonce 最高字节」这个契约：一旦锄头改回滚 4 字节，
// 生产上就是 100% Bad hash，此测试会先在 CI 抓到。
func TestNarrowTagMinerOverwritingTagIsRejected(t *testing.T) {
	m := newNarrowManager(t)
	nonce, result := mineNarrow(t, m, 0x5a, false)
	res := m.HandleSubmit(context.Background(), stratum.CNSubmission{
		ConnID: 0x5a, Address: "a", JobID: currentJobID(m),
		NonceHex: nonce, ResultHex: result, Judge: judgeAt(1),
	})
	if res.Outcome != core.OutcomeBadPow {
		t.Fatalf("覆盖 tag 的 share outcome = %v，want badpow", res.Outcome)
	}
}

func TestConnIDAllocatorUniqueReuseAndExhaustion(t *testing.T) {
	m := New("brva-test", "rx/brva", &narrowTagNode{height: 1}, testKeyed{})
	m.SetConnIDSpace(256)

	seen := map[uint32]struct{}{}
	ids := make([]uint32, 0, 256)
	for i := 0; i < 256; i++ {
		id, ok := m.AcquireConnectionID()
		if !ok {
			t.Fatalf("第 %d 个连接就分配失败（空间 256）", i)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("connID %d 被重复分配 → 两个在线矿工会扫相同 nonce", id)
		}
		if id >= 256 {
			t.Fatalf("connID %d 越界，tag 只有 1 字节", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	// 满了必须拒绝，而不是回绕发出重复 tag。
	if _, ok := m.AcquireConnectionID(); ok {
		t.Fatal("空间已满仍分配成功 → 会发出重复 tag")
	}
	// 归还后可复用。
	m.ReleaseConnectionID(ids[7])
	got, ok := m.AcquireConnectionID()
	if !ok {
		t.Fatal("归还后仍分配不出")
	}
	if got != ids[7] {
		t.Fatalf("复用的 id = %d，want %d", got, ids[7])
	}
}

// xmrig-brisvia 兼容 wire 契约：
//   - login extensions 必须含 "nicehash"（否则 stock XMRig 滚满 4 字节 nonce，
//     覆盖 blob[79] 的连接 tag → 池端重建 tag 重算 → 100% badpow）；
//   - job JSON 必须含 "brva_job_mode":"pplns"（否则 xmrig-brisvia 按 solo 语义
//     校验 coinbase 收款人，矿池 coinbase 付池地址 → 任务被拒 code 8）。
func TestNarrowTagWireContractForXmrigBrisvia(t *testing.T) {
	m := newNarrowManager(t)

	ext := m.LoginExtensions()
	hasNicehash := false
	for _, e := range ext {
		if e == "nicehash" {
			hasNicehash = true
		}
	}
	if !hasNicehash {
		t.Fatalf("login extensions = %v，缺 \"nicehash\"：stock XMRig 会滚满 4 字节覆盖连接 tag", ext)
	}

	job, ok := m.ConnJob(0x11, 1)
	if !ok {
		t.Fatal("无 job")
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["brva_job_mode"] != "pplns" {
		t.Fatalf("job JSON brva_job_mode = %v，want \"pplns\"（缺失 = xmrig-brisvia 拒任务）", wire["brva_job_mode"])
	}
}

// space=0（默认）保持原纯自增行为，宽 tag 的币（dragonx 等）不受影响。
func TestConnIDAllocatorUnlimitedByDefault(t *testing.T) {
	m := New("wide-test", "rx/test", &narrowTagNode{height: 1}, testKeyed{})
	a, ok1 := m.AcquireConnectionID()
	b, ok2 := m.AcquireConnectionID()
	if !ok1 || !ok2 {
		t.Fatal("默认不限时不该分配失败")
	}
	if a == b {
		t.Fatalf("默认自增仍给出重复 id: %d", a)
	}
}
