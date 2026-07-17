package accounting

import (
	"context"
	"fmt"
	"sort"

	"github.com/scashcc/ntmpool/internal/core"
)

// JournalShadowAudit 对五个阶段 1 科目执行全 SQL 聚合。任一查询失败时 checked=false，
// 调用方不得告警或冻结；err 只供诊断与测试观察。
func (l *PGLedger) JournalShadowAudit(ctx context.Context, _ string) ([]JournalMismatch, bool, error) {
	queries := []string{
		`WITH j AS (
		   SELECT address, SUM(amount) amount FROM journal_entry
		   WHERE poolid=$1 AND account='miner:payable' GROUP BY address
		 ), p AS (
		   SELECT address, amount FROM balances WHERE poolid=$1
		 )
		 SELECT 'miner:payable', COALESCE(j.address,p.address,''),
		        COALESCE(-j.amount,0)::text, COALESCE(p.amount,0)::text
		 FROM j FULL OUTER JOIN p USING (address)
		 WHERE COALESCE(-j.amount,0) <> COALESCE(p.amount,0)
		 ORDER BY COALESCE(j.address,p.address) LIMIT $2`,
		`WITH j AS (
		   SELECT address, SUM(amount) amount FROM journal_entry
		   WHERE poolid=$1 AND account='debt:receivable' GROUP BY address
		 ), p AS (
		   SELECT address, SUM(remaining) amount FROM debts WHERE poolid=$1 GROUP BY address
		 )
		 SELECT 'debt:receivable', COALESCE(j.address,p.address,''),
		        COALESCE(j.amount,0)::text, COALESCE(p.amount,0)::text
		 FROM j FULL OUTER JOIN p USING (address)
		 WHERE COALESCE(j.amount,0) <> COALESCE(p.amount,0)
		 ORDER BY COALESCE(j.address,p.address) LIMIT $2`,
		`WITH v AS (
		   SELECT COALESCE(-(SELECT SUM(amount) FROM journal_entry
		                    WHERE poolid=$1 AND account='pool:fee_accrued'),0) journal_amount,
		          COALESCE((SELECT SUM(feeamount) FROM blocks
		                    WHERE poolid=$1 AND status='confirmed'),0) projection_amount
		 )
		 SELECT 'pool:fee_accrued','',journal_amount::text,projection_amount::text
		 FROM v WHERE journal_amount <> projection_amount LIMIT $2`,
		`WITH v AS (
		   SELECT COALESCE((SELECT SUM(amount) FROM journal_entry
		                    WHERE poolid=$1 AND account='block:revenue'),0) journal_amount,
		          COALESCE((SELECT SUM(reward) FROM blocks
		                    WHERE poolid=$1 AND status='confirmed'),0) projection_amount
		 )
		 SELECT 'block:revenue','',journal_amount::text,projection_amount::text
		 FROM v WHERE journal_amount <> projection_amount LIMIT $2`,
		`WITH v AS (
		   SELECT COALESCE(-(SELECT SUM(amount) FROM journal_entry
		                    WHERE poolid=$1 AND account='payout:settled'),0) journal_amount,
		          COALESCE(-(SELECT SUM(amount) FROM balance_changes
		                    WHERE poolid=$1 AND usage IN ('payment','payment_refund')),0) projection_amount
		 )
		 SELECT 'payout:settled','',journal_amount::text,projection_amount::text
		 FROM v WHERE journal_amount <> projection_amount LIMIT $2`,
	}
	var out []JournalMismatch
	for _, query := range queries {
		rows, err := l.h.QueryContext(ctx, query, l.coin, journalAuditLimit)
		if err != nil {
			return nil, false, fmt.Errorf("J3 聚合查询: %w", err)
		}
		for rows.Next() {
			var account, address, journalStr, projectionStr string
			if err := rows.Scan(&account, &address, &journalStr, &projectionStr); err != nil {
				rows.Close()
				return nil, false, fmt.Errorf("J3 扫描: %w", err)
			}
			journalSat, err := l.parse(journalStr)
			if err != nil {
				rows.Close()
				return nil, false, fmt.Errorf("J3 journal 金额: %w", err)
			}
			projectionSat, err := l.parse(projectionStr)
			if err != nil {
				rows.Close()
				return nil, false, fmt.Errorf("J3 projection 金额: %w", err)
			}
			out = append(out, JournalMismatch{
				Account: account, Address: address,
				JournalAmount: l.toStr(journalSat), ProjectionAmount: l.toStr(projectionSat),
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, false, fmt.Errorf("J3 结果集: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, false, fmt.Errorf("J3 关闭结果集: %w", err)
		}
	}
	return out, true, nil
}

func (l *MemLedger) JournalShadowAudit(_ context.Context, _ string) ([]JournalMismatch, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	journal := map[string]map[string]int64{}
	for _, jt := range l.journal {
		for _, leg := range jt.legs {
			byAddr := journal[leg.account]
			if byAddr == nil {
				byAddr = map[string]int64{}
				journal[leg.account] = byAddr
			}
			byAddr[leg.address] += leg.delta
		}
	}
	var out []JournalMismatch
	appendPerAddress := func(account string, sign int64, projection map[string]int64) {
		keys := make(map[string]struct{}, len(journal[account])+len(projection))
		for address := range journal[account] {
			keys[address] = struct{}{}
		}
		for address := range projection {
			keys[address] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for address := range keys {
			ordered = append(ordered, address)
		}
		sort.Strings(ordered)
		added := 0
		for _, address := range ordered {
			jv, pv := sign*journal[account][address], projection[address]
			if jv == pv {
				continue
			}
			out = append(out, JournalMismatch{Account: account, Address: address,
				JournalAmount: l.toStr(jv), ProjectionAmount: l.toStr(pv)})
			added++
			if added == journalAuditLimit {
				break
			}
		}
	}
	appendPerAddress("miner:payable", -1, l.balances)
	appendPerAddress("debt:receivable", 1, l.debts)

	var revenue int64
	for _, mb := range l.blocks {
		if mb.b.Status != core.BlockConfirmed {
			continue
		}
		sat, err := l.parse(mb.b.Reward)
		if err != nil {
			return nil, false, err
		}
		revenue += sat
	}
	scalars := []struct {
		account    string
		sign       int64
		projection int64
	}{
		{"pool:fee_accrued", -1, l.totalFees},
		{"block:revenue", 1, revenue},
		{"payout:settled", -1, l.totalPaid},
	}
	for _, item := range scalars {
		jv := item.sign * journal[item.account][""]
		if jv != item.projection {
			out = append(out, JournalMismatch{Account: item.account,
				JournalAmount: l.toStr(jv), ProjectionAmount: l.toStr(item.projection)})
		}
	}
	return out, true, nil
}
