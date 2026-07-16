package zephyrwallet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZephyrWalletContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch request.Method {
		case "get_balance":
			var params map[string]any
			_ = json.Unmarshal(request.Params, &params)
			if params["asset_type"] != "ZPH" {
				t.Errorf("get_balance asset_type=%v", params["asset_type"])
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"balances": []any{
				map[string]any{"asset_type": "ZSD", "unlocked_balance": uint64(999)},
				map[string]any{"asset_type": "ZPH", "unlocked_balance": uint64(1234567890123)},
			}}})
		case "transfer":
			var params struct {
				SourceAsset      string `json:"source_asset"`
				DestinationAsset string `json:"destination_asset"`
				Destinations     []struct {
					Amount uint64 `json:"amount"`
				} `json:"destinations"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.SourceAsset != "ZPH" || params.DestinationAsset != "ZPH" {
				t.Errorf("transfer assets=%q→%q", params.SourceAsset, params.DestinationAsset)
			}
			if len(params.Destinations) != 1 || params.Destinations[0].Amount != 2500000000000 {
				t.Errorf("transfer destinations=%+v", params.Destinations)
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tx_hash": "tx-zph"}})
		case "get_transfer_by_txid":
			var params struct {
				TxID string `json:"txid"`
			}
			_ = json.Unmarshal(request.Params, &params)
			asset, typ := "ZPH", "out"
			if params.TxID == "wrong-asset" {
				asset = "ZSD"
			}
			if params.TxID == "incoming" {
				typ = "in"
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"transfer": map[string]any{
				"asset_type": asset, "type": typ, "confirmations": 7,
			}}})
		default:
			t.Fatalf("unexpected RPC method %q", request.Method)
		}
	}))
	defer server.Close()

	client := New("zeph-wallet", server.URL, 12)
	balance, err := client.SpendableBalance(context.Background())
	if err != nil || balance != "1.234567890123" {
		t.Fatalf("ZPH unlocked balance=%q err=%v", balance, err)
	}
	txid, err := client.SendMany(context.Background(), map[string]string{"address": "2.500000000000"})
	if err != nil || txid != "tx-zph" {
		t.Fatalf("transfer txid=%q err=%v", txid, err)
	}
	confirmations, err := client.TxConfirmations(context.Background(), txid)
	if err != nil || confirmations != 7 {
		t.Fatalf("confirmations=%d err=%v", confirmations, err)
	}
	if _, err := client.TxConfirmations(context.Background(), "wrong-asset"); err == nil {
		t.Fatal("异资产 tx 不得当作 ZPH payout")
	}
	if _, err := client.TxConfirmations(context.Background(), "incoming"); err == nil {
		t.Fatal("incoming tx 不得当作 payout")
	}
}
