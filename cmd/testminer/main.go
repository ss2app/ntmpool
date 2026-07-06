// testminer — 极简 stratum V1 CPU 矿工（sha256d），复用 btcwork。
// 用途：pool-core 的 regtest 端到端验收 + CI 集成测试（不依赖外部 cpuminer）。
//
// 用法：testminer -o host:port -u 地址.矿工名 [-p x] [-n 秒数]
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/btcwork"
)

func main() {
	url := flag.String("o", "127.0.0.1:3401", "stratum host:port")
	user := flag.String("u", "addr.rig", "钱包地址.矿工名")
	pass := flag.String("p", "x", "密码")
	secs := flag.Int("n", 30, "运行秒数（0=一直）")
	flag.Parse()

	conn, err := net.Dial("tcp", *url)
	if err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	m := &miner{conn: conn, w: bufio.NewWriter(conn), br: bufio.NewReader(conn), user: *user}

	m.send(1, "mining.subscribe", []any{"testminer/1.0"})
	m.send(2, "mining.authorize", []any{*user, *pass})

	deadline := time.Time{}
	if *secs > 0 {
		deadline = time.Now().Add(time.Duration(*secs) * time.Second)
	}
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Printf("到时退出: accepted=%d rejected=%d blocks=%d", m.accepted, m.rejected, m.blocks)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := m.br.ReadString('\n')
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				m.grind() // 无新消息时挖一会当前 job
				continue
			}
			log.Printf("读断开: %v (accepted=%d blocks=%d)", err, m.accepted, m.blocks)
			return
		}
		m.handle(line)
		m.grind()
	}
}

type miner struct {
	conn net.Conn
	w    *bufio.Writer
	br   *bufio.Reader
	user string

	en1      []byte
	en2Size  int
	target   *btcwork.BigTarget
	diff     float64

	// 当前 job
	jobID     string
	prevInt   []byte
	coinb1    []byte
	coinb2    []byte
	branch    [][]byte
	version   uint32
	bits      uint32
	ntime     uint32
	haveJob   bool

	en2Ctr   uint32
	accepted int
	rejected int
	blocks   int
	subID    int
}

func (m *miner) send(id int, method string, params []any) {
	pb, _ := json.Marshal(params)
	msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(pb)})
	_, _ = m.w.Write(append(msg, '\n'))
	_ = m.w.Flush()
}

func (m *miner) handle(line string) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(line), &msg) != nil {
		return
	}
	switch {
	case msg.Method == "mining.set_difficulty":
		var p []float64
		if json.Unmarshal(msg.Params, &p) == nil && len(p) > 0 {
			m.diff = p[0]
			m.target = btcwork.NewBigTarget(btcwork.DiffToTarget(p[0]))
		}
	case msg.Method == "mining.notify":
		m.onNotify(msg.Params)
	case len(msg.Result) > 0 && msg.Method == "":
		// submit / subscribe 响应
		m.onResponse(msg.ID, msg.Result, msg.Error)
	}
}

func (m *miner) onResponse(id json.RawMessage, result, errRaw json.RawMessage) {
	var idNum int
	_ = json.Unmarshal(id, &idNum)
	if idNum == 1 {
		// subscribe: [subs, en1, en2size]
		var arr []json.RawMessage
		if json.Unmarshal(result, &arr) == nil && len(arr) >= 3 {
			var en1 string
			_ = json.Unmarshal(arr[1], &en1)
			m.en1, _ = hex.DecodeString(en1)
			_ = json.Unmarshal(arr[2], &m.en2Size)
		}
		return
	}
	// submit 响应
	if idNum >= 100 {
		var ok bool
		if json.Unmarshal(result, &ok) == nil && ok {
			m.accepted++
		} else if string(errRaw) != "null" && len(errRaw) > 0 {
			m.rejected++
		}
	}
}

func (m *miner) onNotify(params json.RawMessage) {
	var p []json.RawMessage
	if json.Unmarshal(params, &p) != nil || len(p) < 9 {
		return
	}
	str := func(i int) string { var s string; _ = json.Unmarshal(p[i], &s); return s }
	m.jobID = str(0)
	m.prevInt, _ = btcwork.PrevHashInternalFromStratum(str(1))
	m.coinb1, _ = hex.DecodeString(str(2))
	m.coinb2, _ = hex.DecodeString(str(3))
	var branchHex []string
	_ = json.Unmarshal(p[4], &branchHex)
	m.branch = nil
	for _, b := range branchHex {
		bb, _ := hex.DecodeString(b)
		m.branch = append(m.branch, bb)
	}
	m.version = parseU32(str(5))
	m.bits = parseU32(str(6))
	m.ntime = parseU32(str(7))
	m.haveJob = true
}

// grind 对当前 job 试若干 nonce（每次调用一小批，保持消息循环响应）。
func (m *miner) grind() {
	if !m.haveJob || m.target == nil || len(m.en1) == 0 {
		return
	}
	en2 := make([]byte, m.en2Size)
	putU32(en2, m.en2Ctr)
	m.en2Ctr++

	// coinbase = coinb1 || en1 || en2 || coinb2 → txid
	cb := make([]byte, 0, len(m.coinb1)+len(m.en1)+len(en2)+len(m.coinb2))
	cb = append(cb, m.coinb1...)
	cb = append(cb, m.en1...)
	cb = append(cb, en2...)
	cb = append(cb, m.coinb2...)
	cbTxid := btcwork.DoubleSHA(cb)
	root := btcwork.MerkleRootFromBranch(cbTxid, m.branch)

	const batch = 200000
	for i := uint32(0); i < batch; i++ {
		nonce := i
		header := btcwork.SerializeHeader(m.version, m.prevInt, root, m.ntime, m.bits, nonce)
		h := btcwork.HeaderHash(header)
		if m.target.Meets(h) {
			m.subID++
			id := 100 + m.subID
			m.send(id, "mining.submit", []any{
				m.user, m.jobID, hex.EncodeToString(en2),
				fmt.Sprintf("%08x", m.ntime), fmt.Sprintf("%08x", nonce),
			})
			if btcwork.ShareDiff(h) > m.diff*1000 {
				m.blocks++ // 可能爆块（难度远超份额难度）
			}
			return
		}
	}
}

func parseU32(s string) uint32 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 32)
	return uint32(v)
}
func putU32(b []byte, v uint32) {
	if len(b) >= 4 {
		b[0] = byte(v >> 24)
		b[1] = byte(v >> 16)
		b[2] = byte(v >> 8)
		b[3] = byte(v)
	}
}
