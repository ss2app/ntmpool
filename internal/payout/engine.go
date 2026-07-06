package payout

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/core"
)

// NodeClassifier 是打款引擎对节点的依赖（孤块判定：确认数 + 主链 hash 逐字节比对）。
type NodeClassifier interface {
	Confirmations(ctx context.Context, blockHash string) (int64, error)
	BlockHashAt(ctx context.Context, height uint64) (string, error)
}

// BatchStore 打款批次持久化（崩溃恢复的关键，docs/05 场景A）。
// 内存实现供 M1/测试；Postgres 实现供生产（重启后 recovery 扫描它）。
type BatchStore interface {
	NextBatchID() int64
	Save(b *Batch) error
	Load(id int64) (*Batch, bool)
	Unfinished() []*Batch // created/prepared/sent 但未 confirmed/failed
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
}

// Config 打款引擎配置（热参数子集在这里取快照）。
type Config struct {
	Coin          string
	Decimals      int
	FeePercent    float64
	MinPayout     float64
	Maturity      int64 // 打款所需确认数（低于链成熟期 = 预打款）
}

// Engine 打款引擎（每币一个）。持有该币打款锁：正常打款/手续费/整备互斥。
type Engine struct {
	cfg     Config
	ledger  accounting.Ledger
	node    NodeClassifier
	wallet  adapter.WalletAdapter
	rawtx   adapter.RawTxWallet // 可选：拆步打款
	store   BatchStore

	mu       sync.Mutex // 每币打款锁
	enabled  bool
	frozen   bool // 守恒对账破坏时冻结
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
		e.frozen = true
		log.Printf("[payout %s] ⚠ 守恒对账不平 delta=%s，冻结打款", e.cfg.Coin, delta)
	}

	if !e.enabled || e.frozen {
		return nil
	}
	return e.payout(ctx)
}

// classify 推进待确认块状态：确认数达标 + 主链 hash 逐字节比对 → confirm/orphan。
func (e *Engine) classify(ctx context.Context) error {
	pending, err := e.ledger.PendingBlocks(ctx, e.cfg.Coin)
	if err != nil {
		return err
	}
	for _, b := range pending {
		conf, err := e.node.Confirmations(ctx, b.Hash)
		if err != nil {
			continue
		}
		if conf < 0 {
			// 节点已不认识该块 = 孤块
			_ = e.ledger.OrphanBlock(ctx, b)
			log.Printf("[payout %s] 块 %d %s → ORPHANED (不在主链)", e.cfg.Coin, b.Height, short(b.Hash))
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
			continue
		}
		if err := e.ledger.ConfirmBlock(ctx, b, e.cfg.FeePercent); err != nil {
			log.Printf("[payout %s] 块 %d 入账失败: %v", e.cfg.Coin, b.Height, err)
			continue
		}
		log.Printf("[payout %s] 块 %d %s → CONFIRMED，PPLNS 分账", e.cfg.Coin, b.Height, short(b.Hash))
	}
	return nil
}

// payout 一轮打款：组批 → 先扣余额 → 拆步签名落库 → 广播 → 追踪。
func (e *Engine) payout(ctx context.Context) error {
	payable, err := e.ledger.PayableBalances(ctx, e.cfg.Coin, e.cfg.MinPayout, nil)
	if err != nil || len(payable) == 0 {
		return err
	}
	batch := &Batch{
		ID:      e.store.NextBatchID(),
		Kind:    "payout",
		Outputs: payable,
		Status:  core.PaymentCreated,
	}
	// ① 先扣余额（防双花第一步）
	if err := e.ledger.DeductForPayout(ctx, e.cfg.Coin, payable, batch.ID); err != nil {
		return fmt.Errorf("扣余额失败: %w", err)
	}
	_ = e.store.Save(batch)

	// ② 拆步打款（RawTxWallet）：签名即定 txid → 落库 → 广播（崩溃零歧义）
	if e.rawtx != nil {
		txid, rawtx, err := e.rawtx.PrepareSendMany(ctx, payable)
		if err != nil {
			// 未广播，安全退回余额
			_ = e.ledger.RefundPayout(ctx, e.cfg.Coin, payable, batch.ID)
			return fmt.Errorf("准备打款失败(已退回): %w", err)
		}
		batch.PlannedTxID, batch.RawTx, batch.Status = txid, rawtx, core.PaymentSent
		_ = e.store.Save(batch) // 广播前落库！
		if err := e.rawtx.Broadcast(ctx, rawtx); err != nil {
			// 广播失败：交易已签名落库，交给恢复流程按 plannedtxid 判定，绝不在此重发
			log.Printf("[payout %s] 广播失败 batch=%d txid=%s: %v（留待恢复扫描）",
				e.cfg.Coin, batch.ID, short(txid), err)
			return nil
		}
		batch.TxID = txid
		_ = e.store.Save(batch)
		log.Printf("[payout %s] 打款 batch=%d 已广播 txid=%s (%d 地址)",
			e.cfg.Coin, batch.ID, short(txid), len(payable))
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
		return nil
	}
	batch.TxID, batch.Status = txid, core.PaymentSent
	_ = e.store.Save(batch)
	log.Printf("[payout %s] 打款 batch=%d txid=%s", e.cfg.Coin, batch.ID, short(txid))
	return nil
}

// Recover 崩溃恢复扫描（docs/05 场景A + 六步清单第 2~3 步）：
// 未完成批次按 plannedtxid 查链判定「广播出去没有」→ 续流程或重播。
func (e *Engine) Recover(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rawtx == nil {
		return nil // 无拆步能力，unfinished 批次靠人工
	}
	for _, b := range e.store.Unfinished() {
		if b.PlannedTxID == "" {
			// created 但无 txid：交易从未签名 → 退回余额
			_ = e.ledger.RefundPayout(ctx, e.cfg.Coin, b.Outputs, b.ID)
			b.Status = core.PaymentFailed
			_ = e.store.Save(b)
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
	outputs := map[string]string{coldAddress: amount}
	batch := &Batch{ID: e.store.NextBatchID(), Kind: "fee_sweep", Outputs: outputs, Status: core.PaymentCreated}
	_ = e.store.Save(batch)
	// 手续费地址的钱是池外收益（不在 ledger balances 里），不走 DeductForPayout。
	if e.rawtx != nil {
		txid, rawtx, err := e.rawtx.PrepareSendMany(ctx, outputs)
		if err != nil {
			batch.Status = core.PaymentFailed
			_ = e.store.Save(batch)
			return "", err
		}
		batch.PlannedTxID, batch.RawTx, batch.Status = txid, rawtx, core.PaymentSent
		_ = e.store.Save(batch)
		if err := e.rawtx.Broadcast(ctx, rawtx); err != nil {
			return "", err
		}
		batch.TxID = txid
		_ = e.store.Save(batch)
		return txid, nil
	}
	txid, err := e.wallet.SendMany(ctx, outputs)
	if err != nil {
		batch.Status = core.PaymentFailed
		_ = e.store.Save(batch)
		return "", err
	}
	batch.TxID, batch.Status = txid, core.PaymentSent
	_ = e.store.Save(batch)
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
