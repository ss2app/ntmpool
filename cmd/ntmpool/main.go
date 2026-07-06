// ntmpool — 通用多币种矿池核心（scashcc/ntmpool，闭源）。
//
// M0 骨架：配置加载 + 算法金锚自检门禁 + 公共/管理 API 占位 + 优雅退出。
// M1 起接入：stratum 方言、节点适配器、会计、打款（见 docs/03-ROADMAP.md）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/scashcc/ntmpool/internal/api"
	"github.com/scashcc/ntmpool/internal/coininstance"
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

	// 币实例注册表（API 读快照；热添加币后自动可见）
	var instMu sync.Mutex
	instByID := map[string]*coininstance.Instance{}
	poolsSnapshot := func() map[string]api.Pool {
		instMu.Lock()
		defer instMu.Unlock()
		out := make(map[string]api.Pool, len(instByID))
		for id, inst := range instByID {
			out[id] = inst
		}
		return out
	}

	// 公共 API（miningcore 形状 + 地址脱敏，internal/api）
	apiSrv := api.New(poolsSnapshot, []byte(cfg.MaskSecret))
	pub := http.NewServeMux()
	pub.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	pub.Handle("/api/", apiSrv.Handler())
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

	// 每币拉起独立实例（bitcoin-rpc + stratum1，M1 竖切）
	var instances []*coininstance.Instance
	for _, coin := range cfg.Coins {
		if !coin.MiningEnabled {
			log.Printf("[boot] 跳过 %s（mining 未启用）", coin.ID)
			continue
		}
		inst, err := coininstance.Start(ctx, coin, *payouts)
		if err != nil {
			log.Printf("[boot] 启动币 %s 失败: %v", coin.ID, err)
			continue
		}
		instances = append(instances, inst)
		instMu.Lock()
		instByID[coin.ID] = inst
		instMu.Unlock()
		log.Printf("[boot] 币 %s 已启动", coin.ID)
	}
	if len(instances) == 0 {
		log.Printf("[boot] 无可用币实例")
	}

	<-ctx.Done()
	log.Printf("[boot] 收到退出信号，优雅关停 %d 个币", len(instances))
	for _, inst := range instances {
		inst.Stop()
	}
}
