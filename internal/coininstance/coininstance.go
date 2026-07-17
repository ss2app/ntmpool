// Package coininstance 把一个币的所有部件拼装并运行：
// 节点适配器 × 算法 × JobManager × stratum 端口 × 会计 × 打款。
// 每币一个独立生命周期（可运行时增删，不影响其他币）——这是 NTMPool 对 miningcore
// 「加币要重启全进程」短板的核心改进。
package coininstance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/banlist"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hashrate"
	"github.com/scashcc/ntmpool/internal/minersettings"
	"github.com/scashcc/ntmpool/internal/notify"
	"github.com/scashcc/ntmpool/internal/payout"
	"github.com/scashcc/ntmpool/internal/stratum"
	"github.com/scashcc/ntmpool/internal/zmqsub"
)

const extraNonce2Size = 4

// Deps 跨币共享的依赖（全部可为 nil = 不启用）。
type Deps struct {
	Payouts  bool                 // 打款总开关（-payouts 命令行）
	Ban      *banlist.List        // 全池共享 ban 名单
	Settings *minersettings.Store // 矿工设置（mp=/密码绑定）
	Notify   *notify.Hub          // 运营通知（爆块/打款/孤块/节点失联/对账冻结）
	DB       *sql.DB              // 共享 Postgres（多实例，M4）；nil = 内存会计（单实例）
	Instance string               // 本实例 ID（shares.source 溯源；多服务器横向扩展）
}

// statusSource 节点健康检查面（失联检测/高度轮询）。
type statusSource interface {
	Status(ctx context.Context) (adapter.ChainStatus, error)
}

// jobPipe 各链家族作业管理器的公共面（jobmanager=GBT 系 / cnjob=blob 系）。
type jobPipe interface {
	Refresh(ctx context.Context, clean bool) error
	Snapshot() (height uint64, netDiff float64, ok bool)
}

// authHookable 支持矿工设置钩子的方言（V1/CN 都实现）。
type authHookable interface {
	SetAuthHook(func(addr, worker string, p minersettings.PasswordParams))
}

// autoBannable 支持协议层自动 ban 的方言（V1/CN 都实现）。
type autoBannable interface {
	SetAutoBan(stratum.Banner)
}

// familyParts 一个链家族（bitcoin GBT / blob CN 系）拼装出的全部部件。
// 家族构建器见 family_bitcoin.go / family_blob.go —— M3 选型化的核心：
// coininstance 只认这些面，节点形态/方言/算法在构建器里按 cfg.Adapter 绑定。
type familyParts struct {
	status     statusSource
	hashps     adapter.HashPSSource // nil = 链无全网算力真值口径（API 省略，绝不反推）
	classifier payout.NodeClassifier
	wallet     adapter.WalletAdapter
	jobs       jobPipe
	dialects   map[string]stratum.Dialect
	connCount  func() int
	notifiers  []adapter.Notifier // 新块推送通道（longpoll/ZMQ/…）；轮询兜底永远另行保留
}

// Instance 一个运行中的币。
type Instance struct {
	cfg     config.CoinConfig
	parts   *familyParts
	ports   *stratum.Manager
	ledger  accounting.Ledger
	engine  *payout.Engine
	tracker *hashrate.Tracker
	batches payout.BatchStore
	deps    Deps

	ctx    context.Context // 实例生命周期（热加端口用）
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex // 保护 cfg 热改 + 网络缓存
	lastHeight uint64
	netHashPS  float64 // getnetworkhashps 缓存（30s 刷新；0=尚未取到）
	netHashAt  time.Time
	nodeFails  int  // 节点连续失败计数（失联检测）
	nodeDown   bool // 当前是否处于失联状态（翻转时才发通知，不刷屏）
}

// nodeDownThreshold 连续多少次 Status 失败判定节点失联（2s 轮询 ≈ 10s）。
const nodeDownThreshold = 5

// notifyEvent 事件上报（Hub 未配置 = 空操作；Publish 非阻塞）。
func (inst *Instance) notifyEvent(kind, title string, fields map[string]string) {
	if inst.deps.Notify == nil {
		return
	}
	inst.deps.Notify.Publish(notify.Event{
		Kind: kind, Coin: inst.cfg.ID, Title: title, Fields: fields, At: time.Now(),
	})
}

