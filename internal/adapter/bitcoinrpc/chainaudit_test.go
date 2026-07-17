package bitcoinrpc

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestPIVXMissingBlockHeightDerivedAndCursorAdvances(t *testing.T) {
	var tipCalls int
	var anchors []uint64
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
			tipCalls++
			result = uint64(2000)
		case "getblockhash":
			var height uint64
			_ = json.Unmarshal(req.Params[0], &height)
			anchors = append(anchors, height)
			result = "anchor"
		case "listsinceblock":
			result = map[string]any{"transactions": []any{
				map[string]any{"address": "external", "category": "send", "amount": json.RawMessage("-0.00000001"),
					"txid": "pivx-outbound", "confirmations": 3},
			}}
		case "getaddressinfo":
			result = map[string]any{"ismine": false}
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "ntmpool"})
	}))
	t.Cleanup(srv.Close)
	client := New("pivx", srv.URL, "", "")

	first, err := client.ListRecentOutbound(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Height != 1998 {
		t.Fatalf("PIVX 缺失高度应由 tip-confirmations+1 反推: %+v", first)
	}
	second, err := client.ListRecentOutbound(context.Background(), first[0].Height)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Height != 1998 {
		t.Fatalf("第二轮仍应稳定返回反推高度: %+v", second)
	}
	if tipCalls != 2 {
		t.Fatalf("每轮审计只应调用一次 getblockcount: calls=%d", tipCalls)
	}
	if len(anchors) != 2 || anchors[0] != 1000 || anchors[1] != 1997 {
		t.Fatalf("第二轮游标应前进而非重扫初始窗口: anchors=%v", anchors)
	}
}

func TestPIVXMissingBlockHeightNotDerivedWithoutConfirmations(t *testing.T) {
	tests := []struct {
		confirmations int64
		want          uint64
	}{
		{confirmations: 3, want: 1998},
		{confirmations: 0, want: 0},
		{confirmations: -1, want: 0},
		{confirmations: 2000, want: 1},
		{confirmations: 2001, want: 0},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.confirmations), func(t *testing.T) {
			if got := derivedBlockHeight(2000, tt.confirmations); got != tt.want {
				t.Fatalf("derived=%d want=%d", got, tt.want)
			}
		})
	}
}
