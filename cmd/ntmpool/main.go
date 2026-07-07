// ntmpool — 通用多币种矿池核心（scashcc/ntmpool，闭源）。
//
// M0 骨架 → M1 单币竖切（stratum/会计/打款/公共 API）→ M2 热管理 + 管理后台。
// 见 docs/03-ROADMAP.md。
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/scashcc/ntmpool/internal/admin"
	"github.com/scashcc/ntmpool/internal/api"
	"github.com/scashcc/ntmpool/internal/banlist"
	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/metrics"
	"github.com/scashcc/ntmpool/internal/minersettings"
	"github.com/scashcc/ntmpool/internal/notify"
	"github.com/scashcc/ntmpool/internal/pgdb"
)

var version = "0.2.0-m2"

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	payouts := flag.Bool("payouts", true, "打款总开关（false = accrue-only，只记账不打款）")
	flag.Parse()

	store, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[boot] 配置加载失败: %v", err)
	}
	cfg := store.Snapshot()

	// 状态目录（bans/miner_settings/config.state/config_audit）
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("[boot] 建状态目录失败: %v", err)
	}
	statePath := filepath.Join(dataDir, "config.state.json")

	// 重启以最后热状态为准（docs/02 §7）：管理后台的热变更覆盖启动配置
	if applied, err := config.LoadAndApplyState(statePath, cfg); err != nil {
		log.Fatalf("[boot] %v", err)
	} else if applied {
		log.Printf("[boot] 已应用热状态 %s（重启以最后热状态为准）", statePath)
	}

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

	// 跨币共享部件：ban 名单 + 矿工设置
	bans, err := banlist.New(filepath.Join(dataDir, "bans.json"))
	if err != nil {
		log.Fatalf("[boot] ban 名单加载失败: %v", err)
	}
	settings, err := minersettings.New(filepath.Join(dataDir, "miner_settings.json"),
		loadOrCreateSalt(filepath.Join(dataDir, "settings.salt")))
	if err != nil {
		log.Fatalf("[boot] 矿工设置加载失败: %v", err)
	}
	// 运营通知（webhook/Telegram；一个不配 = 不通知，Publish 为空操作）
	var sinks []notify.Sink
	if cfg.Notify.WebhookURL != "" {
		sinks = append(sinks, notify.NewWebhookSink(cfg.Notify.WebhookURL))
	}
	if cfg.Notify.TelegramBotToken != "" && cfg.Notify.TelegramChatID != "" {
		sinks = append(sinks, notify.NewTelegramSink(cfg.Notify.TelegramBotToken, cfg.Notify.TelegramChatID))
	}
	hub := notify.NewHub(sinks)
	defer hub.Close()
	if len(sinks) > 0 {
		log.Printf("[boot] 通知已启用: %d 个出口", len(sinks))
	}

	// 共享 Postgres（M4 多实例）：配了 DSN 就连上并迁移 schema，会计/打款批次落库、
	// 重启不丢账、崩溃恢复走真持久化；没配则各币用内存会计（单实例竖切）。
	var db *sql.DB
	var reg *coininstance.InstanceRegistry
	if cfg.PostgresDSN != "" {
		var err error
		db, err = pgdb.Open(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Fatalf("[boot] Postgres 连接失败: %v", err)
		}
		defer db.Close()
		if err := pgdb.Migrate(ctx, db); err != nil {
			log.Fatalf("[boot] Postgres 迁移失败: %v", err)
		}
		instID := cfg.InstanceID
		if instID == "" {
			instID = "pool1"
		}
		reg = coininstance.NewInstanceRegistry(db, instID, version)
		reg.Start(ctx)
		log.Printf("[boot] Postgres 就绪（instance=%s），会计=持久化 + 实例心跳已注册", instID)
	} else {
		log.Printf("[boot] 未配置 postgresDsn，会计=内存（单实例模式）")
	}

	deps := coininstance.Deps{Payouts: *payouts, Ban: bans, Settings: settings, Notify: hub,
		DB: db, Instance: cfg.InstanceID}

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
	controlSnapshot := func() map[string]admin.CoinControl {
		instMu.Lock()
		defer instMu.Unlock()
		out := make(map[string]admin.CoinControl, len(instByID))
		for id, inst := range instByID {
			out[id] = inst
		}
		return out
	}

	// 公共 API（miningcore 形状 + 地址脱敏，internal/api）
	apiSrv := api.New(poolsSnapshot, []byte(cfg.MaskSecret))
	if db != nil {
		apiSrv.SetInstances(func() []api.InstanceInfo {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			list, err := coininstance.ListInstances(ctx, db)
			if err != nil {
				log.Printf("[api] 读实例列表失败: %v", err)
				return nil
			}
			out := make([]api.InstanceInfo, len(list))
			for i, in := range list {
				out[i] = api.InstanceInfo{
					ID: in.ID, Hostname: in.Hostname, Version: in.Version,
					Coins: in.Coins, LastSeen: in.LastSeen, Alive: in.Alive,
				}
			}
			return out
		})
	}
	pub := http.NewServeMux()
	pub.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	pub.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, metrics.Render())
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

	// 管理后台 API（独立端口 + Bearer token；一切热操作入口，internal/admin）
	admSrv := admin.New(version, cfg.InstanceID, cfg.AdminToken, controlSnapshot,
		bans, settings, statePath, filepath.Join(dataDir, "config_audit.jsonl"))
	go func() {
		if cfg.AdminAPI == "" {
			return
		}
		log.Printf("[api] admin API on %s", cfg.AdminAPI)
		if err := http.ListenAndServe(cfg.AdminAPI, admSrv.Handler()); err != nil {
			log.Printf("[api] admin API 退出: %v", err)
		}
	}()

	// 每币拉起独立实例（bitcoin-rpc + stratum1）
	var instances []*coininstance.Instance
	for _, coin := range cfg.Coins {
		if !coin.MiningEnabled {
			log.Printf("[boot] 跳过 %s（mining 未启用）", coin.ID)
			continue
		}
		inst, err := coininstance.Start(ctx, coin, deps)
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
	// 实例心跳带上本机承载的币列表（前端网关据此展示各实例挖什么）
	if reg != nil {
		ids := make([]string, 0, len(instByID))
		instMu.Lock()
		for id := range instByID {
			ids = append(ids, id)
		}
		instMu.Unlock()
		reg.SetCoins(ids)
	}

	<-ctx.Done()
	log.Printf("[boot] 收到退出信号，优雅关停 %d 个币", len(instances))
	for _, inst := range instances {
		inst.Stop()
	}
}

// loadOrCreateSalt 矿工设置密码 hash 的盐：首次启动随机生成并落盘，之后复用
// （必须跨重启稳定，否则矿工的设置密码全部失效）。
func loadOrCreateSalt(path string) []byte {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 16 {
		return b
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		log.Fatalf("[boot] 生成盐失败: %v", err)
	}
	if err := os.WriteFile(path, salt, 0600); err != nil {
		log.Fatalf("[boot] 写盐文件失败: %v", err)
	}
	return salt
}