// Start 拼装并启动该币：按 cfg.Adapter 选链家族（M3 选型化），
// 家族构建器绑定 节点适配器 × 算法 × 作业管理器 × 方言，其余部件全家族共用。
func Start(parent context.Context, cfg config.CoinConfig, deps Deps) (*Instance, error) {
	if len(cfg.Nodes) == 0 {
		return nil, errf("[%s] 未配置节点", cfg.ID)
	}

	ctx, cancel := context.WithCancel(parent)
	inst := &Instance{cfg: cfg, deps: deps, ctx: ctx, cancel: cancel}

	// 会计 + 算力采样（算力窗口与 PPLNS 窗口分开，pitfall C6）
	decimals := cfg.Decimals
	if decimals <= 0 {
		decimals = 8
	}
	// 会计层：有共享 Postgres（M4 多实例）走 PGLedger/PGBatchStore（重启不丢账、
	// 崩溃恢复走真持久化）；否则内存版（单实例，M1 竖切够用）。
	if deps.DB != nil {
		pg := accounting.NewPGLedger(deps.DB, cfg.ID, decimals, cfg.Payout.PplnsFactor, deps.Instance)
		pg.StartFlusher(ctx, time.Second) // share 每秒批量落盘（爆块/confirm 前会强制同步 flush）
		inst.ledger = pg
		inst.batches = payout.NewPGBatchStore(deps.DB, cfg.ID)
		log.Printf("[%s] 会计=Postgres（instance=%s）", cfg.ID, deps.Instance)
	} else {
		inst.ledger = accounting.NewMemLedger(decimals, cfg.Payout.PplnsFactor)
		inst.batches = payout.NewMemBatchStore()
	}
	// 算力口径按家族：bitcoin 系难度 1 = 2^32 哈希（默认）；
	// blob/CN 系难度本身就是期望哈希数（multiplier=1）。zoka live 冒烟实测抓出的坑：
	// 用 2^32 口径会把 1.2 KH/s 显成 964 GH/s（矿池网页只放真实数据铁律）。
	hrCfg := hashrate.Config{}
	if cfg.Adapter == "custom-http" || cfg.Adapter == "cryptonote-rpc" || cfg.Adapter == "dragonx-rpc" ||
		cfg.Adapter == "brisvia-rpc" || cfg.Adapter == "noctari-rpc" || cfg.Adapter == "midstate-rpc" {
		// midstate：难度=期望 VDF 次数（连续标尺），算力单位 ext/s（2026-07-11
		// 生产实测坑：漏加这行 → 34K ext/s 被 2^32 口径显成 168 TH/s，zoka 同款）
		hrCfg.Multiplier = 1
	}
	if cfg.Adapter == "btc09-http" {
		// 09C 难度标尺 = maxTarget(0x1f00ffff)：难度 1 = 2^32/0xffff ≈ 65537 哈希
		hrCfg.Multiplier = float64(1<<32) / float64(0xffff)
	}
	inst.tracker = hashrate.New(hrCfg)
	// PG 模式：重启曲线恢复（必须在端口收 share 前做完，避免与实时 Record 重叠计数）。
	// 两级：①minerstats 快照种子回灌（O(矿工×144) 恒定，与 shares 保留策略解耦）
	//      ②shares 回放补「种子之后的增量段」+「即时窗口段」（真实明细非估算）。
	// 首次部署 minerstats 为空 → 自动退回全量 24h shares 回放（原 9b24044 行为）。
	if deps.DB != nil {
		now := time.Now()
		since := now.Add(-inst.tracker.Retain())
		bucketsFrom := since // 默认全量回放（无种子）
		if last, n, err := seedFromStats(ctx, deps.DB, cfg.ID, inst.tracker); err != nil {
			log.Printf("[%s] 算力快照种子回灌失败（退回全量 shares 回放）: %v", cfg.ID, err)
		} else if n > 0 {
			// 种子桶覆盖到 last+bucket；shares 回放桶段从这之后开始（避免双计），
			// 但即时窗口（默认 10min）要完整 → since 取两者更早者，早段只灌即时窗口。
			bucketsFrom = last.Add(inst.tracker.BucketSize())
			since = bucketsFrom
			if w := now.Add(-inst.tracker.Window()); w.Before(since) {
				since = w
			}
			log.Printf("[%s] 算力曲线已从 minerstats 种子回灌：24h 内 %d 行快照", cfg.ID, n)
		}
		if n, err := replayShareHistory(ctx, deps.DB, cfg.ID, inst.tracker, since, bucketsFrom); err != nil {
			log.Printf("[%s] 算力曲线回放失败（不影响运行，曲线随新 share 重新积累）: %v", cfg.ID, err)
		} else if n > 0 {
			log.Printf("[%s] 算力曲线已从 PG 回放补齐：%d 条 share", cfg.ID, n)
		}
	}

	// 链家族选型：节点适配器 × 方言 × 作业管理器
	parts, err := buildFamily(ctx, cfg, decimals, inst)
	if err != nil {
		cancel()
		return nil, err
	}
	// ZMQ 新块通知（家族无关，配置驱动）：nodes[0].zmq 非空即挂 hashblock 订阅
	if z := cfg.Nodes[0].ZMQ; z != "" {
		parts.notifiers = append(parts.notifiers, &zmqsub.Notifier{
			Coin: cfg.ID, Endpoint: z, Topic: "hashblock",
		})
	}
	inst.parts = parts

	// 打款引擎（家族无关：只吃 NodeClassifier + WalletAdapter）
	pcfg := payout.Config{
		Coin: cfg.ID, Decimals: decimals,
		FeePercent: cfg.Payout.FeePercent, MinPayout: parseFloat(cfg.Payout.MinPayout),
		SoloFeePercent:    cfg.Payout.SoloFeePercent,
		Maturity:          cfg.Payout.Confirmations,
		ChainAuditEnabled: resolveChainAudit(cfg.Payout.ChainAudit, deps.DB != nil),
		FeeAddress:        cfg.FeeAddress,
		FeeCollectEnabled: cfg.Payout.FeeCollect.Enabled,
		FeeCollectMin:     cfg.Payout.FeeCollect.MinAmount,
	}
	inst.engine = payout.NewEngine(pcfg, inst.ledger, parts.classifier, parts.wallet, inst.batches)
	inst.engine.SetEnabled(deps.Payouts && cfg.Payout.Enabled)
	inst.engine.SetEvents(inst.notifyEvent)
	// 钱包整备（隐私链 shield/merge）：钱包适配器实现 WalletMaintainer 即接线
	if m, ok := parts.wallet.(adapter.WalletMaintainer); ok {
		inst.engine.SetMaintainer(m)
		log.Printf("[%s] 钱包整备器已接线（打款前 shield/merge）", cfg.ID)
	}
	if deps.Settings != nil {
		coinID := cfg.ID
		inst.engine.SetMinPayoutOverrides(func() map[string]float64 {
			return deps.Settings.MinPayouts(coinID)
		})
	}

	// 矿工设置钩子（V1/CN 方言同款）
	if deps.Settings != nil {
		coinID, defMin := cfg.ID, parseFloat(cfg.Payout.MinPayout)
		hook := func(addr, worker string, p minersettings.PasswordParams) {
			note, err := deps.Settings.ApplyPassword(coinID, addr, p, defMin)
			if err != nil {
				log.Printf("[%s] 矿工 %s.%s 密码设置被拒: %v", coinID, short(addr), worker, err)
			} else if note != "" {
				log.Printf("[%s] 矿工 %s.%s 设置: %s", coinID, short(addr), worker, note)
			}
		}
		for _, d := range parts.dialects {
			if ah, ok := d.(authHookable); ok {
				ah.SetAuthHook(hook)
			}
		}
	}

	// 协议层自动 ban（badpow/malformed/dup 占比超阈值 → 指数退避 ban IP）
	if deps.Ban != nil {
		for _, d := range parts.dialects {
			if ab, ok := d.(autoBannable); ok {
				ab.SetAutoBan(deps.Ban)
			}
		}
	}

	// 端口（每币可多端口，热管理）
	inst.ports = stratum.NewManager(cfg.ID, parts.dialects)
	if deps.Ban != nil {
		inst.ports.SetBanChecker(deps.Ban)
	}
	inst.ports.SetAcceptNew(cfg.NewConnsEnabled)
	for _, p := range cfg.Ports {
		if !p.Enabled {
			continue
		}
		if err := inst.ports.StartPort(ctx, p); err != nil {
			cancel()
			return nil, err
		}
		log.Printf("[%s] stratum 端口 %d (%s/%s) 已监听", cfg.ID, p.Port, p.Mode, p.Dialect)
	}

	// 首次拉模板
	if err := inst.parts.jobs.Refresh(ctx, true); err != nil {
		log.Printf("[%s] 首次模板刷新失败（将在循环中重试）: %v", cfg.ID, err)
	}

	inst.startLoops(ctx)
	// 算力桶持久化（poolstats/minerstats 快照，桶翻转写库）——parts 已装配，
	// Network() 可用；PG 模式才有落点。
	if deps.DB != nil {
		inst.wg.Add(1)
		go func() {
			defer inst.wg.Done()
			inst.runStatsPersist(ctx, deps.DB)
		}()
	}
	// 恢复扫描（崩溃恢复；内存实现无持久化，Postgres 实现时真正生效）
	_ = inst.engine.Recover(ctx)
	return inst, nil
}

