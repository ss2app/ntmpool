package e2e

// 假 midstate 节点：官方节点 HTTP RPC 契约的最小忠实实现（docs/07 §2），
// 节点端【真 midvdf 全量 1M 迭代】验块——e2e 铁证与真节点同一把尺。
//
// 契约要点（与真节点逐条对齐）：
//   - /state.height = 下一个块的高度（tip = height−1）
//   - /block_template：coinbase 每输出非零 2 的幂；总额必须 == reward+fees，
//     不符回 4xx {"error":"Coinbase mismatch. Expected: <N>"}（触发池的重建重试）
//   - batch_template 对池是黑盒：池只许替换顶层 "extension"
//   - /submit_batch：extension.final_hash = [32 字节数组]，节点重算 VDF 验证
//   - /block/{h}：返回 batch（e2e 只需 extension 字段——块身份 = final_hash）

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"github.com/scashcc/ntmpool/internal/hasher"
)

const (
	fakeMidReward = uint64(1) << 30 // 官方 block_reward 量级
	fakeMidFees   = uint64(5000)   // 非零 mempool 费 → 必然触发 Expected 重试路径
)

// fakeMidTargetHex diff≈4（顶字节 0x3f）：矿工 ~4 个 VDF 出一个块，e2e 秒级。
const fakeMidTargetHex = "3fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

type fakeMidTemplate struct {
	height   uint64
	expected uint64
	coinbase []map[string]any // 原样回显（value 已验 2 的幂）
}

type fakeMidBlock struct {
	nonce     uint64
	finalHash []byte
}

type fakeMidNode struct {
	srv *httptest.Server
	hsh hasher.Hasher // 真 midvdf（1M 迭代）——节点端共识验证

	mu        sync.Mutex
	height    uint64 // /state.height = 下一个块的高度
	templates map[string]*fakeMidTemplate // mining_midstate hex → 模板
	blocks    map[uint64]*fakeMidBlock    // 已收块（按高度）
	coinbases map[uint64][]map[string]any // 已收块的 coinbase（直付契约断言用）
}

func newFakeMidNode() *fakeMidNode {
	h, err := hasher.Get("midvdf")
	if err != nil {
		panic(err)
	}
	n := &fakeMidNode{
		hsh: h, height: 1000,
		templates: map[string]*fakeMidTemplate{},
		blocks:    map[uint64]*fakeMidBlock{},
		coinbases: map[uint64][]map[string]any{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/state", n.handleState)
	mux.HandleFunc("/block_template", n.handleTemplate)
	mux.HandleFunc("/submit_batch", n.handleSubmit)
	mux.HandleFunc("/block/", n.handleBlock)
	n.srv = httptest.NewServer(mux)
	return n
}

func (n *fakeMidNode) URL() string { return n.srv.URL }
func (n *fakeMidNode) Close()      { n.srv.Close() }

// headerHash 每高度确定性变化（/state 基底指纹的一部分）。
func (n *fakeMidNode) headerHashLocked() string {
	s := sha256.Sum256([]byte(fmt.Sprintf("hh|%d|%d", n.height, len(n.blocks))))
	return hex.EncodeToString(s[:])
}

func (n *fakeMidNode) handleState(w http.ResponseWriter, _ *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"height": n.height, "target": fakeMidTargetHex,
		"block_reward": fakeMidReward, "header_hash": n.headerHashLocked(),
		"is_syncing": false,
	})
}

func fakeMidErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func (n *fakeMidNode) handleTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Coinbase []map[string]any `json:"coinbase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fakeMidErr(w, 400, "bad json")
		return
	}
	var total uint64
	for _, cb := range req.Coinbase {
		v, ok := cb["value"].(float64)
		val := uint64(v)
		addr, _ := cb["address"].(string)
		salt, _ := cb["salt"].(string)
		// 真节点 finish_template 同款校验：非零 2 的幂 + hex64 地址/盐
		if !ok || val == 0 || val&(val-1) != 0 || len(addr) != 64 || len(salt) != 64 {
			fakeMidErr(w, 400, "Invalid coinbase output")
			return
		}
		total += val
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	expected := fakeMidReward + fakeMidFees
	if total != expected {
		// 真节点逐字错误串（池按 "Expected: <N>" 解析重建）
		fakeMidErr(w, 409, fmt.Sprintf("Coinbase mismatch. Expected: %d", expected))
		return
	}
	cbJSON, _ := json.Marshal(req.Coinbase)
	msRaw := sha256.Sum256([]byte(fmt.Sprintf("ms|%d|%s|%s", n.height, n.headerHashLocked(), cbJSON)))
	msHex := hex.EncodeToString(msRaw[:])
	n.templates[msHex] = &fakeMidTemplate{height: n.height, expected: expected, coinbase: req.Coinbase}
	// batch_template 对池是黑盒（池只换顶层 extension）；tpl_id 供假节点回查。
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mining_midstate": msHex,
		"target":          fakeMidTargetHex,
		"batch_template": map[string]any{
			"tpl_id":    msHex,
			"extension": map[string]any{"nonce": 0, "final_hash": make([]int, 32)},
			"coinbase":  req.Coinbase,
		},
		"total_fees":   fakeMidFees,
		"block_reward": fakeMidReward,
	})
}

func (n *fakeMidNode) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var batch struct {
		TplID     string `json:"tpl_id"`
		Extension struct {
			Nonce     uint64    `json:"nonce"`
			FinalHash [32]uint8 `json:"final_hash"`
		} `json:"extension"`
		Coinbase []map[string]any `json:"coinbase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		fakeMidErr(w, 400, "Block rejected: bad batch json")
		return
	}
	n.mu.Lock()
	tpl := n.templates[batch.TplID]
	n.mu.Unlock()
	if tpl == nil {
		fakeMidErr(w, 400, "Block rejected: unknown template")
		return
	}
	// 节点端真 VDF 共识验证（全量 1M 迭代，与池端/矿工同一把尺）
	msBytes, _ := hex.DecodeString(batch.TplID)
	got, err := n.hsh.Hash(hasher.MidSeed(msBytes, batch.Extension.Nonce))
	if err != nil {
		fakeMidErr(w, 400, "Block rejected: vdf error")
		return
	}
	if hex.EncodeToString(got) != hex.EncodeToString(batch.Extension.FinalHash[:]) {
		fakeMidErr(w, 400, "Block rejected: extension hash mismatch")
		return
	}
	tb, _ := hex.DecodeString(fakeMidTargetHex)
	if strings.Compare(string(got), string(tb)) >= 0 {
		fakeMidErr(w, 400, "Block rejected: insufficient pow")
		return
	}
	// coinbase 必须与模板一致（池承诺原样透传）
	a, _ := json.Marshal(batch.Coinbase)
	b, _ := json.Marshal(tpl.coinbase)
	if string(a) != string(b) {
		fakeMidErr(w, 400, "Block rejected: coinbase tampered")
		return
	}
	n.mu.Lock()
	if _, dup := n.blocks[tpl.height]; !dup {
		n.blocks[tpl.height] = &fakeMidBlock{nonce: batch.Extension.Nonce, finalHash: got}
		n.coinbases[tpl.height] = tpl.coinbase
		if tpl.height >= n.height {
			n.height = tpl.height + 1
		}
	}
	n.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
}

func (n *fakeMidNode) handleBlock(w http.ResponseWriter, r *http.Request) {
	hStr := strings.TrimPrefix(r.URL.Path, "/block/")
	h, err := strconv.ParseUint(hStr, 10, 64)
	if err != nil {
		fakeMidErr(w, 400, "bad height")
		return
	}
	n.mu.Lock()
	blk := n.blocks[h]
	n.mu.Unlock()
	if blk == nil {
		fakeMidErr(w, 400, fmt.Sprintf("Block at height %d not found", h))
		return
	}
	fh := make([]int, 32)
	for i, x := range blk.finalHash {
		fh[i] = int(x)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"extension": map[string]any{"nonce": blk.nonce, "final_hash": fh},
	})
}

// advance 推进链高度（堆确认数）。
func (n *fakeMidNode) advance(dh uint64) {
	n.mu.Lock()
	n.height += dh
	n.mu.Unlock()
}

// orphanAll 把所有已收块换成"别人的块"（final_hash 换掉 = 我们的块被主链甩掉）。
func (n *fakeMidNode) orphanAll() {
	n.mu.Lock()
	for h, b := range n.blocks {
		other := sha256.Sum256([]byte(fmt.Sprintf("orphan|%d", h)))
		b.finalHash = other[:]
	}
	n.mu.Unlock()
}

// minerPaidDirect 已收块的 coinbase 里付给 addr 的输出值（直付契约断言）。
func (n *fakeMidNode) minerPaidDirect(addr string) []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []uint64
	for _, cb := range n.coinbases {
		for _, o := range cb {
			if a, _ := o["address"].(string); strings.EqualFold(a, addr) {
				if v, ok := o["value"].(float64); ok {
					out = append(out, uint64(v))
				}
			}
		}
	}
	return out
}
