package velkarjob

// 端到端 live 冒烟（默认 skip，联调开）：真 velkard 模板 → 完整 Kaspa stratum 握手 →
// Go 测试矿工暴力找低难度份额 → 池端重算接受。证 wire 协议 + velkarjob + adapter + VelkarHash
// 全链路自洽（矿工用 job 里下发的 block target 算 stage4，与池端重算逐字节一致）。
//
//	VELKAR_NODE=127.0.0.1:26210 VELKAR_PAYADDR=velkartest:qz... \
//	go test -run TestLiveStratumMine -v ./internal/velkarjob/
//
// ⚠ 用极低池难度（StartDiff=1e-8 ≈ 数十次哈希/份额）让纯 Go VelkarHash 秒级出份额。

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher/velkarhash"
	"github.com/scashcc/ntmpool/internal/stratum"
)

func TestLiveStratumMine(t *testing.T) {
	addr := os.Getenv("VELKAR_NODE")
	if addr == "" {
		t.Skip("设 VELKAR_NODE=host:port 开启真节点端到端冒烟")
	}
	node, err := velkarrpc.New("velkar-live", addr, os.Getenv("VELKAR_PAYADDR"))
	if err != nil {
		t.Fatalf("velkarrpc.New: %v", err)
	}
	defer node.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	jm := New("velkartest", "velkarhash", node)
	if err := jm.Refresh(ctx, true); err != nil {
		t.Fatalf("Refresh（拉真模板）: %v", err)
	}
	dialect := stratum.NewKaspaDialect("velkartest", jm)
	acceptedCh := make(chan core.Share, 4)
	jm.SetCallbacks(dialect.BroadcastJob, nil, func(_ context.Context, s core.Share) {
		select {
		case acceptedCh <- s:
		default:
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := config.PortConfig{Mode: "pplns", Vardiff: config.VardiffConfig{
		Enabled: false, StartDiff: 1e-8, MinDiff: 1e-10, MaxDiff: 1,
		TargetSeconds: 10, RetargetMinSec: 60,
	}}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = dialect.Serve(ctx, conn, port)
	}()

	mc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer mc.Close()
	r := bufio.NewReader(mc)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = mc.Write(append(b, '\n'))
	}
	send(map[string]any{"id": 1, "method": "mining.subscribe", "params": []any{"ntmtest/1.0"}})
	send(map[string]any{"id": 2, "method": "mining.authorize", "params": []any{"velkartest:qz.rig1", "x"}})

	// 读到 mining.notify → 解 job（144hex = prePow32 + tsLE8 + blockTargetLE32）
	var prePow, tgtLE []byte
	var ts uint64
	var jobID string
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		_ = mc.SetReadDeadline(time.Now().Add(25 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("读 stratum: %v", err)
		}
		var msg struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil || msg.Method != "mining.notify" {
			continue
		}
		_ = json.Unmarshal(msg.Params[0], &jobID)
		var jobData string
		_ = json.Unmarshal(msg.Params[1], &jobData)
		raw, err := hex.DecodeString(jobData)
		if err != nil || len(raw) != 72 {
			t.Fatalf("job 数据非 144hex(72B): len=%d err=%v", len(raw), err)
		}
		prePow = raw[0:32]
		ts = binary.LittleEndian.Uint64(raw[32:40])
		tgtLE = raw[40:72]
		break
	}
	if prePow == nil {
		t.Fatalf("未收到 mining.notify")
	}
	t.Logf("收到 job=%s prePow=%s… ts=%d", jobID, hex.EncodeToString(prePow)[:16], ts)

	// Go 测试矿工：暴力找 shareDiff >= 池难度(1e-8) 的 nonce（用 job 下发的 block target 算 stage4）
	const poolDiff = 1e-8
	var win uint64
	found := false
	for n := uint64(1); n < 5_000_000; n++ {
		powLE, err := velkarhash.CalculatePow(prePow, ts, n, tgtLE)
		if err != nil {
			t.Fatalf("CalculatePow: %v", err)
		}
		powBE := new(big.Int).SetBytes(reverseBytes(powLE))
		if shareDiffFromPow(powBE) >= poolDiff {
			win = n
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("500 万 nonce 内未找到 1e-8 份额（VelkarHash 有错？）")
	}
	t.Logf("矿工找到份额 nonce=%016x", win)

	send(map[string]any{"id": 3, "method": "mining.submit", "params": []any{"velkartest:qz.rig1", jobID, fmt.Sprintf("%016x", win)}})

	select {
	case s := <-acceptedCh:
		t.Logf("✓✓ 池接受份额：addr=%s worker=%s diff=%.3e（wire+velkarjob+VelkarHash 两端自洽）",
			s.Address, s.Worker, s.Difficulty)
	case <-time.After(15 * time.Second):
		t.Fatalf("池未在 15s 内接受份额（两端算法不一致？）")
	}
}