// resolveChainAudit 保留三态配置：显式 true/false 优先；auto 时持久 store 开启，
// volatile mem store 保持 nil，让 Engine 以 reason=volatile-store 明确记录安全禁用。
func resolveChainAudit(configured *bool, persistent bool) *bool {
	if configured != nil || !persistent {
		return configured
	}
	enabled := true
	return &enabled
}

func (inst *Instance) startLoops(ctx context.Context) {
	// 模板刷新循环（轮询兜底——推送断了池不能瞎，docs/02 铁律，永远保留）。
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				inst.refreshOnce(ctx)
			}
		}
	}()

	// 新块推送通道（longpoll/ZMQ 并存，取最先到者）：事件到手即 force 刷模板，
	// 消掉轮询的秒级滞后（孤块主因之一）。多通道同一新块按 (Height,Hash) 去重；
	// 漏网的重复事件由 cnjob JobKey 同工作判定兜底，只多一次幂等 GBT，不打断矿工。
	if len(inst.parts.notifiers) > 0 {
		tips := make(chan core.TipEvent, 16)
		for _, n := range inst.parts.notifiers {
			n := n
			inst.wg.Add(1)
			go func() {
				defer inst.wg.Done()
				_ = n.Run(ctx, tips)
			}()
		}
		inst.wg.Add(1)
		go func() {
			defer inst.wg.Done()
			var lastKey string
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-tips:
					key := fmt.Sprintf("%d|%s", ev.Height, ev.Hash)
					if key == lastKey {
						continue
					}
					lastKey = key
					if err := inst.parts.jobs.Refresh(ctx, true); err != nil {
						log.Printf("[%s] %s 事件刷模板失败: %v", inst.cfg.ID, ev.Source, err)
						continue
					}
					log.Printf("[%s] 新块推送(%s) height=%d → 模板已即时刷新", inst.cfg.ID, ev.Source, ev.Height)
				}
			}
		}()
		log.Printf("[%s] 新块推送通道已接线 ×%d（轮询兜底保留）", inst.cfg.ID, len(inst.parts.notifiers))
	}

	// 打款循环
	interval := time.Duration(inst.cfg.Payout.IntervalSec) * time.Second
	if interval < 10*time.Second {
		interval = 60 * time.Second
	}
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := inst.engine.RunOnce(ctx); err != nil {
					log.Printf("[%s] 打款周期错误: %v", inst.cfg.ID, err)
				}
			}
		}
	}()
}

