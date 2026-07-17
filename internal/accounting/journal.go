package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"time"
)

const (
	journalPolicyPreRegistry = "pre-registry"
	journalAuditLimit        = 20
)

var (
	// ErrJournalUnbalanced 是 J1 借贷不平的哨兵错误。影子写调用方必须跳过 journal，
	// 但不得阻断同一事务里的既有业务变更。
	ErrJournalUnbalanced = errors.New("journal 借贷不平")
	// ErrJournalOverflow 表示 int64 聪域求和溢出，同样必须整笔跳过 journal。
	ErrJournalOverflow = errors.New("journal 金额求和溢出")
)

type journalLeg struct {
	account string
	address string
	delta   int64 // 正=借 DR，负=贷 CR。
}

type journalBuilder struct {
	kind          string
	businessKey   string
	policyVersion string
	memo          string
	legs          []journalLeg
}

func newJournalTx(kind, businessKey, policyVersion, memo string) *journalBuilder {
	return &journalBuilder{kind: kind, businessKey: businessKey, policyVersion: policyVersion, memo: memo}
}

// leg 忽略零金额腿，避免 fee=0 等场景产生无意义行。
func (j *journalBuilder) leg(account, address string, deltaSat int64) *journalBuilder {
	if deltaSat != 0 {
		j.legs = append(j.legs, journalLeg{account: account, address: address, delta: deltaSat})
	}
	return j
}

func (j *journalBuilder) validate() error {
	var sum int64
	for _, leg := range j.legs {
		if (leg.delta > 0 && sum > math.MaxInt64-leg.delta) ||
			(leg.delta < 0 && sum < math.MinInt64-leg.delta) {
			return ErrJournalOverflow
		}
		sum += leg.delta
	}
	if sum != 0 {
		return fmt.Errorf("%w: business_key=%s sum=%d", ErrJournalUnbalanced, j.businessKey, sum)
	}
	return nil
}

