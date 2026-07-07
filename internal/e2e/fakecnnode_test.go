package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"github.com/scashcc/ntmpool/internal/hasher"
)

// testKeyedHasher e2e 用带 key 哈希器：sha256(key||input)。
// 假节点用同一函数验块 → 池端重算与"链共识"同源（模拟铁律②的关系）。
type testKeyedHasher struct{}

func (testKeyedHasher) Name() string { return "rx/test" }
func (testKeyedHasher) HashKeyed(key, input []byte) ([]byte, error) {
	h := sha256.New()
	h.Write(key)
	h.Write(input)
	return h.Sum(nil), nil
}
func (testKeyedHasher) SelfTest() error { return nil }

func init() { hasher.RegisterKeyed(testKeyedHasher{}) }

const cnSeedHex = "aabbccdd00112233445566778899eeff00112233445566778899aabbccddeeff"

// diffBits 1 前导零 bit：约一半 hash 命中，矿工秒出块。
const cnDiffBits = 1

// fakeCNNode zoka 形状 REST 假节点（custom-http 适配器的对手方）：
// 模板/提交（节点端真验块）/块查询/钱包打款，全内存。
type fakeCNNode struct {
	srv *httptest.Server

	mu        sync.Mutex
	height    uint64
	templates map[string][]byte // template_id → blob（nonce 区全零）
	tplSeq    int
	blocks    map[uint64]string // height → hash
	byHash    map[string]uint64
	sent      int
	txConf    map[string]int64
}

func newFakeCNNode() *fakeCNNode {
	f := &fakeCNNode{
		height:    500,
		templates: map[string][]byte{},
		blocks:    map[uint64]string{},
		byHash:    map[string]uint64{},
		txConf:    map[string]int64{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeCNNode) URL() string { return f.srv.URL }
func (f *fakeCNNode) Close()      { f.srv.Close() }

func (f *fakeCNNode) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

// advance 推高链高度（制造成熟确认数）。
func (f *fakeCNNode) advance(n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.height += n
}

func (f *fakeCNNode) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writeJSON := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}

	switch r.URL.Path {
	case "/chain/height":
		writeJSON(200, map[string]any{"height": f.height})

	case "/mining/template":
		f.tplSeq++
		tid := fmt.Sprintf("tpl-%d", f.tplSeq)
		blob := make([]byte, 80)
		// blob 内容绑高度（高度变 = blob 变 = 池发新 job）
		for i := range blob {
			blob[i] = byte(f.height + uint64(i))
		}
		for i := 72; i < 80; i++ {
			blob[i] = 0 // nonce 区全零
		}
		f.templates[tid] = blob
		writeJSON(200, map[string]any{
			"height": f.height + 1, "prev_hash": fmt.Sprintf("%064x", f.height),
			"difficulty_bits": cnDiffBits, "epoch_seed_hex": cnSeedHex,
			"blob_prefix_hex": hex.EncodeToString(blob), "template_id": tid,
			"reward_atoms": uint64(5000000000), // 50.00000000（8 位小数）
		})

	case "/mining/submit":
		var req struct {
			TemplateID string `json:"template_id"`
			Nonce      uint64 `json:"nonce"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		blob, ok := f.templates[req.TemplateID]
		if !ok {
			writeJSON(200, map[string]any{"status": "rejected", "reason": "unknown template"})
			return
		}
		// 节点端真验块：写 nonce → 同源哈希 → leading-zero-bits 检查
		cand := make([]byte, len(blob))
		copy(cand, blob)
		for i := 0; i < 8; i++ {
			cand[72+i] = byte(req.Nonce >> (8 * i))
		}
		key, _ := hex.DecodeString(cnSeedHex)
		h, _ := testKeyedHasher{}.HashKeyed(key, cand)
		target := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256-cnDiffBits), big.NewInt(1))
		if new(big.Int).SetBytes(h).Cmp(target) > 0 {
			writeJSON(200, map[string]any{"status": "rejected", "reason": "bad pow"})
			return
		}
		f.height++
		blockHash := hex.EncodeToString(h)
		f.blocks[f.height] = blockHash
		f.byHash[blockHash] = f.height
		writeJSON(200, map[string]any{"status": "accepted", "hash": blockHash, "height": f.height})

	case "/wallet/balance":
		writeJSON(200, map[string]any{"balance_atoms": uint64(1000000000000)})

	case "/wallet/sendmany":
		f.sent++
		txid := fmt.Sprintf("cntx-%d", f.sent)
		f.txConf[txid] = 1
		writeJSON(200, map[string]any{"txid": txid})

	case "/wallet/tx":
		txid := r.URL.Query().Get("txid")
		if conf, ok := f.txConf[txid]; ok {
			writeJSON(200, map[string]any{"confirmations": conf})
		} else {
			writeJSON(404, map[string]any{"error": "not found"})
		}

	default:
		// GET /blocks/{height}（zoka 真实路由形状）
		if h, ok := strings.CutPrefix(r.URL.Path, "/blocks/"); ok {
			hq, err := strconv.ParseUint(h, 10, 64)
			if err == nil {
				if hash, found := f.blocks[hq]; found {
					writeJSON(200, map[string]any{"hash": hash, "height": hq, "reward_atoms": uint64(5000000000)})
					return
				}
			}
			writeJSON(404, map[string]any{"error": "not found"})
			return
		}
		writeJSON(404, map[string]any{"error": "no route " + r.URL.Path})
	}
}

// orphanAll 把全部已收块从主链甩掉（换成别人的 hash，模拟被 reorg）。
func (f *fakeCNNode) orphanAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h := range f.blocks {
		old := f.blocks[h]
		delete(f.byHash, old)
		f.blocks[h] = fmt.Sprintf("%064x", h) // 别人的 hash
	}
}
