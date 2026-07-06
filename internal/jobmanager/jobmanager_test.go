package jobmanager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// stubNode 回放一个固定 GBT，记录提交的块。实现 nodeIface。
type stubNode struct {
	gbt       json.RawMessage
	submitted string
}

func (s *stubNode) GetTemplate(_ context.Context) (*adapter.BlockTemplate, error) {
	return &adapter.BlockTemplate{Raw: s.gbt, FetchedAt: time.Now()}, nil
}

func TestParseGBTAndBits(t *testing.T) {
	raw := json.RawMessage(`{
		"version": 536870912,
		"previousblockhash": "0f9188f13cb7b2c71f2a335e3a4fc328bf5beb436012afca590b1a11466e2206",
		"transactions": [],
		"coinbasevalue": 5000000000,
		"target": "7fffff0000000000000000000000000000000000000000000000000000000000",
		"mintime": 1296688603,
		"curtime": 1296688700,
		"bits": "207fffff",
		"height": 1,
		"default_witness_commitment": ""
	}`)
	g, err := parseGBT(raw)
	if err != nil {
		t.Fatal(err)
	}
	if g.Height != 1 || g.CoinbaseValue != 5000000000 {
		t.Fatalf("解析错误: %+v", g)
	}
	bits, err := parseBits(g.Bits)
	if err != nil || bits != 0x207fffff {
		t.Fatalf("bits 解析错误: %x %v", bits, err)
	}
}

// 全链路自洽：buildJob → HandleSubmit 用同一套 btcwork 重建，
// 命中极易目标(diff<1)时必爆块，且池组出的块哈希 = 池重算的 header 哈希（逐字节自洽）。
func TestBuildJobAndSubmitSelfConsistent(t *testing.T) {
	// regtest 风格：target 全 F 高位（几乎任何 nonce 都命中）
	raw := json.RawMessage(`{
		"version": 536870912,
		"previousblockhash": "0f9188f13cb7b2c71f2a335e3a4fc328bf5beb436012afca590b1a11466e2206",
		"transactions": [],
		"coinbasevalue": 5000000000,
		"target": "7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"curtime": 1296688700,
		"bits": "207fffff",
		"height": 1
	}`)
	g, _ := parseGBT(raw)

	h, _ := hasher.Get("sha256d")
	reg := stratum.NewJobRegistry()
	node := &stubNode{gbt: raw}
	jm := New("test", node, h, reg, 4, 8)
	// 假 P2PKH 脚本
	jm.poolScript, _ = hex.DecodeString("76a914000102030405060708090a0b0c0d0e0f101112131488ac")

	job, err := jm.buildJob(g, true)
	if err != nil {
		t.Fatal(err)
	}
	reg.Put(job)

	var recordedBlock string
	var recordedHashBE string
	jm.SetCallbacks(func() {}, func(_ context.Context, b core.FoundBlock, rawHex string) error {
		recordedBlock = rawHex
		recordedHashBE = b.Hash
		return nil
	}, nil)

	sub := stratum.Submission{
		Address:      "poolAddr",
		Worker:       "rig1",
		JobID:        job.ID,
		ExtraNonce1:  []byte{0, 0, 0, 1},
		ExtraNonce2:  []byte{0, 0, 0, 0},
		NTime:        job.NTime,
		Nonce:        0,
		RequiredDiff: 0.0001,
	}
	res := jm.HandleSubmit(context.Background(), sub)
	if res.Outcome != core.OutcomeBlock {
		t.Fatalf("超易目标下应爆块, got %v", res.Outcome)
	}
	if recordedBlock == "" {
		t.Fatal("BlockSink 未被回调")
	}
	if node.submitted == "" {
		// stubNode.SubmitBlock 未实现记录，见下
	}

	// 自洽核验：从池组出的块 hex 里抠出 header(前 80 字节)，重算哈希，
	// 必须等于 BlockSink 收到的 blockHashBE。
	blockBytes, err := hex.DecodeString(recordedBlock)
	if err != nil {
		t.Fatal(err)
	}
	header := blockBytes[:80]
	gotHash := hex.EncodeToString(btcwork.Reverse(btcwork.HeaderHash(header)))
	if gotHash != recordedHashBE {
		t.Fatalf("块哈希不自洽:\n组块header算出 %s\nBlockSink记录 %s", gotHash, recordedHashBE)
	}

	// header 里的 merkle root 必须 = 用 submission 重建的 coinbase txid（空块）
	cbTxid, _ := job.Coinbase.TxID(sub.ExtraNonce1, sub.ExtraNonce2)
	merkleInHeader := header[36:68]
	if hex.EncodeToString(merkleInHeader) != hex.EncodeToString(cbTxid) {
		t.Fatal("空块 header 的 merkle root 应等于 coinbase txid")
	}
}

// stale：未知 jobID。
func TestSubmitStale(t *testing.T) {
	h, _ := hasher.Get("sha256d")
	reg := stratum.NewJobRegistry()
	jm := New("test", &stubNode{}, h, reg, 4, 8)
	res := jm.HandleSubmit(context.Background(), stratum.Submission{JobID: "nope", ExtraNonce2: []byte{0, 0, 0, 0}})
	if res.Outcome != core.OutcomeStale {
		t.Fatalf("未知 job 应 stale, got %v", res.Outcome)
	}
}

func (s *stubNode) SubmitBlock(_ context.Context, raw any) error {
	if hexStr, ok := raw.(string); ok {
		s.submitted = hexStr
	}
	return nil
}
func (s *stubNode) PayoutScript(_ context.Context, _ string) (string, error) {
	return "76a914000102030405060708090a0b0c0d0e0f101112131488ac", nil
}
