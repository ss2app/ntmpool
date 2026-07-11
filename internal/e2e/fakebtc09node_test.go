package e2e

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// fakeBtc09Node poolnode 形状的 REST 假节点（btc09rpc 适配器的对手方）。
// 与 CN 假节点不同：节点端验块用【真 Argon2id】（golang.org/x/crypto，与
// 池 hasher、真实链共识同一份库）——整条 e2e 是共识真实的字节级契约测试。
type fakeBtc09Node struct {
	srv *httptest.Server

	mu        sync.Mutex
	height    uint64
	templates map[string][]byte // template_id → 88B header（nonce=0）
	curID     string
	curHeight uint64 // curID 对应的模板高度（=当时的 height+1）
	blocks    map[uint64]string // height → block id（sha256d 显示序）
	sent      int
	sentOuts  []map[string]string
	txConf    map[string]int64
}

var btc09Salt = []byte("BTC09/pow/v1")

// btc09PowHash 与链共识/池 hasher 同参数同库（64 MiB, t=1, p=1）。
func btc09PowHash(header []byte) []byte {
	return argon2.IDKey(header, btc09Salt, 1, 64*1024, 1, 32)
}

// btc09NetTarget 假链全网目标 2^254（每 4 个 hash 一个块——e2e 秒出块）。
var btc09NetTarget = new(big.Int).Lsh(big.NewInt(1), 254)

