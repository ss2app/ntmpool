package payout

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/metrics"
)

// NodeClassifier 是打款引擎对节点的依赖（孤块判定：确认数 + 主链 hash 逐字节比对）。
// Confirmations 的 height 参数：只有按高度查块 API 的链（zoka 类 REST）靠它定位。
type NodeClassifier interface {
	Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error)
	BlockHashAt(ctx context.Context, height uint64) (string, error)
}

// BatchStore 打款批次持久化（崩溃恢复的关键，docs/05 场景A）。
// 内存实现供 M1/测试；Postgres 实现供生产（重启后 recovery 扫描它）。
type BatchStore interface {
	NextBatchID() (int64, error)
	Save(b *Batch) error
	Load(id int64) (*Batch, bool, error)
	Unfinished() ([]*Batch, error) // created/prepared/sent 但未 confirmed/failed
	All() ([]*Batch, error)        // 全部批次（新→旧），公共 API /payments 用
}

// Batch 一笔打款批次的完整状态（对应 payment_batches 表）。
type Batch struct {
	ID          int64
	Kind        string // payout | fee_collect | fee_sweep | consolidate
	Outputs     map[string]string
	Status      core.PaymentStatus
	PlannedTxID string
	RawTx       string
	TxID        string
	CreatedAt   time.Time
}

// Config 打款引擎配置（热参数子集在这里取快照）。
type Config struct {
	Coin       string
	Decimals   int
	FeePercent float64
	MinPayout  float64
	Maturity   int64 // 打款所需确认数（低于链成熟期 = 预打款）

	// 手续费自动归集（R9）：未归集费 ≥ FeeCollectMin 时在打款周期尾部自动
	// 池钱包→FeeAddress（与打款共用每币锁，天然串行）。
	FeeAddress        string
	FeeCollectEnabled bool
	FeeCollectMin     string // 十进制字符串（金额铁律）；空/0 = 有多少归多少
}

// EventFunc 运营事件上报（notify.Hub 适配；nil = 不上报）。绝不阻塞调用方。
type EventFunc func(kind, title string, fields map[string]string)

// Engine 打款引擎（每币一个）。持有该币打款锁：正常打款/手续费/整备互斥。
type Engine struct {
	cfg    Config
	ledger accounting.Ledger
	node   NodeClassifier
	wallet adapter.WalletAdapter
	rawtx  adapter.RawTxWallet // 可选：拆步打款
	store  BatchStore

	mu      sync.Mutex // 每币打款锁
	enabled bool
	frozen  bool // 守恒对账破坏时冻结

	// minOverrides 地址级起付额覆盖（矿工 mp= 设置，minersettings 注入；可为 nil）
	minOverrides func() map[string]float64
	// events 运营事件上报（可为 nil）
	events EventFunc
	// maintainer 钱包整备器（可为 nil；dragonx 类隐私链：shield 成熟 coinbase
	// 回金库，打款锁内、payout 前调——docs/02 §6 承诺的接线，dragonx 首用）
	maintainer adapter.WalletMaintainer
}

func NewEngine(cfg Config, l accounting.Ledger, node NodeClassifier, w adapter.WalletAdapter, store BatchStore) *Engine {
	e := &Engine{cfg: cfg, ledger: l, node: node, wallet: w, store: store, enabled: true}
	if rt, ok := w.(adapter.RawTxWallet); ok {
		e.rawtx = rt
	}
	return e
}

func (e *Engine) SetEnabled(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = v
}

// Enabled 当前打款开关状态（管理后台查询）。
func (e *Engine) Enabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled
}

// SetMaintainer 注入钱包整备器（启动时一次；nil 安全）。
func (e *Engine) SetMaintainer(m adapter.WalletMaintainer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maintainer = m
}

// SetMinPayoutOverrides 注入地址级起付额来源（启动时一次）。
func (e *Engine) SetMinPayoutOverrides(f func() map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.minOverrides = f
}

// SetEvents 注入事件上报（启动时一次）。
func (e *Engine) SetEvents(f EventFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = f
}

// emit 上报事件（须持 e.mu 或在启动前）；nil 安全。
func (e *Engine) emit(kind, title string, fields map[string]string) {
	if e.events != nil {
		e.events(kind, title, fields)
	}
}

