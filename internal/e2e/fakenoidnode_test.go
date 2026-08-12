//go:build noid

package e2e

// 假 ParanO(1)d (NOID) 节点：官方 HTTP JSON-RPC 2.0（jsonrpsee）契约的最小忠实实现，
// 节点端【真 noidp2b 重算】验 PoW —— e2e 铁证与真节点同一把尺。
//
// 契约要点（逐条对齐 coins/parano1d/REPORT-节点协议与单机测试网.md，别推导）：
//   - Bearer：节点带 --mining-key 时【所有】RPC 必须带 Authorization: Bearer <token>，
//     否则 401（noidrpc 对 401 有专门报错文案）。
//   - ★paranoid_getBlockTemplate 必须收到【一个字符串参数】（coinbase）。传 [] 会被
//     jsonrpsee 判 "Invalid params: No more params"——2026-08-12 实测抓到的真 bug，
//     本假节点照实现，锁死回归（badParams 计数一旦非零即说明客户端又传空参了）。
//   - ★单飞行槽（server.rs:499-679）：模板在手且未消费未过期时，再调 getBlockTemplate
//     直接报 "external mining attempt is already active"。此语义是【故意】实现的——
//     它让 noidrpc 客户端侧单槽缓存成为硬需求：缓存一旦失效，测试立刻炸。
//   - paranoid_submitBlock(template_id, nonce_hex)：真验 PoW（noidp2b.Check 全网 target，
//     官方 le256_lt 严格 <）。达标才收块、推进 tip、消费模板槽；否则报错。
//   - NOID BlockHash = poseidon digest 原样 32B（noidjob 的 intent hash 与之逐字节相同）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/hasher/noidp2b"
)

const (
	// fakeNoidMiningKey 假节点的 --mining-key（= 池 config nodes[0].pass 的 Bearer）。
	fakeNoidMiningKey = "e2e-noid-mining-key"
	// fakeNoidTemplateTTL 官方 EXTERNAL_MINING_TEMPLATE_TTL。
	fakeNoidTemplateTTL = 30 * time.Second
	// fakeNoidGenesisHash 起始 tip（pow_fields 前 32B = parent hash）。
	fakeNoidGenesisHash = "6b874fac7385c0e5f85ebf9b6bc8ea82a080db912042d4a8bc1560e794d3854f"
)

// fakeNoidNetTargetLE 全网 target（256-bit LE，noidp2b.Check 直用）：顶 2 字节为 0
// ⇒ 命中概率 2^-16，e2e 里 ~65536 次 poseidon 即可找到爆块解（cgo 单次 µs 级，亚秒完成）。
// 同时 share target（vardiff diff=256 ⇒ 顶 1 字节为 0，概率 2^-8）严格更宽松，
// 于是「满足 share 但不满足 network」的普通 share 必然存在 —— 这是本 e2e 的核心分岔。
var fakeNoidNetTargetLE = func() [32]byte {
	var t [32]byte
	for i := 0; i < 30; i++ { // t[30] = t[31] = 0
		t[i] = 0xff
	}
	return t
}()

type fakeNoidTemplate struct {
	id        string
	powFields [256]byte
	height    uint64
	issuedAt  time.Time
	consumed  bool
}

type fakeNoidNode struct {
	srv   *httptest.Server
	token string
	// netTarget 本节点的全网 target（256-bit LE）。可配：live 桥接 e2e 要按真锄头
	// 算力挑一个「秒级出块」的难度，与单元 e2e 的固定 2^-16 不同。
	netTarget [32]byte

	mu       sync.Mutex
	height   uint64 // tip 高度
	bestHash string
	cur      *fakeNoidTemplate
	seq      uint64
	blocks   map[uint64]string

	// 计数器（e2e 断言用）
	templateCalls  int // 真正打到节点的 getBlockTemplate 次数（验客户端单槽缓存）
	submitCalls    int // ★上行 submitBlock 次数——普通 share 期间必须恒为 0
	acceptedBlocks int // 通过节点端真 PoW 验证的块数
	badPoWSubmits  int // 提交了但 PoW 不达标（池侧不该让这种上行）
	badParams      int // getBlockTemplate 收到空参（锁死 [""] 坑）
	unauthorized   int // 缺/错 Bearer
}

