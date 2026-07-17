package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

type configAuditIDContextKey struct{}

// WithConfigAuditID 把 admin 预写审计记录的唯一 id 传入账本，同时保持 Ledger 的公开
// 方法签名只承载原始业务参数。非 admin 调用未设置时，账本以 reason/memo 作为兼容回退 id。
func WithConfigAuditID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, configAuditIDContextKey{}, strings.TrimSpace(id))
}

func configAuditID(ctx context.Context, fallback string) string {
	if ctx != nil {
		if id, ok := ctx.Value(configAuditIDContextKey{}).(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return strings.TrimSpace(fallback)
}

func auditedMemo(ctx context.Context, memo string) string {
	id := configAuditID(ctx, "")
	if id == "" {
		return memo
	}
	return fmt.Sprintf("config_audit_id=%s %s", id, memo)
}

func checkedAdd(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("金额 int64 溢出")
	}
	return a + b, nil
}

func validateB2Common(address, amount, note string, parse func(string) (int64, error)) (int64, error) {
	if strings.TrimSpace(address) == "" {
		return 0, fmt.Errorf("地址不能为空")
	}
	if strings.TrimSpace(note) == "" {
		return 0, fmt.Errorf("reason/memo 不能为空")
	}
	amt, err := parse(amount)
	if err != nil {
		return 0, err
	}
	if amt <= 0 {
		return 0, fmt.Errorf("金额必须大于 0")
	}
	return amt, nil
}

func (l *MemLedger) hasJournalKeyLocked(key string) bool {
	for _, tx := range l.journal {
		if tx.key == key {
			return true
		}
	}
	return false
}

// WriteOffDebt 核销坏账；余额与 balance_changes 完全不动。
func (l *MemLedger) WriteOffDebt(ctx context.Context, coin, address, amount, reason string) error {
	amt, err := validateB2Common(address, amount, reason, l.parse)
	if err != nil {
		return err
	}
	auditID := configAuditID(ctx, reason)
	key := "debtwriteoff:" + address + ":" + auditID

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasJournalKeyLocked(key) {
		return fmt.Errorf("坏账核销已存在: %s", key)
	}
	remaining := l.debts[address]
	if amt > remaining {
		return fmt.Errorf("核销金额 %s 超过剩余债务 %s", amount, l.toStr(remaining))
	}
	writtenOff, err := checkedAdd(l.totalWrittenOff, amt)
	if err != nil {
		return err
	}
	l.debts[address] = remaining - amt
	l.totalWrittenOff = writtenOff
	l.appendJournal(coin, newJournalTx("debt_writeoff", key, journalPolicyPreRegistry, auditedMemo(ctx, reason)).
		leg("orphan:loss", "", amt).
		leg("debt:receivable", address, -amt))
	return nil
}

// ManualAdjust 人工调余额，始终同步追加 balance_changes 与配平 journal。
func (l *MemLedger) ManualAdjust(ctx context.Context, coin, address, amount string, credit bool, reason string) error {
	amt, err := validateB2Common(address, amount, reason, l.parse)
	if err != nil {
		return err
	}
	auditID := configAuditID(ctx, reason)
	key := "manual:" + auditID

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasJournalKeyLocked(key) {
		return fmt.Errorf("人工调账已存在: %s", key)
	}
	current := l.balances[address]
	delta := amt
	usage := "manual_credit"
	if !credit {
		if current < amt {
			return fmt.Errorf("余额不足: 当前 %s，拟扣 %s", l.toStr(current), amount)
		}
		delta = -amt
		usage = "manual_debit"
	}
	next, err := checkedAdd(current, delta)
	if err != nil {
		return err
	}
	manualTotal, err := checkedAdd(l.totalManualAdjustment, delta)
	if err != nil {
		return err
	}
	l.balances[address] = next
	l.totalManualAdjustment = manualTotal
	l.addBalanceChange(address, delta, usage, "admin:"+auditID)
	j := newJournalTx("manual", key, journalPolicyPreRegistry, auditedMemo(ctx, reason))
	if credit {
		j.leg("manual:adjustment", "", amt).leg("miner:payable", address, -amt)
	} else {
		j.leg("miner:payable", address, amt).leg("manual:adjustment", "", -amt)
	}
	l.appendJournal(coin, j)
	return nil
}

func validateIncident(id, value, amount, memo string, allowed map[string]struct{}, parse func(string) (int64, error)) (int64, error) {
	if strings.TrimSpace(id) == "" {
		return 0, fmt.Errorf("incident id 不能为空")
	}
	if _, ok := allowed[value]; !ok {
		return 0, fmt.Errorf("非法 incident 类型/结果 %q", value)
	}
	return validateB2Common(id, amount, memo, parse)
}

var incidentKinds = map[string]struct{}{"overpay": {}, "wrong_address": {}}
var incidentOutcomes = map[string]struct{}{"recovered": {}, "writeoff": {}}

func (l *MemLedger) RecordIncident(ctx context.Context, coin, id, kind, recipient, amount, memo string) error {
	amt, err := validateIncident(id, kind, amount, memo, incidentKinds, l.parse)
	if err != nil {
		return err
	}
	if strings.TrimSpace(recipient) == "" {
		return fmt.Errorf("recipient 不能为空")
	}
	key := "incident:" + id
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasJournalKeyLocked(key) {
		return fmt.Errorf("incident id 已存在: %s", id)
	}
	journalMemo := fmt.Sprintf("incident_kind=%s %s", kind, memo)
	l.appendJournal(coin, newJournalTx("incident_record", key, journalPolicyPreRegistry, auditedMemo(ctx, journalMemo)).
		leg("recipient:receivable", recipient, amt).
		leg("incident:outflow", "", -amt))
	return nil
}

func (l *MemLedger) incidentStateLocked(id string) (recipient string, original, resolved int64, err error) {
	recordKey := "incident:" + id
	for _, tx := range l.journal {
		if tx.key != recordKey || tx.kind != "incident_record" {
			continue
		}
		for _, leg := range tx.legs {
			if leg.account == "recipient:receivable" && leg.delta > 0 {
				recipient, original = leg.address, leg.delta
				break
			}
		}
		break
	}
	if recipient == "" || original <= 0 {
		return "", 0, 0, fmt.Errorf("incident 不存在: %s", id)
	}
	for _, outcome := range []string{"recovered", "writeoff"} {
		key := recordKey + ":" + outcome
		for _, tx := range l.journal {
			if tx.key != key {
				continue
			}
			for _, leg := range tx.legs {
				if leg.account == "recipient:receivable" && leg.address == recipient && leg.delta < 0 {
					resolved, err = checkedAdd(resolved, -leg.delta)
					if err != nil {
						return "", 0, 0, err
					}
				}
			}
		}
	}
	return recipient, original, resolved, nil
}

func (l *MemLedger) ResolveIncident(ctx context.Context, coin, id, outcome, amount, memo string) error {
	amt, err := validateIncident(id, outcome, amount, memo, incidentOutcomes, l.parse)
	if err != nil {
		return err
	}
	key := "incident:" + id + ":" + outcome
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasJournalKeyLocked(key) {
		return fmt.Errorf("incident 结果已登记: id=%s outcome=%s", id, outcome)
	}
	recipient, original, resolved, err := l.incidentStateLocked(id)
	if err != nil {
		return err
	}
	if amt > original-resolved {
		return fmt.Errorf("了结金额 %s 超过未了结余额 %s", amount, l.toStr(original-resolved))
	}
	j := newJournalTx("incident_resolve", key, journalPolicyPreRegistry, auditedMemo(ctx, memo))
	if outcome == "recovered" {
		j.leg("incident:outflow", "", amt)
	} else {
		j.leg("incident:loss", "", amt)
	}
	j.leg("recipient:receivable", recipient, -amt)
	l.appendJournal(coin, j)
	return nil
}

func (l *PGLedger) lockJournalKeyTx(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, l.coin+":"+key)
	return err
}