// refreshOnce 拉模板；高度变化 = 新块，clean 广播；否则静默刷新（新 mempool/时间戳）。
// 顺带每 30s 刷一次 getnetworkhashps 缓存（API 读缓存，不因请求打 RPC）+
// 节点失联检测（连续失败 nodeDownThreshold 次翻转 down，恢复翻转 up；翻转才通知）。
func (inst *Instance) refreshOnce(ctx context.Context) {
	st, err := inst.parts.status.Status(ctx)
	if err != nil {
		inst.mu.Lock()
		inst.nodeFails++
		flip := inst.nodeFails == nodeDownThreshold && !inst.nodeDown
		if flip {
			inst.nodeDown = true
		}
		inst.mu.Unlock()
		if flip {
			log.Printf("[%s] ⚠ 节点失联（连续 %d 次失败）: %v", inst.cfg.ID, nodeDownThreshold, err)
			inst.notifyEvent("node_down", "⚠ 节点失联", map[string]string{"error": err.Error()})
		}
		return
	}
	inst.mu.Lock()
	inst.nodeFails = 0
	recovered := inst.nodeDown
	inst.nodeDown = false
	inst.mu.Unlock()
	if recovered {
		log.Printf("[%s] 节点恢复 height=%d", inst.cfg.ID, st.Height)
		inst.notifyEvent("node_up", "节点恢复", map[string]string{"height": fmt.Sprint(st.Height)})
	}
	inst.mu.Lock()
	clean := st.Height != inst.lastHeight
	inst.lastHeight = st.Height
	needHashPS := inst.parts.hashps != nil && time.Since(inst.netHashAt) > 30*time.Second
	inst.mu.Unlock()
	if needHashPS {
		if hps, err := inst.parts.hashps.NetworkHashPS(ctx); err == nil {
			inst.mu.Lock()
			inst.netHashPS, inst.netHashAt = hps, time.Now()
			inst.mu.Unlock()
		}
	}
	if err := inst.parts.jobs.Refresh(ctx, clean); err != nil {
		log.Printf("[%s] 模板刷新失败: %v", inst.cfg.ID, err)
	}
}

