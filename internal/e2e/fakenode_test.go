package e2e

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/scashcc/ntmpool/internal/btcwork"
)

// fakeNode 是一个最小 bitcoind RPC 模拟：提供极易目标的 GBT、接受 submitblock、
// 支持确认追踪与拆步打款所需的全部方法。用于 pool-core 纯 Go 端到端测试。
type fakeNode struct {
	srv *httptest.Server

	mu         sync.Mutex
	height     uint64
	poolScript string
	submitted  map[uint64]string // height → 我们提交的块 hash（BE）
	blockConf  map[string]int64  // blockhash → 确认数
	broadcasts []string          // 已广播的 rawtx
	sentMany   int
	targetHex  string
}

func newFakeNode(poolScript string) *fakeNode {
	f := &fakeNode{
		height:     100,
		poolScript: poolScript,
		submitted:  map[uint64]string{},
		blockConf:  map[string]int64{},
		// 极易目标：高位 7f，几乎任何 header 都命中（矿工秒出块）
		targetHex: "7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeNode) URL() string { return f.srv.URL }
func (f *fakeNode) Close()      { f.srv.Close() }

// 设置某个已提交块的确认数（推进成熟/制造孤块）。
func (f *fakeNode) setConfirmations(hash string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockConf[hash] = n
}

func (f *fakeNode) submittedHash(height uint64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submitted[height]
}

func (f *fakeNode) broadcastCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.broadcasts) + f.sentMany
}

func (f *fakeNode) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &req)

	f.mu.Lock()
	defer f.mu.Unlock()

	var result any
	var rpcErr any

	switch req.Method {
	case "getblockchaininfo":
		result = map[string]any{
			"blocks": f.height, "headers": f.height,
			"bestblockhash": "00", "initialblockdownload": false,
		}
	case "getaddressinfo", "validateaddress":
		result = map[string]any{"scriptPubKey": f.poolScript, "isvalid": true}
	case "getblocktemplate":
		result = map[string]any{
			"version":           536870912,
			"previousblockhash": fmt.Sprintf("%064x", f.height),
			"transactions":      []any{},
			"coinbasevalue":     int64(5000000000),
			"target":            f.targetHex,
			"curtime":           1600000000,
			"bits":              "207fffff",
			"height":            f.height + 1,
		}
	case "submitblock":
		// params[0] = 完整块 hex。取前 80 字节 header 算真 hash（与池同一算法），
		// 模拟块进主链：height++、记 submitted[height]=hash、初始 1 确认。
		var blockHex string
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &blockHex)
		}
		if b, err := hex.DecodeString(blockHex); err == nil && len(b) >= 80 {
			hashBE := hex.EncodeToString(btcwork.Reverse(btcwork.HeaderHash(b[:80])))
			f.height++
			f.submitted[f.height] = hashBE
			f.blockConf[hashBE] = 1
		}
		result = nil
	case "getblockhash":
		var h uint64
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &h)
		}
		if hash, ok := f.submitted[h]; ok {
			result = hash
		} else {
			rpcErr = map[string]any{"code": -8, "message": "height out of range"}
		}
	case "getblockheader":
		var hash string
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &hash)
		}
		if conf, ok := f.blockConf[hash]; ok {
			result = map[string]any{"confirmations": conf}
		} else {
			rpcErr = map[string]any{"code": -5, "message": "not found"}
		}
	case "gettransaction":
		result = map[string]any{"confirmations": int64(1)}
	case "getbalance":
		result = json.RawMessage("1000000.00000000")
	// 拆步打款
	case "createrawtransaction":
		result = "0100raw"
	case "fundrawtransaction":
		result = map[string]any{"hex": "0100rawfunded"}
	case "signrawtransactionwithwallet":
		result = map[string]any{"hex": "0100rawsigned", "complete": true}
	case "decoderawtransaction":
		result = map[string]any{"txid": "deadbeefpayouttxid"}
	case "sendrawtransaction":
		f.broadcasts = append(f.broadcasts, "0100rawsigned")
		result = "deadbeefpayouttxid"
	case "sendmany":
		f.sentMany++
		result = "sendmanytxid"
	default:
		rpcErr = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}

	resp, _ := json.Marshal(map[string]any{"result": result, "error": rpcErr, "id": "1"})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// matureAll 把所有已提交块的确认数设为 n（推进成熟）。
func (f *fakeNode) matureAll(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, hash := range f.submitted {
		f.blockConf[hash] = n
	}
}
