package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/scashcc/ntmpool/internal/hasher"
)

// drgTestKeyed dragonx e2e 双段哈希器：内层假 rx = sha256(key||input)，
// 外层 = 【真实结构】sha256d(blob140 || 0x20 || 内层)。外层与假节点的独立验块
// 逐字节同构 —— e2e 锚的是管线与 173B 拼装，不是 RandomX 本体（那个由
// dragonxrx 三层金锚在 -tags randomx 下锚）。
type drgTestKeyed struct{}

func (drgTestKeyed) Name() string { return "rx/drgtest" }
func (h drgTestKeyed) HashKeyedTwoStage(key, input []byte) (result, pow []byte, err error) {
	if len(input) != 140 {
		return nil, nil, fmt.Errorf("drgtest: blob 须 140B, got %d", len(input))
	}
	r := sha256.Sum256(append(append([]byte{}, key...), input...))
	full := make([]byte, 173)
	copy(full, input)
	full[140] = 0x20
	copy(full[141:], r[:])
	d1 := sha256.Sum256(full)
	d2 := sha256.Sum256(d1[:])
	return r[:], d2[:], nil
}
func (h drgTestKeyed) HashKeyed(key, input []byte) ([]byte, error) {
	_, pow, err := h.HashKeyedTwoStage(key, input)
	return pow, err
}
func (drgTestKeyed) SelfTest() error { return nil }

var _ hasher.TwoStageKeyedHasher = drgTestKeyed{}

func init() { hasher.RegisterKeyed(drgTestKeyed{}) }

// fakeDrgNode 假 dragonxd（JSON-RPC）：GBT 带现成 coinbasetxn、submitblock
// 节点端真验块（假 rx 重算 + 173B sha256d + target）、z_* 异步 opid 状态机、
// 成熟 coinbase 的 listunspent/z_shieldcoinbase。
type fakeDrgNode struct {
	srv *httptest.Server

	mu         sync.Mutex
	tip        uint64
	ourBlocks  map[uint64]string // height → display block hash（我们提交的）
	hashByHash map[string]uint64 // display hash → height（getblockheader 用）
	opSeq      int
	ops        map[string]*fakeOp
	sent       int      // z_sendmany 成功次数
	sentFrom   string   // 最近一次 z_sendmany 的 fromaddress
	sentAddrs  []string // 最近一次收款地址
	shielded   int      // z_shieldcoinbase 次数
	polls      int      // z_getoperationstatus 调用次数（验证真的在轮询）
}

type fakeOp struct {
	stage int // 每次 status 查询 +1；≥2 → success（模拟异步 zk 证明耗时）
	txid  string
	kind  string
}

const drgVault = "zs1vaultfake000000000000000000000000000000000000000000000000000"

func newFakeDrgNode() *fakeDrgNode {
	f := &fakeDrgNode{
		tip:        100,
		ourBlocks:  map[uint64]string{},
		hashByHash: map[string]uint64{},
		ops:        map[string]*fakeOp{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeDrgNode) URL() string { return f.srv.URL }
func (f *fakeDrgNode) Close()      { f.srv.Close() }

// hashAt 基链高度的确定性假块 hash（display）。
func (f *fakeDrgNode) hashAt(h uint64) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("drgchain-%d", h)))
	return hex.EncodeToString(s[:])
}

func (f *fakeDrgNode) advance(n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tip += n
}

// orphanAll 把我们提交的块全部甩出主链。
func (f *fakeDrgNode) orphanAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h, hash := range f.ourBlocks {
		delete(f.hashByHash, hash)
		f.ourBlocks[h] = f.hashAt(h + 777777) // 主链换成别的 hash
	}
}

func (f *fakeDrgNode) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

func (f *fakeDrgNode) shieldCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shielded
}

func (f *fakeDrgNode) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

func rpcOK(w http.ResponseWriter, id, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": id})
}

func rpcErr(w http.ResponseWriter, id any, code int, msg string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": nil, "error": map[string]any{"code": code, "message": msg}, "id": id,
	})
}

