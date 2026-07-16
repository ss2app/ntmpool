package zephyrrpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/cnwork"
)

func TestZephyrDaemonContract(t *testing.T) {
	hashing := make([]byte, 64)
	template := make([]byte, 80)
	for i := range hashing {
		hashing[i] = byte(i)
	}
	for i := range template {
		template[i] = byte(0xa0 + i)
	}

	const blockID = "abcedf0123456789"
	var submitted []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "get_block_template":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "ntmpool", "result": map[string]any{
				"blocktemplate_blob": hex.EncodeToString(template),
				"blockhashing_blob":  hex.EncodeToString(hashing),
				"difficulty":         uint64(1),
				"wide_difficulty":    "0x10000000000000001",
				"height":             uint64(123),
				"prev_hash":          "prev",
				"seed_hash":          "seed",
				"expected_reward":    uint64(9999999999999),
			}})
		case "submit_block":
			var params []string
			if err := json.Unmarshal(request.Params, &params); err != nil || len(params) != 1 {
				t.Fatalf("submit_block params: %s err=%v", request.Params, err)
			}
			submitted, _ = hex.DecodeString(params[0])
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "ntmpool", "result": map[string]any{
				"status": "OK", "block_id": blockID,
			}})
		case "get_block":
			var params struct {
				Hash string `json:"hash"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Hash != blockID {
				t.Errorf("get_block hash=%q want %q", params.Hash, blockID)
			}
			blockJSON := `{"miner_tx":{"vout":[` +
				`{"amount":1234567890123,"target":{"tagged_key":{"asset_type":"ZPH","key":"miner"}}},` +
				`{"amount":700000000000,"target":{"tagged_key":{"asset_type":"ZPH","key":"governance"}}},` +
				`{"amount":900000000000,"target":{"tagged_key":{"asset_type":"ZSD","key":"fee"}}}` +
				`]}}`
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "ntmpool", "result": map[string]any{
				"status": "OK", "json": blockJSON,
			}})
		default:
			t.Fatalf("unexpected RPC method %q", request.Method)
		}
	}))
	defer server.Close()

	client := New("zeph", server.URL, "rx/0", 12)
	client.SetPoolAddress("pool-address")
	tpl, err := client.GetTemplate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	work, ok := tpl.Raw.(*adapter.BlobWork)
	if !ok {
		t.Fatalf("Raw=%T want *adapter.BlobWork", tpl.Raw)
	}
	if hex.EncodeToString(work.HashingBlob) != hex.EncodeToString(hashing) {
		t.Fatal("blockhashing_blob 未透明传递")
	}
	if work.NonceOffset != 39 || work.NonceLen != 4 || work.SubmitRef != hex.EncodeToString(template) {
		t.Fatalf("blob contract 错误: offset=%d len=%d submitRef=%v", work.NonceOffset, work.NonceLen, work.SubmitRef)
	}
	wantTarget := new(big.Int).Div(new(big.Int).Set(cnwork.Diff1), func() *big.Int {
		v, _ := new(big.Int).SetString("18446744073709551617", 10)
		return v
	}())
	if work.NetworkTarget.Cmp(wantTarget) != 0 {
		t.Fatalf("wide_difficulty target 不精确: got=%s want=%s", work.NetworkTarget, wantTarget)
	}

	const nonce = uint64(0x44332211)
	gotID, err := client.SubmitBlob(context.Background(), &adapter.BlobSolution{Work: work, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	if gotID != blockID {
		t.Fatalf("block_id=%q want %q", gotID, blockID)
	}
	if len(submitted) != len(template) || hex.EncodeToString(submitted[:39]) != hex.EncodeToString(template[:39]) ||
		hex.EncodeToString(submitted[43:]) != hex.EncodeToString(template[43:]) {
		t.Fatal("submit_block 未保持模板 blob 透明")
	}
	if got := cnwork.NonceFieldLE(submitted, 39, 4); got != nonce {
		t.Fatalf("nonce=%x want %x", got, nonce)
	}

	reward, err := client.BlockReward(context.Background(), gotID)
	if err != nil {
		t.Fatal(err)
	}
	if reward != "1.234567890123" {
		t.Fatalf("ZPH miner reward=%s；治理/异资产 output 不得计入", reward)
	}
}

func TestBlockRewardRejectsNonZPHFirstOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"json": `{"miner_tx":{"vout":[{"amount":1,"target":{"tagged_key":{"asset_type":"ZSD"}}}]}}`,
		}})
	}))
	defer server.Close()
	if _, err := New("zeph", server.URL, "rx/0", 12).BlockReward(context.Background(), "id"); err == nil {
		t.Fatal("首个 output 不是 ZPH 时必须拒绝")
	}
}