// writeTx 假定调用方已建立 SAVEPOINT；任一 SQL 错误由调用方回滚到保存点。
func (j *journalBuilder) writeTx(ctx context.Context, tx *sql.Tx, l *PGLedger) error {
	if err := j.validate(); err != nil {
		return err
	}
	var txref int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO journal_tx (poolid, business_key, kind, policy_version, memo)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		l.coin, j.businessKey, j.kind, j.policyVersion, nullIfEmpty(j.memo)).Scan(&txref); err != nil {
		return err
	}
	for _, leg := range j.legs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO journal_entry (txref, poolid, account, address, amount)
			VALUES ($1,$2,$3,$4,$5)`,
			txref, l.coin, leg.account, nullIfEmpty(leg.address), l.toStr(leg.delta)); err != nil {
			return err
		}
	}
	return nil
}

// writeJournalHard 供「journal 本身是承重结构」的操作使用（B2：坏账核销/人工调账/
// 事故登记与了结）：这些操作要么其投影效果被守恒式从 journal 取数抵消（written_off/
// manual_adjustment），要么 journal 就是操作本体（incident）——journal 写不进去时**必须
// 整笔失败回滚**，绝不能像影子事件那样跳过（跳过=守恒式误冻结或事故静默丢失）。
func (l *PGLedger) writeJournalHard(ctx context.Context, tx *sql.Tx, j *journalBuilder) error {
	return j.writeTx(ctx, tx, l)
}

// writeJournalShadow 在业务事务末尾追加 journal。校验或写库失败只发 P0 日志；
// PostgreSQL 语句失败会污染事务，因此数据库写入始终包在 SAVEPOINT 中。
// ⚠只用于影子事件（confirm/orphan/deduct/refund/direct）；B2 操作必须用 writeJournalHard。
func (l *PGLedger) writeJournalShadow(ctx context.Context, tx *sql.Tx, j *journalBuilder) {
	if err := j.validate(); err != nil {
		log.Printf("[P0] [会计 %s] journal_write_skipped business_key=%s kind=%s err=%v",
			l.coin, j.businessKey, j.kind, err)
		return
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT journal_shadow_write`); err != nil {
		log.Printf("[P0] [会计 %s] journal_savepoint_failed business_key=%s kind=%s err=%v",
			l.coin, j.businessKey, j.kind, err)
		return
	}
	if err := j.writeTx(ctx, tx, l); err != nil {
		_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT journal_shadow_write`)
		_, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT journal_shadow_write`)
		log.Printf("[P0] [会计 %s] journal_write_failed business_key=%s kind=%s err=%v rollback_err=%v release_err=%v action=skip",
			l.coin, j.businessKey, j.kind, err, rollbackErr, releaseErr)
		return
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT journal_shadow_write`); err != nil {
		log.Printf("[P0] [会计 %s] journal_savepoint_release_failed business_key=%s kind=%s err=%v",
			l.coin, j.businessKey, j.kind, err)
	}
}

// EnsureJournalOpening 用现有投影的同一事务快照写一次起账分录。它不改任何投影；
// 调用方必须按影子期规则把错误降级为启动告警。
func (l *PGLedger) EnsureJournalOpening(ctx context.Context) error {
	conn, err := l.h.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取 journal opening 连接: %w", err)
	}
	defer conn.Close()
	lockName := "journal-opening:" + l.coin
	// 先在事务外取得 session advisory lock，再建立 Repeatable Read 快照；否则等待锁时
	// 过早取得的事务快照可能看不到前一实例刚提交的 opening。
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockName); err != nil {
		return fmt.Errorf("锁定 journal opening: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, lockName)
	}()
	// Repeatable Read 保证 balances/debts/blocks/balance_changes 来自同一个一致快照；
	// 灰度部署期间即使仍有旧实例写投影，也不会拼出跨时点 opening。
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM journal_tx WHERE poolid=$1 AND kind='opening')`, l.coin).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit()
	}

	j := newJournalTx("opening", "opening:"+time.Now().UTC().Format("2006-01-02"),
		"opening-snapshot", "现有投影起账快照")
	rows, err := tx.QueryContext(ctx, `SELECT address, amount::text FROM balances WHERE poolid=$1 ORDER BY address`, l.coin)
	if err != nil {
		return err
	}
	for rows.Next() {
		var address, amount string
		if err := rows.Scan(&address, &amount); err != nil {
			rows.Close()
			return err
		}
		sat, err := l.parse(amount)
		if err != nil {
			rows.Close()
			return err
		}
		j.leg("miner:payable", address, -sat).leg("opening:equity", "", sat)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT address, COALESCE(SUM(remaining),0)::text
		FROM debts WHERE poolid=$1 GROUP BY address ORDER BY address`, l.coin)
	if err != nil {
		return err
	}
	for rows.Next() {
		var address, amount string
		if err := rows.Scan(&address, &amount); err != nil {
			rows.Close()
			return err
		}
		sat, err := l.parse(amount)
		if err != nil {
			rows.Close()
			return err
		}
		j.leg("debt:receivable", address, sat).leg("opening:equity", "", -sat)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var feeStr, revenueStr, paidStr string
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(feeamount),0)::text,
		  COALESCE(SUM(reward),0)::text,
		  COALESCE((SELECT -SUM(amount) FROM balance_changes
		            WHERE poolid=$1 AND usage IN ('payment','payment_refund')),0)::text
		FROM blocks WHERE poolid=$1 AND status='confirmed'`, l.coin).Scan(&feeStr, &revenueStr, &paidStr); err != nil {
		return err
	}
	fee, err := l.parse(feeStr)
	if err != nil {
		return err
	}
	revenue, err := l.parse(revenueStr)
	if err != nil {
		return err
	}
	paid, err := l.parse(paidStr)
	if err != nil {
		return err
	}
	j.leg("pool:fee_accrued", "", -fee).leg("opening:equity", "", fee)
	j.leg("block:revenue", "", revenue).leg("opening:equity", "", -revenue)
	j.leg("payout:settled", "", -paid).leg("opening:equity", "", paid)
	if err := j.writeTx(ctx, tx, l); err != nil {
		return fmt.Errorf("写 journal opening: %w", err)
	}
	return tx.Commit()
}