// SetFeeCollect 热更新自动归集设置。
func (e *Engine) SetFeeCollect(enabled bool, minAmount string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.FeeCollectEnabled, e.cfg.FeeCollectMin = enabled, minAmount
}

// SetParams 热更新打款参数（R4：新 round 用新值，已入账的不追溯）。
// 只允许改费率/起付额/确认数——币种/精度是身份，不可热改。
func (e *Engine) SetParams(feePercent, minPayout float64, maturity int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if feePercent >= 0 && feePercent <= 100 {
		e.cfg.FeePercent = feePercent
	}
	if minPayout > 0 {
		e.cfg.MinPayout = minPayout
	}
	if maturity > 0 {
		e.cfg.Maturity = maturity
	}
}

// Params 当前生效参数快照（管理后台/审计用）。
func (e *Engine) Params() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Frozen 是否因守恒对账不平被冻结。
func (e *Engine) Frozen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozen
}

// Unfreeze 人工解冻（管理后台，对账修复后调用）。下一轮对账仍不平会再次冻结。
func (e *Engine) Unfreeze() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.frozen = false
	log.Printf("[payout %s] 人工解冻打款（下一轮对账不平将再次冻结）", e.cfg.Coin)
}

// RunOnce 一轮：分类块 → 入账 → 守恒对账 → 打款。持锁串行。
func (e *Engine) RunOnce(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.classify(ctx); err != nil {
		log.Printf("[payout %s] 分类块失败: %v", e.cfg.Coin, err)
	}

	// 守恒对账：破则冻结打款（bitcoin09 超发事故的事前防线）
	delta, err := e.ledger.Reconcile(ctx, e.cfg.Coin)
	if err == nil && delta != "" && !isZeroAmount(delta) {
		if !e.frozen {
			// 只在翻转时通知一次（持续状态不刷屏）
			e.emit("reconcile_frozen", "⚠ 守恒对账不平，打款已冻结",
				map[string]string{"delta": delta})
		}
		e.frozen = true
		log.Printf("[payout %s] ⚠ 守恒对账不平 delta=%s，冻结打款", e.cfg.Coin, delta)
	}

	if !e.enabled || e.frozen {
		return nil
	}
	e.maintain(ctx)
	if err := e.payout(ctx); err != nil {
		return err
	}
	e.autoFeeCollect(ctx)
	return nil
}

// maintain 钱包整备（须持 e.mu，payout 前调）：隐私链把成熟 coinbase shield 回
// 金库，垫付资金闭环。失败只记日志下轮再试，绝不阻断本轮打款。
func (e *Engine) maintain(ctx context.Context) {
	if e.maintainer == nil {
		return
	}
	need, desc, err := e.maintainer.NeedsMaintenance(ctx)
	if err != nil || !need {
		return
	}
	txids, err := e.maintainer.Maintain(ctx)
	if err != nil {
		log.Printf("[payout %s] 钱包整备失败（下轮再试）: %v", e.cfg.Coin, err)
		return
	}
	if len(txids) > 0 {
		log.Printf("[payout %s] 钱包整备完成: %s txids=%v", e.cfg.Coin, desc, txids)
		e.emit("wallet_maintained", "钱包整备完成（shield/merge）", map[string]string{
			"desc": desc, "txids": strings.Join(txids, ",")})
	}
}

