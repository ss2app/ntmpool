package scashrpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const genesisZeroed112 = "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4adae5494dffff7f1edb1700000000000000000000000000000000000000000000000000000000000000000000"

func TestBuildSolvedBlockGoldenID(t *testing.T) {
	header, _ := hex.DecodeString(genesisZeroed112)
	rawR, _ := hex.DecodeString("86af952d5202ecbf18bef2311391d6cc7951dbb6388a3c78b1a404b6dfdd48e8")
	var r [32]byte
	copy(r[:], rawR)
	got, err := BuildSolvedBlock(&Solution{ZeroedHeader: header, Nonce: 6107, R: r})
	if err != nil {
		t.Fatal(err)
	}
	if want := "0e3ba94819749c208e2526d9b829e0dba109f1bce4e62600c0fc556294f24c82"; got.ID != want {
		t.Fatalf("block ID = %s, want %s", got.ID, want)
	}
	if len(got.Header) != 112 {
		t.Fatalf("solved header 长度 = %d", len(got.Header))
	}
	if hex.EncodeToString(got.Header[80:]) != hex.EncodeToString(rawR) {
		t.Fatal("R 未原样写入 offset 80..111")
	}
}

func TestValidateTemplateRequiresEpochDurationAndTxID(t *testing.T) {
	base := Template{
		PreviousBlockHash: stringOf('0', 64), Target: stringOf('f', 64), Bits: "1e7fffff",
		CurTime: 1, RxEpochDuration: 604800,
	}
	if err := validateTemplate(&base); err != nil {
		t.Fatalf("有效空交易模板: %v", err)
	}
	base.RxEpochDuration = 0
	if err := validateTemplate(&base); err == nil {
		t.Fatal("缺 rx_epoch_duration 未 fail closed")
	}
	base.RxEpochDuration = 604800
	base.Transactions = []Transaction{{Data: "00"}}
	if err := validateTemplate(&base); err == nil {
		t.Fatal("缺 txid 的交易未被拒绝")
	}
}

func TestSubmitSolutionBuilds112HeaderBeforeRPC(t *testing.T) {
	var submitted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "submitblock" || len(req.Params) != 1 {
			http.Error(w, "unexpected RPC", http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(req.Params[0], &submitted); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil})
	}))
	defer server.Close()

	header, _ := hex.DecodeString(genesisZeroed112)
	rawR, _ := hex.DecodeString("86af952d5202ecbf18bef2311391d6cc7951dbb6388a3c78b1a404b6dfdd48e8")
	var r [32]byte
	copy(r[:], rawR)
	id, err := New("scash-test", server.URL, "", "").SubmitSolution(context.Background(), &Solution{
		ZeroedHeader: header, Nonce: 6107, R: r, CoinbaseWitness: []byte{0x00},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "0e3ba94819749c208e2526d9b829e0dba109f1bce4e62600c0fc556294f24c82" {
		t.Fatalf("submit ID = %s", id)
	}
	rawBlock, err := hex.DecodeString(submitted)
	if err != nil {
		t.Fatal(err)
	}
	if len(rawBlock) < 112 || hex.EncodeToString(rawBlock[80:112]) != hex.EncodeToString(rawR) {
		t.Fatal("submitblock 未收到含 R 的 112B solved header")
	}
}

func stringOf(b byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return string(out)
}