func (f *fakeDrgNode) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	switch req.Method {
	case "getblockchaininfo":
		rpcOK(w, req.ID, map[string]any{
			"blocks": f.tip, "headers": f.tip, "bestblockhash": f.hashAt(f.tip),
		})

	case "getblocktemplate":
		coinbaseHex := "0400008085202f8901e2e2" // 假 coinbase（透传字节即可）
		cbHash := sha256.Sum256([]byte(fmt.Sprintf("cb-%d", f.tip+1)))
		rpcOK(w, req.ID, map[string]any{
			"version":              4,
			"previousblockhash":    f.hashAt(f.tip),
			"finalsaplingroothash": f.hashAt(999999999),
			"target":               hexRepeatE2E("ff", 32), // 2^256-1：每个 share 必爆块（e2e 确定性）
			"bits":                 "1e010eda",
			"curtime":              1783700000 + int64(f.tip),
			"mintime":              1783600000,
			"height":               f.tip + 1,
			"coinbasetxn": map[string]any{
				"data": coinbaseHex, "hash": hex.EncodeToString(cbHash[:]),
				"coinbasevalue": 300000000,
			},
			"transactions": []any{},
		})

	case "getblockhash":
		var h uint64
		_ = json.Unmarshal(req.Params[0], &h)
		if hash, ok := f.ourBlocks[h]; ok {
			rpcOK(w, req.ID, hash)
			return
		}
		rpcOK(w, req.ID, f.hashAt(h))

	case "getblockheader":
		var hash string
		_ = json.Unmarshal(req.Params[0], &hash)
		if h, ok := f.hashByHash[hash]; ok {
			rpcOK(w, req.ID, map[string]any{"confirmations": f.tip - h + 1, "height": h})
			return
		}
		rpcErr(w, req.ID, -5, "Block not found")

	case "submitblock":
		var blockHex string
		_ = json.Unmarshal(req.Params[0], &blockHex)
		raw, err := hex.DecodeString(blockHex)
		if err != nil || len(raw) < 174 || raw[140] != 0x20 {
			rpcOK(w, req.ID, "rejected: malformed")
			return
		}
		blob, sol := raw[:140], raw[141:173]
		// 节点端真验块①：solution 必须 == 假 rx(seed, blob)。
		// e2e 高度恒 <1088 → seedHeight(h,1024,64) 恒 0 → seed 恒 = hashAt(0) 反转。
		seedDisp, _ := hex.DecodeString(f.hashAt(0))
		seed := reverse32E2E(seedDisp)
		want := sha256.Sum256(append(append([]byte{}, seed...), blob...))
		if !equalBytes(sol, want[:]) {
			rpcOK(w, req.ID, "rejected: bad-solution")
			return
		}
		// 节点端真验块②：块 hash = sha256d(173B) 反转（target 为 2^256-1，
		// e2e 确定性——PoW 强度由①的 solution 重算与 dragonxrx 金锚层把守）
		d1 := sha256.Sum256(raw[:173])
		d2 := sha256.Sum256(d1[:])
		rev := make([]byte, 32)
		for i, v := range d2 {
			rev[31-i] = v
		}
		display := hex.EncodeToString(rev)
		height := f.tip + 1
		f.ourBlocks[height] = display
		f.hashByHash[display] = height
		f.tip = height
		rpcOK(w, req.ID, nil) // zcash 系：null = accepted

	case "getnetworkhashps":
		rpcOK(w, req.ID, 466000.0)

	case "z_getbalance":
		rpcOK(w, req.ID, 350.0)

	case "z_sendmany":
		var from string
		_ = json.Unmarshal(req.Params[0], &from)
		var amounts []struct {
			Address string `json:"address"`
		}
		_ = json.Unmarshal(req.Params[1], &amounts)
		if from != drgVault {
			rpcErr(w, req.ID, -8, "from 非金库")
			return
		}
		f.opSeq++
		opid := fmt.Sprintf("opid-send-%d", f.opSeq)
		f.ops[opid] = &fakeOp{txid: fmt.Sprintf("sendtx-%d", f.opSeq), kind: "send"}
		f.sentFrom = from
		f.sentAddrs = nil
		for _, a := range amounts {
			f.sentAddrs = append(f.sentAddrs, a.Address)
		}
		rpcOK(w, req.ID, opid)

	case "z_getoperationstatus":
		f.polls++
		var ids []string
		_ = json.Unmarshal(req.Params[0], &ids)
		out := []any{}
		for _, id := range ids {
			op, ok := f.ops[id]
			if !ok {
				continue
			}
			op.stage++
			if op.stage >= 2 {
				if op.kind == "send" {
					f.sent++ // success 首次可见即算发出
					op.kind = "send-done"
				}
				if op.kind == "shield" {
					f.shielded++
					op.kind = "shield-done"
				}
				out = append(out, map[string]any{
					"id": id, "status": "success",
					"result": map[string]any{"txid": op.txid},
				})
			} else {
				out = append(out, map[string]any{"id": id, "status": "executing"})
			}
		}
		rpcOK(w, req.ID, out)

	case "z_getoperationresult":
		var ids []string
		_ = json.Unmarshal(req.Params[0], &ids)
		out := []any{}
		for _, id := range ids {
			if op, ok := f.ops[id]; ok && op.stage >= 2 {
				out = append(out, map[string]any{
					"id": id, "status": "success",
					"result": map[string]any{"txid": op.txid},
				})
				delete(f.ops, id)
			}
		}
		rpcOK(w, req.ID, out)

	case "gettransaction":
		rpcOK(w, req.ID, map[string]any{"confirmations": 3})

	case "listunspent":
		// 成熟 coinbase：我们的块距 tip ≥ 100 且尚未 shield
		out := []any{}
		for h := range f.ourBlocks {
			if _, inMain := f.hashByHash[f.ourBlocks[h]]; inMain && f.tip-h >= 100 && f.shielded == 0 {
				out = append(out, map[string]any{"generated": true, "amount": 3.0})
			}
		}
		rpcOK(w, req.ID, out)

	case "z_shieldcoinbase":
		f.opSeq++
		opid := fmt.Sprintf("opid-shield-%d", f.opSeq)
		f.ops[opid] = &fakeOp{txid: fmt.Sprintf("shieldtx-%d", f.opSeq), kind: "shield"}
		rpcOK(w, req.ID, map[string]any{"opid": opid, "shieldingUTXOs": 1})

	default:
		rpcErr(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

func reverse32E2E(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hexRepeatE2E(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