// Stop 优雅关停该币（先停端口新连接与循环）。
func (inst *Instance) Stop() {
	inst.ports.StopAll()
	inst.cancel()
	inst.wg.Wait()
}

// ---- 公共 API 读接口（internal/api.Pool 的实现）----

// Cfg 该币配置快照（热改期间也一致：深拷贝 Ports 切片）。
func (inst *Instance) Cfg() config.CoinConfig {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	c := inst.cfg
	c.Ports = append([]config.PortConfig(nil), inst.cfg.Ports...)
	c.Nodes = append([]config.NodeEndpoint(nil), inst.cfg.Nodes...)
	return c
}

// Ledger 暴露会计快照（API 用）。
func (inst *Instance) Ledger() accounting.Ledger { return inst.ledger }

// Hashrate 算力采样器。
func (inst *Instance) Hashrate() *hashrate.Tracker { return inst.tracker }

// Batches 打款批次存储（API /payments 只读）。
func (inst *Instance) Batches() payout.BatchStore { return inst.batches }

// ConnectedMiners 当前 stratum 在连数。
// Connections 当前在线 stratum 连接数（≈矿机数：NTMminer/xmrig 每台机器一条连接）。
func (inst *Instance) Connections() int { return inst.parts.connCount() }

// Network 链上状态缓存快照（高度/难度来自当前 job，HashPS 来自真值口径缓存）。
func (inst *Instance) Network() core.NetworkSnapshot {
	ns := core.NetworkSnapshot{}
	if h, d, ok := inst.parts.jobs.Snapshot(); ok {
		ns.Height = h
		ns.Difficulty = d
	}
	inst.mu.Lock()
	if ns.Height == 0 {
		ns.Height = inst.lastHeight
	}
	ns.HashPS, ns.UpdatedAt = inst.netHashPS, inst.netHashAt
	inst.mu.Unlock()
	return ns
}

// RunPayoutNow 立即跑一轮打款（测试/管理后台用）。
func (inst *Instance) RunPayoutNow(ctx context.Context) error { return inst.engine.RunOnce(ctx) }

// ---- M2 热管理（管理后台调；全部立即生效 + 更新 cfg 快照）----

// PayoutFrozen 打款是否因守恒对账不平被冻结。
func (inst *Instance) PayoutFrozen() bool { return inst.engine.Frozen() }

// UnfreezePayout 人工解冻（对账修复后）。
func (inst *Instance) UnfreezePayout() { inst.engine.Unfreeze() }

// FeeSweep 手续费地址 → 冷地址（from 固定为本币 FeeAddress，双地址铁律）。
func (inst *Instance) FeeSweep(ctx context.Context, coldAddress, amount string) (string, error) {
	return inst.engine.FeeSweep(ctx, inst.Cfg().FeeAddress, coldAddress, amount)
}

// FeeCollect 池钱包 → 手续费地址归集（to 固定为本币 FeeAddress）。
func (inst *Instance) FeeCollect(ctx context.Context, amount string) (string, error) {
	return inst.engine.FeeCollect(ctx, inst.Cfg().FeeAddress, amount)
}

// UncollectedFees 未归集手续费（计提总费 − 已归集批次，含在途）。metrics/后台展示用。
func (inst *Instance) UncollectedFees(ctx context.Context) (string, error) {
	return inst.engine.UncollectedFees(ctx)
}

