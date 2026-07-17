package payout

import (
	"context"
	"log"

	"github.com/scashcc/ntmpool/internal/accounting"
	"github.com/scashcc/ntmpool/internal/adapter"
)

const chainAuditReorgOverlap = uint64(6)

// ChainAuditResult 只包含本轮能够确证的异常。Checked=false 表示能力不足或查询失败；
// 调用方此时不得清除旧异常状态，更不得据此冻结。
type ChainAuditResult struct {
	OutboundChecked bool
	CoinbaseChecked bool
	UnknownOutbound []adapter.OutboundTx
	MissingRounds   []adapter.CoinbaseReceipt
}

// ChainToBookReconciler 把钱包链上事实反查到 payout intent / blocks round。
// 它本身严格只读：不改账、不补 round；冻结与事件由 Engine 统一执行。
type ChainToBookReconciler struct {
	coin     string
	auditor  adapter.ChainAuditor
	coinbase adapter.CoinbaseAuditor
	store    BatchStore
	ledger   accounting.Ledger

	outboundSince             uint64
	coinbaseSince             uint64
	unavailableLogged         bool
	coinbaseUnavailableLogged bool
}

func NewChainToBookReconciler(coin string, wallet adapter.WalletAdapter, store BatchStore, ledger accounting.Ledger) *ChainToBookReconciler {
	r := &ChainToBookReconciler{coin: coin, store: store, ledger: ledger}
	r.auditor, _ = wallet.(adapter.ChainAuditor)
	r.coinbase, _ = wallet.(adapter.CoinbaseAuditor)
	return r
}

// Run 执行一轮只读反向对账。查链、查批次或查 blocks 任一步失败都只记日志并跳过；
// 只有完整完成匹配后才返回可用于冻结的 UnknownOutbound。
func (r *ChainToBookReconciler) Run(ctx context.Context) ChainAuditResult {
	var result ChainAuditResult
	if r.auditor == nil {
		if !r.unavailableLogged {
			log.Printf("[payout %s] chain_audit_unavailable coin=%s", r.coin, r.coin)
			r.unavailableLogged = true
		}
	} else {
		r.auditOutbound(ctx, &result)
	}

	if r.coinbase == nil {
		if !r.coinbaseUnavailableLogged {
			log.Printf("[payout %s] chain_audit_coinbase_unavailable coin=%s", r.coin, r.coin)
			r.coinbaseUnavailableLogged = true
		}
	} else {
		r.auditCoinbase(ctx, &result)
	}
	return result
}

func (r *ChainToBookReconciler) auditOutbound(ctx context.Context, result *ChainAuditResult) {
	txs, err := r.auditor.ListRecentOutbound(ctx, r.outboundSince)
	if err != nil {
		log.Printf("[payout %s] chain_audit_outbound_failed（不冻结）: %v", r.coin, err)
		return
	}
	var maxHeight uint64
	unknown := make([]adapter.OutboundTx, 0)
	for _, tx := range txs {
		if tx.Height > maxHeight {
			maxHeight = tx.Height
		}
		if tx.TxID == "" {
			continue // 无稳定反查键，不能确证未知，安全跳过
		}
		if onlyOwnWalletOutputs(tx.Outputs) {
			continue // 全部输出均已确证属于本钱包：找零/自转/整备，不是未知外流
		}
		batch, found, err := r.store.FindByTxID(tx.TxID)
		if err != nil {
			// 任一反查失败，本轮整体不能证明“未知”；丢弃已收集结果，不冻结。
			log.Printf("[payout %s] chain_audit_batch_lookup_failed（不冻结）: %v", r.coin, err)
			return
		}
		if found && batch != nil && chainAuditBatchKind(batch.Kind) {
			continue
		}
		unknown = append(unknown, tx)
	}
	result.UnknownOutbound = unknown
	result.OutboundChecked = true
	r.outboundSince = advanceAuditCursor(r.outboundSince, maxHeight)
}

func (r *ChainToBookReconciler) auditCoinbase(ctx context.Context, result *ChainAuditResult) {
	receipts, err := r.coinbase.ListRecentCoinbase(ctx, r.coinbaseSince)
	if err != nil {
		log.Printf("[payout %s] chain_audit_coinbase_failed（只跳过 missing-round 检查）: %v", r.coin, err)
		return
	}
	var maxHeight uint64
	for _, receipt := range receipts {
		if receipt.Height > maxHeight {
			maxHeight = receipt.Height
		}
		if receipt.BlockHash == "" {
			continue
		}
		exists, err := r.ledger.HasBlockHash(ctx, r.coin, receipt.BlockHash)
		if err != nil {
			log.Printf("[payout %s] chain_audit_round_lookup_failed（只告警方向跳过）: %v", r.coin, err)
			return
		}
		if !exists {
			result.MissingRounds = append(result.MissingRounds, receipt)
		}
	}
	result.CoinbaseChecked = true
	r.coinbaseSince = advanceAuditCursor(r.coinbaseSince, maxHeight)
}

func chainAuditBatchKind(kind string) bool {
	switch kind {
	case "payout", "fee_collect", "fee_sweep", "consolidate":
		return true
	default:
		return false
	}
}

func onlyOwnWalletOutputs(outputs []adapter.OutboundOutput) bool {
	if len(outputs) == 0 {
		return false
	}
	for _, output := range outputs {
		if !output.IsMine {
			return false
		}
	}
	return true
}

func advanceAuditCursor(current, observed uint64) uint64 {
	if observed <= chainAuditReorgOverlap {
		return current
	}
	next := observed - chainAuditReorgOverlap
	if next > current {
		return next
	}
	return current
}