func newFakeBtc09Node() *fakeBtc09Node {
	f := &fakeBtc09Node{
		height:    700,
		templates: map[string][]byte{},
		blocks:    map[uint64]string{},
		txConf:    map[string]int64{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeBtc09Node) URL() string { return f.srv.URL }
func (f *fakeBtc09Node) Close()      { f.srv.Close() }

func (f *fakeBtc09Node) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

func (f *fakeBtc09Node) advance(n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.height += n
}

// orphanAll 把全部已收块从主链甩掉（换成别人的 hash，模拟被 reorg）。
func (f *fakeBtc09Node) orphanAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h := range f.blocks {
		f.blocks[h] = fmt.Sprintf("%064x", h)
	}
}

// currentTemplate 稳定模板：同高度同模板（poolnode JobKey 语义），高度变才换。
func (f *fakeBtc09Node) currentTemplate() (string, []byte, uint64) {
	if f.curID != "" && f.curHeight == f.height+1 {
		return f.curID, f.templates[f.curID], f.curHeight
	}
	hdr := make([]byte, 88)
	binary.LittleEndian.PutUint32(hdr[0:4], 1) // version
	for i := 4; i < 36; i++ {                  // prev：绑高度
		hdr[i] = byte(f.height + uint64(i))
	}
	for i := 36; i < 68; i++ { // merkle：绑高度
		hdr[i] = byte(f.height*3 + uint64(i))
	}
	binary.LittleEndian.PutUint64(hdr[68:76], 1700000000+f.height) // time
	binary.LittleEndian.PutUint32(hdr[76:80], 0x1f00ffff)          // bits（陈列用）
	// nonce [80:88] = 0
	id := sha256.Sum256(hdr)
	tid := hex.EncodeToString(id[:16])
	f.templates[tid] = hdr
	f.curID, f.curHeight = tid, f.height+1
	return tid, hdr, f.curHeight
}

func (f *fakeBtc09Node) writeTemplate(w http.ResponseWriter) {
	f.mu.Lock()
	tid, hdr, height := f.currentTemplate()
	f.mu.Unlock()
	fbWriteJSON(w, 200, map[string]any{
		"template_id": tid,
		"height":      height,
		"header":      hex.EncodeToString(hdr),
		"target":      fmt.Sprintf("%064x", btc09NetTarget),
		"bits":        "1f00ffff",
		"reward":      "50.00000000",
		"prev":        hex.EncodeToString(hdr[4:36]),
		"longpoll_id": tid,
	})
}

func fbWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeBtc09Node) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/status":
		f.mu.Lock()
		h := f.height
		f.mu.Unlock()
		fbWriteJSON(w, 200, map[string]any{"height": h, "tip": fmt.Sprintf("%064x", h), "peers": 3})

	case r.URL.Path == "/mining/template":
		// longpoll：同 id 挂等 → 短睡模拟超时返回（notifier 视同无事、继续挂）
		if lp := r.URL.Query().Get("longpoll_id"); lp != "" {
			f.mu.Lock()
			cur := f.curID
			f.mu.Unlock()
			if lp == cur {
				time.Sleep(150 * time.Millisecond)
			}
		}
		f.writeTemplate(w)

	case r.URL.Path == "/mining/submit":
		var req struct {
			TemplateID string `json:"template_id"`
			Nonce      uint64 `json:"nonce"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		hdr, ok := f.templates[req.TemplateID]
		if !ok {
			f.mu.Unlock()
			fbWriteJSON(w, 200, map[string]any{"status": "rejected", "reason": "unknown template"})
			return
		}
		cand := make([]byte, 88)
		copy(cand, hdr)
		binary.LittleEndian.PutUint64(cand[80:88], req.Nonce)
		f.mu.Unlock()

		// 节点端真验块：真 Argon2id ≤ 全网目标
		pow := btc09PowHash(cand)
		if new(big.Int).SetBytes(pow).Cmp(btc09NetTarget) > 0 {
			fbWriteJSON(w, 200, map[string]any{"status": "rejected", "reason": "proof of work invalid"})
			return
		}
		h1 := sha256.Sum256(cand)
		h2 := sha256.Sum256(h1[:])
		blockID := hex.EncodeToString(h2[:])

		f.mu.Lock()
		f.height++
		f.blocks[f.height] = blockID
		newH := f.height
		f.mu.Unlock()
		fbWriteJSON(w, 200, map[string]any{"status": "accepted", "hash": blockID, "height": newH})

	case r.URL.Path == "/wallet/balance":
		fbWriteJSON(w, 200, map[string]any{"balance": "1000.00000000", "address": "fakePoolAddr"})

	case r.URL.Path == "/wallet/sendmany":
		var req struct {
			Outputs map[string]string `json:"outputs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.sent++
		f.sentOuts = append(f.sentOuts, req.Outputs)
		txid := fmt.Sprintf("%064d", f.sent)
		f.txConf[txid] = 1
		f.mu.Unlock()
		fbWriteJSON(w, 200, map[string]any{"txid": txid})

	case r.URL.Path == "/networkhashps":
		fbWriteJSON(w, 200, map[string]any{"hashps": 5570.0})

	case strings.HasPrefix(r.URL.Path, "/tx/"):
		txid := strings.TrimPrefix(r.URL.Path, "/tx/")
		f.mu.Lock()
		conf, ok := f.txConf[txid]
		f.mu.Unlock()
		if ok {
			fbWriteJSON(w, 200, map[string]any{"confirmations": conf})
		} else {
			fbWriteJSON(w, 200, map[string]any{"confirmations": 0, "mempool": false})
		}

	case strings.HasPrefix(r.URL.Path, "/blocks/"):
		hs := strings.TrimPrefix(r.URL.Path, "/blocks/")
		hq, err := strconv.ParseUint(hs, 10, 64)
		f.mu.Lock()
		hash, found := f.blocks[hq]
		tip := f.height
		f.mu.Unlock()
		if err != nil || !found {
			fbWriteJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
		fbWriteJSON(w, 200, map[string]any{
			"height": hq, "hash": hash, "confirmations": int64(tip-hq) + 1,
		})

	default:
		fbWriteJSON(w, 404, map[string]any{"error": "no route " + r.URL.Path})
	}
}

// ---- 09C 地址工具（base58check v0x09，与节点 core.EncodeAddress 同规则）----

const b58alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func btc09TestAddress(seed byte) string {
	payload := make([]byte, 21)
	payload[0] = 0x09
	for i := 1; i < 21; i++ {
		payload[i] = seed + byte(i)
	}
	c1 := sha256.Sum256(payload)
	c2 := sha256.Sum256(c1[:])
	raw := append(payload, c2[:4]...)

	x := new(big.Int).SetBytes(raw)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for x.Sign() > 0 {
		x.DivMod(x, radix, mod)
		out = append([]byte{b58alpha[mod.Int64()]}, out...)
	}
	for _, b := range raw {
		if b != 0 {
			break
		}
		out = append([]byte{'1'}, out...)
	}
	return string(out)
}
