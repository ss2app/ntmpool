package domrpc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validWorkResult() getWorkResult {
	preimage := make([]byte, PreimageLen)
	compact := uint32(0x1e7fffff)
	binary.LittleEndian.PutUint32(preimage[TargetCompactOffset:TargetCompactOffset+4], compact)
	target, _ := expandCompact(compact)
	return getWorkResult{
		JobID: "0123456789abcdef", Height: 12345,
		PrevHash: strings.Repeat("11", 32), Preimage: hex.EncodeToString(preimage),
		SeedHash: strings.Repeat("22", 32), NextSeedHash: nil,
		Target: fmt.Sprintf("%064x", target), TargetCompact: compact,
	}
}

func TestMiningRPCCodecAndBearer(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization=%q", got)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      uint64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.JSONRPC != "2.0" || req.ID == 0 {
			t.Errorf("bad envelope: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "ntm_get_work":
			calls.Add(1)
			var params map[string]any
			if err := json.Unmarshal(req.Params, &params); err != nil || len(params) != 0 {
				t.Errorf("get_work params=%s err=%v", req.Params, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": validWorkResult(),
			})
		case "ntm_submit_work":
			calls.Add(1)
			var params map[string]string
			_ = json.Unmarshal(req.Params, &params)
			if params["job_id"] != "0123456789abcdef" || params["nonce"] != "0807060504030201" {
				t.Errorf("submit params=%v", params)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"accepted": true, "block_hash": strings.Repeat("ab", 32),
					"height": 12345, "error": nil,
				},
			})
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
		}
	}))
	defer srv.Close()

	c, err := New("dom-test", srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()
	w, err := c.GetWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.JobID != "0123456789abcdef" || len(w.Preimage) != PreimageLen || w.NextSeedHash != "" {
		t.Fatalf("work=%+v preimage=%d", w, len(w.Preimage))
	}
	res, err := c.SubmitWork(context.Background(), w.JobID, "0807060504030201")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.Height != w.Height || res.BlockHash != strings.Repeat("ab", 32) {
		t.Fatalf("submit result=%+v", res)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestNormalizeWorkRejectsNonceAndCompactMismatch(t *testing.T) {
	raw := validWorkResult()
	preimage, _ := hex.DecodeString(raw.Preimage)
	preimage[NonceOffset] = 1
	raw.Preimage = hex.EncodeToString(preimage)
	if _, err := normalizeWork(&raw); err == nil || !strings.Contains(err.Error(), "未置零") {
		t.Fatalf("want nonce-zero error, got %v", err)
	}

	raw = validWorkResult()
	raw.TargetCompact++
	if _, err := normalizeWork(&raw); err == nil || !strings.Contains(err.Error(), "preimage") {
		t.Fatalf("want compact/preimage error, got %v", err)
	}
}

func TestExpandCompactMatchesDOMMantissaByteOrder(t *testing.T) {
	target, err := expandCompact(0x1e7fffff)
	if err != nil {
		t.Fatal(err)
	}
	// dom-pow compact_to_target_unchecked 写成 00 00 ff ff 7f 00...，不是
	// Bitcoin 常见的 00 00 7f ff ff 00...。
	want := "0000ffff7f" + strings.Repeat("00", 27)
	if got := fmt.Sprintf("%064x", target); got != want {
		t.Fatalf("DOM compact expansion=%s want=%s", got, want)
	}
}

func TestReadOnlyClassifierAndReward(t *testing.T) {
	blockHash := strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"chain_height": 12})
		case strings.HasPrefix(r.URL.Path, "/block/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"height": 10, "hash": blockHash, "prev_hash": strings.Repeat("00", 32),
				"timestamp": 1, "target": "207fffff",
			})
		case r.URL.Path == "/chain/scan":
			if r.URL.Query().Get("from") != "10" || r.URL.Query().Get("to") != "10" {
				t.Errorf("scan query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"blocks": []map[string]any{{"height": 10, "hash": blockHash, "fees": 123}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New("dom-test", srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()

	conf, err := c.Confirmations(context.Background(), blockHash, 10)
	if err != nil || conf != 3 {
		t.Fatalf("confirmations=%d err=%v", conf, err)
	}
	reward, err := c.BlockReward(context.Background(), blockHash)
	if err != nil || reward != "33.00000123" {
		t.Fatalf("reward=%q err=%v", reward, err)
	}
}

func TestNewRequiresBearerToken(t *testing.T) {
	if _, err := New("dom", "http://127.0.0.1:33369", ""); err == nil {
		t.Fatal("empty token should fail closed")
	}
}

func TestBlockSubsidyIntegerRecurrence(t *testing.T) {
	if got := blockSubsidyNoms(0); got != 3_300_000_000 {
		t.Fatalf("epoch0=%d", got)
	}
	if got := blockSubsidyNoms(halvingInterval); got != 2_211_000_000 {
		t.Fatalf("epoch1=%d", got)
	}
}
