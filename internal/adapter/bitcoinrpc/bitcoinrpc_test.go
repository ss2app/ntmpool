package bitcoinrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSatoshiToCoin(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{5000000000, "50.00000000"},
		{1, "0.00000001"},
		{123456789, "1.23456789"},
		{0, "0.00000000"},
	}
	for _, c := range cases {
		if got := satoshiToCoin(c.in, 8); got != c.want {
			t.Errorf("satoshiToCoin(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutputsParamRejectsGarbage(t *testing.T) {
	if _, err := outputsParam(map[string]string{"addr": "0.01"}); err != nil {
		t.Fatalf("合法金额被拒: %v", err)
	}
	if _, err := outputsParam(map[string]string{"addr": "0.01; DROP TABLE"}); err == nil {
		t.Fatal("非法金额应被拒")
	}
}

// mockNode 起一个假节点，按 method 回放结果。
func mockNode(t *testing.T, handler func(method string) (any, *RPCError)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result, rpcErr := handler(req.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": result, "error": rpcErr, "id": "ntmpool",
		})
	}))
	t.Cleanup(srv.Close)
	return New("test", srv.URL, "", "")
}

// 恢复流程重播同一笔 rawtx：already-in-mempool / -27 必须视为成功（幂等铁律）。
func TestBroadcastIdempotent(t *testing.T) {
	for _, e := range []*RPCError{
		{Code: -26, Message: "txn-already-in-mempool"},
		{Code: -27, Message: "Transaction already in block chain"},
		{Code: -26, Message: "already known"},
	} {
		c := mockNode(t, func(string) (any, *RPCError) { return nil, e })
		if err := c.Broadcast(context.Background(), "deadbeef"); err != nil {
			t.Errorf("重播 %q 应视为成功, got %v", e.Message, err)
		}
	}
	// 真错误必须报错
	c := mockNode(t, func(string) (any, *RPCError) {
		return nil, &RPCError{Code: -26, Message: "insufficient fee"}
	})
	if err := c.Broadcast(context.Background(), "deadbeef"); err == nil {
		t.Error("真拒绝不应被吞")
	}
}

// 节点不认识的块（-5）归一为 -1（孤块语义），不报错。
func TestConfirmationsNotFound(t *testing.T) {
	c := mockNode(t, func(string) (any, *RPCError) {
		return nil, &RPCError{Code: -5, Message: "Block not found"}
	})
	n, err := c.Confirmations(context.Background(), "0000", 0)
	if err != nil || n != -1 {
		t.Fatalf("got (%d,%v), want (-1,nil)", n, err)
	}
}

// TxExists：mempool 查不到但钱包认识 → true；两处都 -5 → false。
func TestTxExists(t *testing.T) {
	c := mockNode(t, func(method string) (any, *RPCError) {
		if method == "getmempoolentry" {
			return nil, &RPCError{Code: rpcNotFound, Message: "not in mempool"}
		}
		return map[string]any{"confirmations": 3}, nil
	})
	ok, err := c.TxExists(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("钱包认识的 tx 应为 true, got (%v,%v)", ok, err)
	}
	c2 := mockNode(t, func(string) (any, *RPCError) {
		return nil, &RPCError{Code: rpcNotFound, Message: "not found"}
	})
	ok, err = c2.TxExists(context.Background(), "abc")
	if err != nil || ok {
		t.Fatalf("未知 tx 应为 false, got (%v,%v)", ok, err)
	}
}
