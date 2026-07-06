// ntmpool — 通用多币种矿池核心（scashcc/ntmpool，闭源）。
//
// M0 骨架：配置加载 + 算法金锚自检门禁 + 公共/管理 API 占位 + 优雅退出。
// M1 起接入：stratum 方言、节点适配器、会计、打款（见 docs/03-ROADMAP.md）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/hasher"
)

var version = "0.1.0-m0"

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	payouts := flag.Bool("payouts", true, "打款总开关（false = accrue-only，只记账不打款）")
	flag.Parse()

	store, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[boot] 配置加载失败: %v", err)
	}
	cfg := store.Snapshot()

	// 铁律：所有启用算法先过金锚自检，不过不启动。
	algos := map[string]bool{}
	for _, c := range cfg.Coins {
		if c.Algo != "" {
			algos[c.Algo] = true
		}
	}
	names := make([]string, 0, len(algos))
	for a := range algos {
		names = append(names, a)
	}
	if err := hasher.SelfTestAll(names); err != nil {
		log.Fatalf("[boot] %v", err)
	}
	log.Printf("[boot] ntmpool %s 启动: instance=%s coins=%d payouts=%v",
		version, cfg.InstanceID, len(cfg.Coins), *payouts)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 公共 API（miningcore 形状，M1 填充；地址脱敏见 docs/01 R14.6）
	pub := http.NewServeMux()
	pub.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	pub.HandleFunc("/api/pools", func(w http.ResponseWriter, _ *http.Request) {
		type poolInfo struct {
			ID     string `json:"id"`
			Coin   string `json:"coin"`
			Algo   string `json:"algorithm"`
		}
		out := struct {
			Pools []poolInfo `json:"pools"`
		}{}
		for _, c := range store.Snapshot().Coins {
			out.Pools = append(out.Pools, poolInfo{ID: c.ID, Coin: c.Symbol, Algo: c.Algo})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	go func() {
		if cfg.PublicAPI == "" {
			return
		}
		log.Printf("[api] public API on %s", cfg.PublicAPI)
		if err := http.ListenAndServe(cfg.PublicAPI, pub); err != nil {
			log.Printf("[api] public API 退出: %v", err)
		}
	}()

	// 管理后台 API（独立端口 + token；一切热操作入口，M2 填充）
	adm := http.NewServeMux()
	adm.HandleFunc("/admin/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		if cfg.AdminToken == "" || r.Header.Get("Authorization") != "Bearer "+cfg.AdminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintln(w, "pong")
	})
	go func() {
		if cfg.AdminAPI == "" {
			return
		}
		log.Printf("[api] admin API on %s", cfg.AdminAPI)
		if err := http.ListenAndServe(cfg.AdminAPI, adm); err != nil {
			log.Printf("[api] admin API 退出: %v", err)
		}
	}()

	// TODO(M1): 每币拉起 CoinInstance{adapter, notifier, jobmgr, stratum.Manager, ledger, payout}
	<-ctx.Done()
	log.Printf("[boot] 收到退出信号，优雅关停")
}
