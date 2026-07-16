// Command velkarstatic 是 Velkar 内网端到端测试池：连本机 velkar 节点拉合法模板，
// 起 Kaspa EthereumStratum 方言监听公网端口，等真 NTMminer-velkar 锄头连上挖，
// 池端用生产同款 velkarhash.CalculatePow 逐 nonce 重算校验份额。
//
// 目的：验证 C 锄头(NTMminer velkar_hash.c) 与 Go 矿池(velkarhash.go) 的主网 VelkarHash
// （含 stage4 Argon2id）逐字节一致 + Kaspa stratum wire 自洽。低难度、不真爆块。
//
//	VELKAR_NODE=127.0.0.1:26210 VELKAR_LISTEN=0.0.0.0:5512 \
//	VELKAR_PAYADDR=velkartest:qz... VELKAR_CHAIN=velkartest ./velkarstatic
package main

import (
	"context"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/stratum"
	"github.com/scashcc/ntmpool/internal/velkarjob"
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	nodeAddr := envOr("VELKAR_NODE", "127.0.0.1:26210")
	payAddr := envOr("VELKAR_PAYADDR", "velkartest:qzaaslzww48jdzysktamxfphq8gu2lq2gslesq9x0eyngmrjsnr92t40ua0vr")
	listen := envOr("VELKAR_LISTEN", "0.0.0.0:5512")
	chain := envOr("VELKAR_CHAIN", "velkartest")

	node, err := velkarrpc.New("velkar-test", nodeAddr, payAddr)
	if err != nil {
		log.Fatalf("velkarrpc.New(%s): %v", nodeAddr, err)
	}
	defer node.Close()

	jm := velkarjob.New(chain, "velkarhash", node)
	ctx := context.Background()
	if err := jm.Refresh(ctx, true); err != nil {
		log.Fatalf("首次 Refresh（拉模板）: %v", err)
	}

	dialect := stratum.NewKaspaDialect(chain, jm)
	var accepted atomic.Int64
	jm.SetCallbacks(dialect.BroadcastJob, nil, func(_ context.Context, s core.Share) {
		n := accepted.Add(1)
		if n <= 5 || n%50 == 0 {
			log.Printf("[pool] accepted=%d diff=%.3e worker=%s addr=%s", n, s.Difficulty, s.Worker, s.Address)
		}
	})

	// 定时刷新模板（velkar 10s 出块）。
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for range t.C {
			_ = jm.Refresh(ctx, false)
		}
	}()

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("listen %s: %v", listen, err)
	}
	log.Printf("velkar 内网测试池 LISTEN %s  node=%s  chain=%s（低难度，验证 C==Go）", listen, nodeAddr, chain)

	// 低固定难度让 argon2 慢锄头也能秒级出份额；不真爆块。
	port := config.PortConfig{Mode: "pplns", Vardiff: config.VardiffConfig{
		Enabled: false, StartDiff: 1e-8, MinDiff: 1e-10, MaxDiff: 1,
		TargetSeconds: 10, RetargetMinSec: 60,
	}}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		log.Printf("[pool] 矿工连接 %s", conn.RemoteAddr())
		go func(c net.Conn) {
			if err := dialect.Serve(ctx, c, port); err != nil {
				log.Printf("[pool] conn %s 结束: %v", c.RemoteAddr(), err)
			}
		}(conn)
	}
}
