package bitcoinrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func chainAuditNode(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var result any
		switch req.Method {
		case "getblockcount":
			result = uint64(2000)
		case "getblockhash":
			result = "anchor"
		case "listsinceblock":
			result = map[string]any{"transactions": []any{
				map[string]any{"address": "external", "category": "send", "amount": json.RawMessage("-0.00000042"), "txid": "outbound", "blockhash": "block-a", "blockheight": 1500, "confirmations": 3},
				map[string]any{"address": "mine", "category": "send", "amount": json.RawMessage("-1.25000000"), "txid": "outbound", "blockhash": "block-a", "blockheight": 1500, "confirmations": 3},
				map[string]any{"address": "mine", "category": "receive", "amount": json.RawMessage("1.25000000"), "txid": "outbound", "blockhash": "block-a", "blockheight": 1500, "confirmations": 3},
				map[string]any{"address": "pool", "category": "immature", "amount": json.RawMessage("50.00000000"), "txid": "coinbase", "blockhash": "block-b", "blockheight": 1501, "confirmations": 2},
			}}
		case "getaddressinfo":
			var address string
			_ = json.Unmarshal(req.Params[0], &address)
			result = map[string]any{"ismine": address == "mine"}
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "ntmpool"})
	}))
	t.Cleanup(srv.Close)
	return New("audit", srv.URL, "", "")
}

func TestListRecentOutboundExactUnitsAndOwnership(t *testing.T) {
	txs, err := chainAuditNode(t).ListRecentOutbound(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 || txs[0].TxID != "outbound" || txs[0].Confirmations != 3 || txs[0].Height != 1500 {
		t.Fatalf("outbound 聚合错误: %+v", txs)
	}
	if len(txs[0].Outputs) != 2 {
		t.Fatalf("只应包含 send 输出: %+v", txs[0].Outputs)
	}
	if got := txs[0].Outputs[0].Amount; got != 42 {
		t.Fatalf("最小单位精确解析错误: got=%d want=42", got)
	}
	if txs[0].Outputs[0].IsMine || !txs[0].Outputs[1].IsMine {
		t.Fatalf("自有地址标记错误: %+v", txs[0].Outputs)
	}
}

func TestListRecentCoinbase(t *testing.T) {
	receipts, err := chainAuditNode(t).ListRecentCoinbase(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].BlockHash != "block-b" || receipts[0].Amount != 5_000_000_000 {
		t.Fatalf("coinbase 枚举错误: %+v", receipts)
	}
}

func TestJSONAmountToUnitsRejectsFractionLoss(t *testing.T) {
	if _, err := jsonAmountToUnits(json.RawMessage("0.000000001"), 8); err == nil {
		t.Fatal("超过链精度且非零的金额必须拒绝，不能静默截断")
	}
}