// ApplyPayout 热更新打款参数（R4）：费率/起付额/确认数/开关/归集，立即生效不追溯。
func (inst *Instance) ApplyPayout(p config.PayoutConfig) {
	// 直付币（midstate）：费率/尘埃阈值同时下发作业管理器（模板分账用同一套热参数）
	if sp, ok := inst.parts.jobs.(interface{ SetPayoutParams(float64, string) }); ok {
		sp.SetPayoutParams(p.FeePercent, p.MinPayout)
	}
	inst.engine.SetParams(p.FeePercent, parseFloat(p.MinPayout), p.Confirmations)
	inst.engine.SetSoloFeePercent(p.SoloFeePercent)
	inst.engine.SetEnabled(inst.deps.Payouts && p.Enabled)
	inst.engine.SetFeeCollect(p.FeeCollect.Enabled, p.FeeCollect.MinAmount)
	inst.mu.Lock()
	cur := &inst.cfg.Payout
	cur.FeePercent, cur.MinPayout, cur.Confirmations, cur.Enabled =
		p.FeePercent, p.MinPayout, p.Confirmations, p.Enabled
	cur.SoloFeePercent = p.SoloFeePercent
	cur.FeeCollect = p.FeeCollect
	if p.IntervalSec > 0 {
		cur.IntervalSec = p.IntervalSec // 下一轮 ticker 周期不变（M2 简化）；重启后生效
	}
	inst.mu.Unlock()
	log.Printf("[%s] 打款参数热更新: fee=%v%% min=%s conf=%d enabled=%v feeCollect=%v",
		inst.cfg.ID, p.FeePercent, p.MinPayout, p.Confirmations, p.Enabled, p.FeeCollect.Enabled)
}

// AddPort 热添加端口并立即监听。
func (inst *Instance) AddPort(pc config.PortConfig) error {
	inst.mu.Lock()
	for _, p := range inst.cfg.Ports {
		if p.Port == pc.Port {
			inst.mu.Unlock()
			return errf("[%s] 端口 %d 已存在", inst.cfg.ID, pc.Port)
		}
	}
	inst.mu.Unlock()
	if pc.Enabled {
		if err := inst.ports.StartPort(inst.ctx, pc); err != nil {
			return err
		}
	}
	inst.mu.Lock()
	inst.cfg.Ports = append(inst.cfg.Ports, pc)
	inst.mu.Unlock()
	log.Printf("[%s] 热添加端口 %d (%s/%s) enabled=%v", inst.cfg.ID, pc.Port, pc.Mode, pc.Dialect, pc.Enabled)
	return nil
}

// RemovePort 热删除端口：停止 accept + 断开该端口全部存量连接。
func (inst *Instance) RemovePort(port int) error {
	inst.mu.Lock()
	idx := -1
	for i, p := range inst.cfg.Ports {
		if p.Port == port {
			idx = i
			break
		}
	}
	if idx < 0 {
		inst.mu.Unlock()
		return errf("[%s] 端口 %d 不存在", inst.cfg.ID, port)
	}
	enabled := inst.cfg.Ports[idx].Enabled
	inst.cfg.Ports = append(inst.cfg.Ports[:idx], inst.cfg.Ports[idx+1:]...)
	inst.mu.Unlock()
	if enabled {
		if err := inst.ports.StopPort(port); err != nil {
			return err
		}
	}
	log.Printf("[%s] 热删除端口 %d", inst.cfg.ID, port)
	return nil
}

// SetPortEnabled 热启停端口（保留配置，只动监听器）。
func (inst *Instance) SetPortEnabled(port int, enabled bool) error {
	inst.mu.Lock()
	var pc *config.PortConfig
	for i := range inst.cfg.Ports {
		if inst.cfg.Ports[i].Port == port {
			pc = &inst.cfg.Ports[i]
			break
		}
	}
	if pc == nil {
		inst.mu.Unlock()
		return errf("[%s] 端口 %d 不存在", inst.cfg.ID, port)
	}
	was := pc.Enabled
	pc.Enabled = enabled
	snapshot := *pc
	inst.mu.Unlock()
	if was == enabled {
		return nil
	}
	if enabled {
		return inst.ports.StartPort(inst.ctx, snapshot)
	}
	return inst.ports.StopPort(port)
}

// SetNewConns 热开关：是否接受新连接（存量不断；R7 币开关的软下线用法）。
func (inst *Instance) SetNewConns(v bool) {
	inst.ports.SetAcceptNew(v)
	inst.mu.Lock()
	inst.cfg.NewConnsEnabled = v
	inst.mu.Unlock()
	log.Printf("[%s] 新连接开关 = %v", inst.cfg.ID, v)
}

// RunReconcile 手工触发守恒对账，返回 delta（"0.00000000" = 平）。
func (inst *Instance) RunReconcile(ctx context.Context) (string, error) {
	return inst.ledger.Reconcile(ctx, inst.cfg.ID)
}