func newFakeNoidNode() *fakeNoidNode { return newFakeNoidNodeTarget(fakeNoidNetTargetLE) }

// newFakeNoidNodeTarget 指定全网 target 起假节点。
func newFakeNoidNodeTarget(netTarget [32]byte) *fakeNoidNode {
	n := &fakeNoidNode{
		token:     fakeNoidMiningKey,
		netTarget: netTarget,
		height:    190, // 主网开网当天实测量级
		bestHash:  fakeNoidGenesisHash,
		blocks:    map[uint64]string{},
	}
	n.blocks[n.height] = n.bestHash
	n.srv = httptest.NewServer(http.HandlerFunc(n.handle))
	return n
}

func (n *fakeNoidNode) URL() string         { return n.srv.URL }
func (n *fakeNoidNode) Close()              { n.srv.Close() }
func (n *fakeNoidNode) netTargetLE() []byte { t := n.netTarget; return t[:] }

func (n *fakeNoidNode) stats() (templateCalls, submitCalls, accepted, badPoW, badParams, unauth int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.templateCalls, n.submitCalls, n.acceptedBlocks, n.badPoWSubmits, n.badParams, n.unauthorized
}

func (n *fakeNoidNode) submits() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.submitCalls
}

func (n *fakeNoidNode) tip() (uint64, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.height, n.bestHash
}

// ---- JSON-RPC 分发 ----

type fakeNoidReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (n *fakeNoidNode) handle(w http.ResponseWriter, r *http.Request) {
	// Bearer 全局中间件（节点带 --mining-key 时所有 RPC 都要）。
	if n.token != "" {
		authz := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.EqualFold(authz, "Bearer "+n.token) {
			n.mu.Lock()
			n.unauthorized++
			n.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	var req fakeNoidReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		n.writeErr(w, nil, -32700, "Parse error")
		return
	}

	switch req.Method {
	case "paranoid_getChainInfo":
		n.onChainInfo(w, req.ID)
	case "paranoid_getBlockTemplate":
		n.onGetTemplate(w, req.ID, req.Params)
	case "paranoid_submitBlock":
		n.onSubmitBlock(w, req.ID, req.Params)
	case "paranoid_getBlockHash":
		n.onGetBlockHash(w, req.ID, req.Params)
	default:
		n.writeErr(w, req.ID, -32601, "Method not found")
	}
}

func (n *fakeNoidNode) onChainInfo(w http.ResponseWriter, id json.RawMessage) {
	n.mu.Lock()
	h, best := n.height, n.bestHash
	n.mu.Unlock()
	n.writeResult(w, id, map[string]any{
		"height":             h,
		"best_hash":          best,
		"difficulty_target":  hex.EncodeToString(n.netTarget[:]),
		"active_slot_count":  0,
		"log_slots":          24,
		"circulating_supply": "0",
	})
}

// onGetTemplate 实现单飞行槽 + 强制的一个 coinbase 字符串参数。
func (n *fakeNoidNode) onGetTemplate(w http.ResponseWriter, id json.RawMessage, params json.RawMessage) {
	// ★参数校验：必须至少一个参数，且是字符串。传 [] = jsonrpsee "No more params"。
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) < 1 {
		n.mu.Lock()
		n.badParams++
		n.mu.Unlock()
		n.writeErr(w, id, -32602, "Invalid params: No more params")
		return
	}
	var coinbase string
	if err := json.Unmarshal(arr[0], &coinbase); err != nil {
		n.mu.Lock()
		n.badParams++
		n.mu.Unlock()
		n.writeErr(w, id, -32602, "Invalid params: invalid type: expected string")
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// ★单飞行槽：模板在手且未消费未过期 → already active（客户端必须靠自己的缓存挡住）。
	if n.cur != nil && !n.cur.consumed && time.Since(n.cur.issuedAt) < fakeNoidTemplateTTL {
		n.writeErrLocked(w, id, -32000, "external mining attempt is already active")
		return
	}

	n.seq++
	tpl := &fakeNoidTemplate{
		id:       fmt.Sprintf("tpl-h%d-s%d", n.height+1, n.seq),
		height:   n.height + 1,
		issuedAt: time.Now(),
	}
	// pow_fields 布局（16 字段 × 16B LE）：
	//   [0..32)   = parent block hash（矿工/锄头 watchdog 拿它认父块）
	//   [128..160)= miner_address（fields 8..9）—— 这里填确定性占位
	//   [160..176)= nonce 占位（field 10，提交时被覆写）
	//   其余      = 确定性伪随机（每模板不同 → digest 不同 → nonce 搜索不会跨模板复用）
	seed := sha256.Sum256([]byte(tpl.id))
	for off := 0; off < 256; off += 32 {
		copy(tpl.powFields[off:], seed[:])
		seed = sha256.Sum256(seed[:])
	}
	if pb, err := hex.DecodeString(n.bestHash); err == nil && len(pb) == 32 {
		copy(tpl.powFields[:32], pb)
	}
	n.cur = tpl
	n.templateCalls++

	n.writeResultLocked(w, id, map[string]any{
		"template_id":           tpl.id,
		"pow_fields_hex":        hex.EncodeToString(tpl.powFields[:]),
		"nonce_field_index":     10,
		"difficulty_target_hex": hex.EncodeToString(n.netTarget[:]),
		"height":                tpl.height,
		"expires_in_seconds":    int64(fakeNoidTemplateTTL / time.Second),
		"n_txs":                 0,
	})
}

// onSubmitBlock 节点端真验 PoW：只有 digest < 全网 target 才收块。
func (n *fakeNoidNode) onSubmitBlock(w http.ResponseWriter, id json.RawMessage, params json.RawMessage) {
	var arr []string
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) < 2 {
		n.writeErr(w, id, -32602, "Invalid params: expected [template_id, nonce_hex]")
		return
	}
	tid, nonceHex := arr[0], arr[1]
	nonce, err := hex.DecodeString(strings.TrimSpace(nonceHex))
	if err != nil || len(nonce) != noidp2b.NonceWireBytes {
		n.writeErr(w, id, -32602, "nonce must be 16-byte LE hex")
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	n.submitCalls++

	if n.cur == nil || n.cur.id != tid || n.cur.consumed {
		n.writeErrLocked(w, id, -32000, "stale template: unknown or superseded template_id")
		return
	}
	if time.Since(n.cur.issuedAt) >= fakeNoidTemplateTTL {
		n.writeErrLocked(w, id, -32000, "stale template: expired")
		return
	}

	// ★节点端真 PoW 验证（与池侧同一把尺：官方 le256_lt 严格 <）。
	if !noidp2b.Check(n.cur.powFields[:], nonce, n.netTarget[:]) {
		n.badPoWSubmits++
		n.writeErrLocked(w, id, -32000, "invalid proof of work")
		return
	}

	digest := noidp2b.Digest(n.cur.powFields[:], nonce)
	hash := hex.EncodeToString(digest[:])
	n.height = n.cur.height
	n.bestHash = hash
	n.blocks[n.height] = hash
	n.cur.consumed = true
	n.acceptedBlocks++
	n.writeResultLocked(w, id, hash)
}

func (n *fakeNoidNode) onGetBlockHash(w http.ResponseWriter, id json.RawMessage, params json.RawMessage) {
	var arr []uint64
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) < 1 {
		n.writeErr(w, id, -32602, "Invalid params: expected [height]")
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	h, ok := n.blocks[arr[0]]
	if !ok {
		n.writeResultLocked(w, id, nil) // 高度未上链 → null
		return
	}
	n.writeResultLocked(w, id, h)
}

// ---- 响应写出 ----

func (n *fakeNoidNode) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeNoidRPC(w, id, result, nil)
}

func (n *fakeNoidNode) writeErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeNoidRPC(w, id, nil, &fakeNoidErr{Code: code, Message: msg})
}

// *Locked 变体：调用方已持 n.mu（写响应不碰锁保护的字段，只是避免重复加锁）。
func (n *fakeNoidNode) writeResultLocked(w http.ResponseWriter, id json.RawMessage, result any) {
	writeNoidRPC(w, id, result, nil)
}

func (n *fakeNoidNode) writeErrLocked(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeNoidRPC(w, id, nil, &fakeNoidErr{Code: code, Message: msg})
}

type fakeNoidErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeNoidRPC(w http.ResponseWriter, id json.RawMessage, result any, e *fakeNoidErr) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	env := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		env["error"] = e
	} else {
		env["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(env)
}