// classify 推进待确认块状态：确认数达标 + 主链 hash 逐字节比对 → confirm/orphan。
func (e *Engine) classify(ctx context.Context) error {
	pending, err := e.ledger.PendingBlocks(ctx, e.cfg.Coin)
	if err != nil {
		return err
	}
	for _, b := range pending {
		conf, err := e.node.Confirmations(ctx, b.Hash, b.Height)
		if err != nil {
			continue
		}
		if conf < 0 {
			// 节点已不认识该块 = 孤块
			_ = e.ledger.OrphanBlock(ctx, b)
			log.Printf("[payout %s] 块 %d %s → ORPHANED (不在主链)", e.cfg.Coin, b.Height, short(b.Hash))
			e.emit("block_orphaned", "块被主链甩掉（不在主链）", map[string]string{
				"height": fmt.Sprint(b.Height), "ours": b.Hash})
			continue
		}
		if conf < e.cfg.Maturity {
			continue // 未成熟，继续等
		}
		// 成熟前最后一道闸：按高度取主链块 hash 与我们记的逐字节比对（铁律）
		mainHash, err := e.node.BlockHashAt(ctx, b.Height)
		if err != nil {
			continue
		}
		if mainHash != b.Hash {
			_ = e.ledger.OrphanBlock(ctx, b)
			log.Printf("[payout %s] 块 %d 主链 hash 不符 → ORPHANED (我们=%s 主链=%s)",
				e.cfg.Coin, b.Height, short(b.Hash), short(mainHash))
			e.emit("block_orphaned", "块被主链甩掉（hash 不符）", map[string]string{
				"height": fmt.Sprint(b.Height), "ours": b.Hash, "mainchain": mainHash})
			continue
		}
		if err := e.ledger.ConfirmBlock(ctx, b, e.cfg.FeePercent); err != nil {
			log.Printf("[payout %s] 块 %d 入账失败: %v", e.cfg.Coin, b.Height, err)
			continue
		}
		log.Printf("[payout %s] 块 %d %s → CONFIRMED，PPLNS 分账", e.cfg.Coin, b.Height, short(b.Hash))
		e.emit("block_confirmed", "块已确认，PPLNS 分账", map[string]string{
			"height": fmt.Sprint(b.Height), "hash": b.Hash, "reward": b.Reward})
	}
	return nil
}

// payout 一轮打款：组批 → 先扣余额 → 拆步签名落库 → 广播 → 追踪。
func (e *Engine) payout(ctx context.Context) error {
	var perAddr map[string]float64
	if e.minOverrides != nil {
		perAddr = e.minOverrides() // 矿工 mp= 地址级起付额（只能调高，Ledger 侧取 max）
	}
	payable, err := e.ledger.PayableBalances(ctx, e.cfg.Coin, e.cfg.MinPayout, perAddr)
	if err != nil || len(payable) == 0 {
		return err
	}
	batchID, err := e.store.NextBatchID()
	if err != nil {
		return fmt.Errorf("取批次号: %w", err)
	}
	batch := &Batch{
		ID:        batchID,
		Kind:      "payout",
		Outputs:   payable,
		Status:    core.PaymentCreated,
		CreatedAt: time.Now(),
	}
	// ① 先扣余额（防双花第一步）
	if err := e.ledger.DeductForPayout(ctx, e.cfg.Coin, payable, batch.ID); err != nil {
		return fmt.Errorf("扣余额失败: %w", err)
	}
	if err := e.store.Save(batch); err != nil {
		// 意图未落库：尚未签名/广播，退回余额中止（绝不带着没记录的批次往下走）
		_ = e.ledger.RefundPayout(ctx, e.cfg.Coin, payable, batch.ID)
		return fmt.Errorf("批次意图落库失败(已退回余额): %w", err)
	}

	// ② 拆步打款（RawTxWallet）：签名即定 txid → 落库 → 广播（崩溃零歧义）
	if e.rawtx != nil {
		txid, rawtx, err := e.rawtx.PrepareSendMany(ctx, payable)
		if err != nil {
			// 未广播，安全退回余额
			_ = e.ledger.RefundPayout(ctx, e.cfg.Coin, payable, batch.ID)
			e.emit("payout_failed", "打款准备失败（已退回余额）", map[string]string{
				"batch": fmt.Sprint(batch.ID), "error": err.Error()})
			return fmt.Errorf("准备打款失败(已退回): %w", err)
		}
		batch.PlannedTxID, batch.RawTx, batch.Status = txid, rawtx, core.PaymentSent
		if err := e.store.Save(batch); err != nil {
			// 广播前落库失败 = 绝不广播（已签名未广播无双花风险，签名交易作废）
			batch.Status = core.PaymentFailed
			_ = e.store.Save(batch)
			_ = e.ledger.RefundPayout(ctx, e.cfg.Coin, payable, batch.ID)
			e.emit("payout_failed", "广播前落库失败（未广播，已退回余额）", map[string]string{
				"batch": fmt.Sprint(batch.ID), "error": err.Error()})
			return fmt.Errorf("广播前落库失败(未广播,已退回): %w", err)
		}
		if err := e.rawtx.Broadcast(ctx, rawtx); err != nil {
			// 广播失败：交易已签名落库，交给恢复流程按 plannedtxid 判定，绝不在此重发
			log.Printf("[payout %s] 广播失败 batch=%d txid=%s: %v（留待恢复扫描）",
				e.cfg.Coin, batch.ID, short(txid), err)
			e.emit("payout_failed", "打款广播失败（已落库，待恢复流程重播）", map[string]string{
				"batch": fmt.Sprint(batch.ID), "txid": txid, "error": err.Error()})
			return nil
		}
		batch.TxID = txid
		if err := e.store.Save(batch); err != nil {
			log.Printf("[payout %s] ⚠ 广播后落库失败 batch=%d（恢复扫描会按 plannedtxid 归位）: %v",
				e.cfg.Coin, batch.ID, err)
		}
		metrics.PayoutSent(e.cfg.Coin, e.sumCoins(payable))
		log.Printf("[payout %s] 打款 batch=%d 已广播 txid=%s (%d 地址)",
			e.cfg.Coin, batch.ID, short(txid), len(payable))
		e.emit("payout_sent", "打款已广播", map[string]string{
			"batch": fmt.Sprint(batch.ID), "txid": txid, "addresses": fmt.Sprint(len(payable))})
		return nil
	}

	// ③ 不支持拆步的链：sendmany 一步（构造+广播原子），崩溃窗口交恢复人工比对
	txid, err := e.wallet.SendMany(ctx, payable)
	if err != nil {
		batch.Status = core.PaymentFailed
		_ = e.store.Save(batch)
		// sendmany 可能已广播（超时），绝不自动退回也绝不自动重发 → unknown 交人工
		log.Printf("[payout %s] sendmany 失败 batch=%d: %v（人工核对，绝不自动重发）",
			e.cfg.Coin, batch.ID, err)
		e.emit("payout_failed", "sendmany 失败（unknown 状态，须人工核对）", map[string]string{
			"batch": fmt.Sprint(batch.ID), "error": err.Error()})
		return nil
	}
	batch.TxID, batch.Status = txid, core.PaymentSent
	_ = e.store.Save(batch)
	metrics.PayoutSent(e.cfg.Coin, e.sumCoins(payable))
	log.Printf("[payout %s] 打款 batch=%d txid=%s", e.cfg.Coin, batch.ID, short(txid))
	e.emit("payout_sent", "打款已广播", map[string]string{
		"batch": fmt.Sprint(batch.ID), "txid": txid, "addresses": fmt.Sprint(len(payable))})
	return nil
}

