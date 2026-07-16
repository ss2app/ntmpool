package velkarrpc

// 连真 velkard 节点的集成自证（默认 skip，联调时开）：
//
//	VELKAR_NODE=127.0.0.1:26210 \
//	VELKAR_PAYADDR=velkartest:qz... \
//	go test -run TestLive -v ./internal/adapter/velkarrpc/
//
// 终极铁证（不依赖任何外部 KAT）：取一个【真链已挖块】，用本包 Go 代码从其 header
// 重算——① headerHash(header, 真nonce, 真ts) 必须逐字节等于该块的链上 id；② prePow +
// bits→target + velkarhash.CalculatePow(真nonce/ts) 出的 pow 必须 ≤ target。二者皆过，
// 即证 header 序列化 + bits 解码 + VelkarHash 三者全部字节正确、可安全接管挖矿。

import (
	"context"
	"encoding/hex"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/hasher/velkarhash"
)

func TestLiveHeaderHashAndPow(t *testing.T) {
	addr := os.Getenv("VELKAR_NODE")
	if addr == "" {
		t.Skip("设 VELKAR_NODE=host:port 开启真节点联调")
	}
	a, err := New("velkar-live", addr, os.Getenv("VELKAR_PAYADDR"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info, err := a.c.getInfo(ctx)
	if err != nil {
		t.Fatalf("getInfo: %v", err)
	}
	t.Logf("节点 version=%s mempool=%d", info.GetServerVersion(), info.GetMempoolSize())

	dag, err := a.c.getDagInfo(ctx)
	if err != nil {
		t.Fatalf("getBlockDagInfo: %v", err)
	}
	if len(dag.TipHashes) == 0 {
		t.Fatalf("节点无 tip（还没挖块？）")
	}
	tip := dag.TipHashes[0]
	t.Logf("blockCount=%d headerCount=%d virtualDaaScore=%d tip=%s diff=%.2f",
		dag.BlockCount, dag.HeaderCount, dag.VirtualDaaScore, tip, dag.Difficulty)

	// 取真链 tip 块（含 tx，便于顺带看 coinbase）
	blkResp, err := a.c.getBlock(ctx, tip, true)
	if err != nil {
		t.Fatalf("getBlock(tip): %v", err)
	}
	blk := blkResp.GetBlock()
	if blk == nil || blk.GetHeader() == nil {
		t.Fatalf("getBlock 返回空块")
	}
	h := blk.Header

	// ① headerHash(真 nonce, 真 ts) == 链上块 id
	got, err := headerHash(h, h.Nonce, uint64(h.Timestamp))
	if err != nil {
		t.Fatalf("headerHash: %v", err)
	}
	gotHex := hex.EncodeToString(got)
	if gotHex != tip {
		t.Fatalf("✗ headerHash 失配:\n got  %s\n want %s（header 序列化字节错）", gotHex, tip)
	}
	t.Logf("✓ headerHash == 链上块 id（header 序列化字节正确）")

	// ② 真块 PoW：prePow + bits→target + CalculatePow(真 nonce/ts) 必须 ≤ target
	prePow, err := prePowHash(h)
	if err != nil {
		t.Fatalf("prePowHash: %v", err)
	}
	tgtLE, tgtBE := targetFromBits(h.Bits)
	powLE, err := velkarhash.CalculatePow(prePow, uint64(h.Timestamp), h.Nonce, tgtLE)
	if err != nil {
		t.Fatalf("CalculatePow: %v", err)
	}
	powBE := new(big.Int).SetBytes(reverseBytes(powLE))
	if powBE.Cmp(tgtBE) > 0 {
		t.Fatalf("✗ 真链块 pow > target:\n pow    %064x\n target %064x（prePow/target/VelkarHash 有错）", powBE, tgtBE)
	}
	t.Logf("✓ 真链块 pow ≤ target（prePow + bits→target + VelkarHash 全字节正确）")
	t.Logf("  pow=%064x", powBE)
	t.Logf("  tgt=%064x", tgtBE)

	// ③ GetTemplate 全链路（含 pre_pow_hash 派生 + coinbase value）
	tmpl, err := a.GetTemplate(ctx)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	w := tmpl.Raw.(*VelkarWork)
	t.Logf("✓ GetTemplate height=%d prev=%s cbval=%s target=%s jobKey=%s...",
		tmpl.Height, short(tmpl.PrevHash), tmpl.CoinbaseValue, short(tmpl.NetworkTarget), short(w.JobKey))
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