func (l *PGLedger) journalKeyExistsTx(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM journal_tx WHERE poolid=$1 AND business_key=$2)`, l.coin, key).Scan(&exists)
	return exists, err
}

func (l *PGLedger) WriteOffDebt(ctx context.Context, _ string, address, amount, reason string) error {
	amt, err := validateB2Common(address, amount, reason, l.parse)
	if err != nil {
		return err
	}
	auditID := configAuditID(ctx, reason)
	key := "debtwriteoff:" + address + ":" + auditID
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.lockJournalKeyTx(ctx, tx, key); err != nil {
		return err
	}
	if exists, err := l.journalKeyExistsTx(ctx, tx, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("坏账核销已存在: %s", key)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, remaining::text FROM debts
		WHERE poolid=$1 AND address=$2 AND remaining > 0 ORDER BY id FOR UPDATE`, l.coin, address)
	if err != nil {
		return err
	}
	type debtRow struct {
		id, remaining int64
	}
	var debts []debtRow
	var total int64
	for rows.Next() {
		var id int64
		var remainingStr string
		if err := rows.Scan(&id, &remainingStr); err != nil {
			rows.Close()
			return err
		}
		remaining, err := l.parse(remainingStr)
		if err != nil {
			rows.Close()
			return err
		}
		total, err = checkedAdd(total, remaining)
		if err != nil {
			rows.Close()
			return err
		}
		debts = append(debts, debtRow{id: id, remaining: remaining})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if amt > total {
		return fmt.Errorf("核销金额 %s 超过剩余债务 %s", amount, l.toStr(total))
	}
	left := amt
	for _, debt := range debts {
		if left == 0 {
			break
		}
		take := debt.remaining
		if take > left {
			take = left
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE debts SET remaining=remaining-$3::numeric, updated=now()
			WHERE poolid=$1 AND id=$2`, l.coin, debt.id, l.toStr(take)); err != nil {
			return err
		}
		left -= take
	}
	// journal 承重：守恒式的 written_off 从 journal 取数，写不进去必须整笔失败。
	if err := l.writeJournalHard(ctx, tx, newJournalTx("debt_writeoff", key, journalPolicyPreRegistry, auditedMemo(ctx, reason)).
		leg("orphan:loss", "", amt).
		leg("debt:receivable", address, -amt)); err != nil {
		return fmt.Errorf("写核销 journal: %w", err)
	}
	return tx.Commit()
}

func (l *PGLedger) ManualAdjust(ctx context.Context, _ string, address, amount string, credit bool, reason string) error {
	amt, err := validateB2Common(address, amount, reason, l.parse)
	if err != nil {
		return err
	}
	auditID := configAuditID(ctx, reason)
	key := "manual:" + auditID
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.lockJournalKeyTx(ctx, tx, key); err != nil {
		return err
	}
	if exists, err := l.journalKeyExistsTx(ctx, tx, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("人工调账已存在: %s", key)
	}
	current, err := l.balanceForUpdateTx(ctx, tx, address)
	if err != nil {
		return err
	}
	delta := amt
	usage := "manual_credit"
	if !credit {
		if current < amt {
			return fmt.Errorf("余额不足: 当前 %s，拟扣 %s", l.toStr(current), amount)
		}
		delta = -amt
		usage = "manual_debit"
	} else if _, err := checkedAdd(current, amt); err != nil {
		return err
	}
	if err := l.addBalanceTx(ctx, tx, address, delta, usage, "admin:"+auditID); err != nil {
		return err
	}
	j := newJournalTx("manual", key, journalPolicyPreRegistry, auditedMemo(ctx, reason))
	if credit {
		j.leg("manual:adjustment", "", amt).leg("miner:payable", address, -amt)
	} else {
		j.leg("miner:payable", address, amt).leg("manual:adjustment", "", -amt)
	}
	// journal 承重：守恒式的 manual_adjustment 从 journal 取数，J3 的 miner:payable 也依赖它。
	if err := l.writeJournalHard(ctx, tx, j); err != nil {
		return fmt.Errorf("写人工调账 journal: %w", err)
	}
	return tx.Commit()
}

func (l *PGLedger) RecordIncident(ctx context.Context, _ string, id, kind, recipient, amount, memo string) error {
	amt, err := validateIncident(id, kind, amount, memo, incidentKinds, l.parse)
	if err != nil {
		return err
	}
	if strings.TrimSpace(recipient) == "" {
		return fmt.Errorf("recipient 不能为空")
	}
	key := "incident:" + id
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.lockJournalKeyTx(ctx, tx, key); err != nil {
		return err
	}
	if exists, err := l.journalKeyExistsTx(ctx, tx, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("incident id 已存在: %s", id)
	}
	journalMemo := fmt.Sprintf("incident_kind=%s %s", kind, memo)
	// journal 承重：事故登记的唯一效果就是这笔 journal，写不进去=什么都没发生，必须失败。
	if err := l.writeJournalHard(ctx, tx, newJournalTx("incident_record", key, journalPolicyPreRegistry, auditedMemo(ctx, journalMemo)).
		leg("recipient:receivable", recipient, amt).
		leg("incident:outflow", "", -amt)); err != nil {
		return fmt.Errorf("写事故登记 journal: %w", err)
	}
	return tx.Commit()
}

func (l *PGLedger) ResolveIncident(ctx context.Context, _ string, id, outcome, amount, memo string) error {
	amt, err := validateIncident(id, outcome, amount, memo, incidentOutcomes, l.parse)
	if err != nil {
		return err
	}
	recordKey := "incident:" + id
	key := recordKey + ":" + outcome
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.lockJournalKeyTx(ctx, tx, recordKey); err != nil {
		return err
	}
	if exists, err := l.journalKeyExistsTx(ctx, tx, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("incident 结果已登记: id=%s outcome=%s", id, outcome)
	}
	var txref int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM journal_tx
		WHERE poolid=$1 AND business_key=$2 AND kind='incident_record' FOR UPDATE`,
		l.coin, recordKey).Scan(&txref); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("incident 不存在: %s", id)
	} else if err != nil {
		return err
	}
	var recipient, originalStr string
	if err := tx.QueryRowContext(ctx, `
		SELECT address, amount::text FROM journal_entry
		WHERE txref=$1 AND poolid=$2 AND account='recipient:receivable' AND amount>0`,
		txref, l.coin).Scan(&recipient, &originalStr); err != nil {
		return fmt.Errorf("读取 incident 登记分录: %w", err)
	}
	original, err := l.parse(originalStr)
	if err != nil {
		return err
	}
	var resolvedStr string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(-SUM(je.amount),0)::text
		FROM journal_tx jt JOIN journal_entry je ON je.txref=jt.id
		WHERE jt.poolid=$1 AND jt.business_key IN ($2,$3)
		  AND je.account='recipient:receivable' AND je.address=$4`,
		l.coin, recordKey+":recovered", recordKey+":writeoff", recipient).Scan(&resolvedStr); err != nil {
		return err
	}
	resolved, err := l.parse(resolvedStr)
	if err != nil {
		return err
	}
	if resolved < 0 || resolved > original {
		return fmt.Errorf("incident 已了结金额异常: %s", resolvedStr)
	}
	if amt > original-resolved {
		return fmt.Errorf("了结金额 %s 超过未了结余额 %s", amount, l.toStr(original-resolved))
	}
	j := newJournalTx("incident_resolve", key, journalPolicyPreRegistry, auditedMemo(ctx, memo))
	if outcome == "recovered" {
		j.leg("incident:outflow", "", amt)
	} else {
		j.leg("incident:loss", "", amt)
	}
	j.leg("recipient:receivable", recipient, -amt)
	// journal 承重：了结记录同为操作本体，必须写成功才算数。
	if err := l.writeJournalHard(ctx, tx, j); err != nil {
		return fmt.Errorf("写事故了结 journal: %w", err)
	}
	return tx.Commit()
}
