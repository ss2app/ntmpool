// Package coininstance 把一个币的所有部件拼装并运行：
// 节点适配器 × 算法 × JobManager × stratum 端口 × 会计 × 打款。
// 每币一个独立生命周期（可运行时增删，不影响其他币）——这是 NTMPool 对 miningcore
// 「加币要重启全进程」短板的核心改进。
package coininstance

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter/bitcoinrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/jobmanager"
	"github.com/scashcc/ntmpool/internal/payout"
	"github.com/scashcc/ntmpool/internal/stratum"
)

const extraNonce2Size = 4

// Instance 一个运行中的币。
type Instance struct {
	cfg     config.CoinConfig
	node    *bitcoinrpc.Client
	jm      *jobmanager.JobManager
	dialect *stratum.V1Dialect
	ports   *stratum.Manager
	ledger  accounting.Ledger
	engine  *payout.Engine

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	lastHeight uint64
}

// Start 拼装并启动该币（bitcoin-rpc + stratum1 方言，M1 竖切）。
func Start(parent context.Context, cfg config.CoinConfig, payoutsEnabled bool) (*Instance, error) {
	if len(cfg.Nodes) == 0 {
		return nil, errf("[%s] 未配置节点", cfg.ID)
	}
	n := cfg.Nodes[0]
	node := bitcoinrpc.New(cfg.ID, n.URL, n.User, n.Pass)

	hsh, err := hasher.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	inst := &Instance{cfg: cfg, node: node, cancel: cancel}

	// 会计 + 打款
	decimals := 8
	inst.ledger = accounting.NewMemLedger(decimals, cfg.Payout.PplnsFactor)
	pcfg := payout.Config{
		Coin: cfg.ID, Decimals: decimals,
		FeePercent: cfg.Payout.FeePercent, MinPayout: parseFloat(cfg.Payout.MinPayout),
		Maturity: cfg.Payout.Confirmations,
	}
	inst.engine = payout.NewEngine(pcfg, inst.ledger, node, node, payout.NewMemBatchStore())
	inst.engine.SetEnabled(payoutsEnabled && cfg.Payout.Enabled)

	// JobManager + 方言
	reg := stratum.NewJobRegistry()
	inst.jm = jobmanager.New(cfg.ID, node, hsh, reg, extraNonce2Size, decimals)
	if err := inst.jm.Init(ctx, cfg.PoolAddress); err != nil {
		cancel()
		return nil, errf("[%s] 初始化矿池地址脚本失败: %v", cfg.ID, err)
	}
	inst.dialect = stratum.NewV1Dialect(cfg.ID, inst.jm)
	inst.jm.SetCallbacks(
		inst.dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string) error {
			log.Printf("[%s] ★爆块 height=%d hash=%s finder=%s", cfg.ID, b.Height, b.Hash[:12], b.Finder)
			return inst.ledger.RecordBlock(ctx, b, rawHex)
		},
		func(ctx context.Context, s core.Share) {
			_ = inst.ledger.RecordShare(ctx, s, s.Difficulty)
		},
	)

	// 端口（每币可多端口，热管理）
	inst.ports = stratum.NewManager(cfg.ID, map[string]stratum.Dialect{"stratum1": inst.dialect})
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
	if err := inst.jm.Refresh(ctx, true); err != nil {
		log.Printf("[%s] 首次模板刷新失败（将在循环中重试）: %v", cfg.ID, err)
	}

	inst.startLoops(ctx)
	// 恢复扫描（崩溃恢复；内存实现无持久化，Postgres 实现时真正生效）
	_ = inst.engine.Recover(ctx)
	return inst, nil
}

func (inst *Instance) startLoops(ctx context.Context) {
	// 模板刷新循环（M1 = 轮询；ZMQ/push 通知作为 M3 增强）。
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
func (inst *Instance) refreshOnce(ctx context.Context) {
	st, err := inst.node.Status(ctx)
	if err != nil {
		return
	}
	inst.mu.Lock()
	clean := st.Height != inst.lastHeight
	inst.lastHeight = st.Height
	inst.mu.Unlock()
	if err := inst.jm.Refresh(ctx, clean); err != nil {
		log.Printf("[%s] 模板刷新失败: %v", inst.cfg.ID, err)
	}
}

// Stop 优雅关停该币（先停端口新连接与循环）。
func (inst *Instance) Stop() {
	inst.ports.StopAll()
	inst.cancel()
	inst.wg.Wait()
}

// Ledger 暴露会计快照（API 用）。
func (inst *Instance) Ledger() accounting.Ledger { return inst.ledger }

// RunPayoutNow 立即跑一轮打款（测试/管理后台用）。
func (inst *Instance) RunPayoutNow(ctx context.Context) error { return inst.engine.RunOnce(ctx) }