// sumCoins 汇总一批打款输出为币量浮点（仅供 metrics 展示，绝不用于结算）。
func (e *Engine) sumCoins(outputs map[string]string) float64 {
	var total float64
	unit := float64(int64(1))
	for i := 0; i < e.cfg.Decimals; i++ {
		unit *= 10
	}
	for _, s := range outputs {
		if sat, err := parseAmountSat(s, e.cfg.Decimals); err == nil {
			total += float64(sat) / unit
		}
	}
	return total
}

// autoFeeCollect 手续费自动归集（须持 e.mu，打款周期尾部调）：
// 未归集费 = Ledger 计提总费 − Σ 已归集批次（含在途，保守防重复归集），
// 达到 FeeCollectMin 才动手。失败只记日志，下轮再试。
func (e *Engine) autoFeeCollect(ctx context.Context) {
	if !e.cfg.FeeCollectEnabled || e.cfg.FeeAddress == "" {
		return
	}
	stats, err := e.ledger.Snapshot(ctx, e.cfg.Coin)
	if err != nil {
		return
	}
	totalSat, err := parseAmountSat(stats.TotalFees, e.cfg.Decimals)
	if err != nil || totalSat <= 0 {
		return
	}
	all, err := e.store.All()
	if err != nil {
		log.Printf("[payout %s] 自动归集读批次失败（下轮再试）: %v", e.cfg.Coin, err)
		return
	}
	var collectedSat int64
	for _, b := range all {
		if b.Kind != "fee_collect" || b.Status == core.PaymentFailed {
			continue
		}
		for _, amt := range b.Outputs {
			if v, err := parseAmountSat(amt, e.cfg.Decimals); err == nil {
				collectedSat += v // created/sent/confirming 都算（保守：宁少归不重复）
			}
		}
	}
	unc := totalSat - collectedSat
	if unc <= 0 {
		return
	}
	minSat := int64(0)
	if e.cfg.FeeCollectMin != "" {
		if v, err := parseAmountSat(e.cfg.FeeCollectMin, e.cfg.Decimals); err == nil {
			minSat = v
		}
	}
	if unc < minSat {
		return
	}
	amount := formatAmountSat(unc, e.cfg.Decimals)
	txid, err := e.sendBatchLocked(ctx, "fee_collect", map[string]string{e.cfg.FeeAddress: amount})
	if err != nil {
		log.Printf("[payout %s] 自动归集失败（下轮再试）: %v", e.cfg.Coin, err)
		return
	}
	log.Printf("[payout %s] 手续费自动归集 %s → %s txid=%s",
		e.cfg.Coin, amount, short(e.cfg.FeeAddress), short(txid))
	e.emit("fee_collected", "手续费已自动归集", map[string]string{
		"amount": amount, "txid": txid})
}

// Recover 崩溃恢复扫描（docs/05 场景A + 六步清单第 2~3 步）：
// 未完成批次按 plannedtxid 查链判定「广播出去没有」→ 续流程或重播。
func (e *Engine) Recover(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rawtx == nil {
		return nil // 无拆步能力，unfinished 批次靠人工
	}
	unfinished, err := e.store.Unfinished()
	if err != nil {
		return fmt.Errorf("恢复扫描读批次: %w", err)
	}
	for _, b := range unfinished {
		if b.PlannedTxID == "" {
			// created 但无 txid：交易从未签名 → 退回余额再标 failed。
			// 退款在 PG 实现里按 batch 幂等（重复恢复不会双退）；标记未落库时
			// 批次保持 unfinished，下轮重试整个闭环。
			if err := e.ledger.RefundPayout(ctx, e.cfg.Coin, b.Outputs, b.ID); err != nil {
				log.Printf("[payout %s] 恢复: batch=%d 退款失败，下轮重试: %v", e.cfg.Coin, b.ID, err)
				continue
			}
			b.Status = core.PaymentFailed
			if err := e.store.Save(b); err != nil {
				log.Printf("[payout %s] 恢复: batch=%d 标记失败未落库（退款幂等，下轮补标）: %v", e.cfg.Coin, b.ID, err)
			}
			continue
		}
		exists, err := e.rawtx.TxExists(ctx, b.PlannedTxID)
		if err != nil {
			continue
		}
		if exists {
			b.TxID, b.Status = b.PlannedTxID, core.PaymentSent
			_ = e.store.Save(b)
			log.Printf("[payout %s] 恢复: batch=%d 已在链/内存池，续追踪 txid=%s",
				e.cfg.Coin, b.ID, short(b.PlannedTxID))
		} else if b.RawTx != "" {
			// 未广播：原样重播同一笔签名交易（同 txid，幂等，不可能双花）
			if err := e.rawtx.Broadcast(ctx, b.RawTx); err == nil {
				b.TxID, b.Status = b.PlannedTxID, core.PaymentSent
				_ = e.store.Save(b)
				log.Printf("[payout %s] 恢复: batch=%d 重播成功 txid=%s",
					e.cfg.Coin, b.ID, short(b.PlannedTxID))
			}
		}
	}
	return nil
}

// FeeSweep 一键把手续费地址余额转到冷地址（R9）。
// 持打款锁（与正常打款互斥）→ 从 fromFeeAddress 构造转账 → 走同一意图落库+广播流程。
// 注：本引擎持锁即「暂停打款」；等在途确认由调用方在无 pending batch 时调用保证。
func (e *Engine) FeeSweep(ctx context.Context, fromFeeAddress, coldAddress, amount string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozen {
		return "", fmt.Errorf("[%s] 打款冻结中，拒绝 fee sweep", e.cfg.Coin)
	}
	// 手续费地址的钱是池外收益（不在 ledger balances 里），不走 DeductForPayout。
	return e.sendBatchLocked(ctx, "fee_sweep", map[string]string{coldAddress: amount})
}

// FeeCollect 手续费归集（R9）：从池钱包转 amount 到手续费地址（kind=fee_collect）。
// 池钱包里矿工余额与费混在一起（coinbase 全进池地址）——转出额度必须由调用方
// 对照 Ledger 的 TotalFees 与历史归集量把关（管理后台展示未归集额并校验）。
func (e *Engine) FeeCollect(ctx context.Context, feeAddress, amount string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozen {
		return "", fmt.Errorf("[%s] 打款冻结中，拒绝手续费归集", e.cfg.Coin)
	}
	return e.sendBatchLocked(ctx, "fee_collect", map[string]string{feeAddress: amount})
}

// sendBatchLocked 通用「意图落库→拆步签名→广播前落库→广播」批次发送（须持 e.mu）。
func (e *Engine) sendBatchLocked(ctx context.Context, kind string, outputs map[string]string) (string, error) {
	batchID, err := e.store.NextBatchID()
	if err != nil {
		return "", fmt.Errorf("取批次号: %w", err)
	}
	batch := &Batch{ID: batchID, Kind: kind, Outputs: outputs, Status: core.PaymentCreated, CreatedAt: time.Now()}
	if err := e.store.Save(batch); err != nil {
		// 意图未落库：未签名未广播，直接中止（fee 批次不动 ledger 余额，无需回滚）
		return "", fmt.Errorf("批次意图落库失败: %w", err)
	}
	if e.rawtx != nil {
		txid, rawtx, err := e.rawtx.PrepareSendMany(ctx, outputs)
		if err != nil {
			batch.Status = core.PaymentFailed
			_ = e.store.Save(batch)
			return "", err
		}
		batch.PlannedTxID, batch.RawTx, batch.Status = txid, rawtx, core.PaymentSent
		if err := e.store.Save(batch); err != nil {
			// 广播前落库失败 = 绝不广播（签名交易作废，无双花风险）
			batch.Status = core.PaymentFailed
			_ = e.store.Save(batch)
			return "", fmt.Errorf("广播前落库失败(未广播): %w", err)
		}
		if err := e.rawtx.Broadcast(ctx, rawtx); err != nil {
			return "", err
		}
		batch.TxID = txid
		if err := e.store.Save(batch); err != nil {
			log.Printf("[payout %s] ⚠ 广播后落库失败 batch=%d（恢复扫描按 plannedtxid 归位）: %v",
				e.cfg.Coin, batch.ID, err)
		}
		return txid, nil
	}
	txid, err := e.wallet.SendMany(ctx, outputs)
	if err != nil {
		batch.Status = core.PaymentFailed
		_ = e.store.Save(batch)
		return "", err
	}
	batch.TxID, batch.Status = txid, core.PaymentSent
	if err := e.store.Save(batch); err != nil {
		log.Printf("[payout %s] ⚠ 广播后落库失败 batch=%d txid=%s: %v",
			e.cfg.Coin, batch.ID, short(txid), err)
	}
	return txid, nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func isZeroAmount(s string) bool {
	for _, c := range s {
		if c != '0' && c != '.' && c != '-' && c != '+' {
			return false
		}
	}
	return true
}

// parseAmountSat 十进制字符串 → 最小单位整数（金额铁律：不过浮点）。
func parseAmountSat(s string, decimals int) (int64, error) {
	unit := int64(1)
	for i := 0; i < decimals; i++ {
		unit *= 10
	}
	var whole, frac int64
	fracDigits := 0
	neg := false
	i := 0
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i = 1
	}
	seenDot := false
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if seenDot {
				return 0, fmt.Errorf("非法金额 %q", s)
			}
			seenDot = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("非法金额 %q", s)
		}
		if seenDot {
			if fracDigits < decimals {
				frac = frac*10 + int64(c-'0')
				fracDigits++
			}
		} else {
			whole = whole*10 + int64(c-'0')
		}
	}
	for fracDigits < decimals {
		frac *= 10
		fracDigits++
	}
	v := whole*unit + frac
	if neg {
		v = -v
	}
	return v, nil
}

// formatAmountSat 最小单位整数 → 十进制字符串。
func formatAmountSat(v int64, decimals int) string {
	unit := int64(1)
	for i := 0; i < decimals; i++ {
		unit *= 10
	}
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%0*d", neg, v/unit, decimals, v%unit)
}
