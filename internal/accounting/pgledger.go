package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// PGLedger Postgres 会计（M4 多实例）。每币一个实例，共享同一个 *sql.DB。
//
// 与 MemLedger 分毫不差的语义（conformance 套件双实现跑同一组断言）：
//   - 金额 int64 聪计算，NUMERIC 字符串出入库；读回一律 ::text + parseAmount，
//     绝不信任 DB 侧格式化（NUMERIC dscale 会漂）。
//   - PPLNS 窗口 = shares 按插入序（id DESC）回溯累加权重；窗口不满归一化付满。
//   - ConfirmBlock/OrphanBlock 单事务 + 块行 FOR UPDATE：幂等、崩溃原子。
//   - 分账快照落 block_credits（抵债前全额），孤块回滚的唯一依据；
//     计提费记在 blocks.feeamount，随 status 翻转自动出入守恒式。
//
// share 写入走缓冲批写（docs/05 场景C：崩溃最多丢 ~flushEvery 的份额）；
// RecordBlock 前强制同步 flush——爆块 share 必须先落库（场景B 的意图记录）。
type PGLedger struct {
	h        *sql.DB
	coin     string // poolid
	decimals int
	pplnsN   float64
	instance string // shares.source（多实例溯源）

	mu  sync.Mutex // 只保护 share 缓冲；DB 一致性靠事务
	buf []core.Share
	wts []float64
}

var _ Ledger = (*PGLedger)(nil)

const shareFlushBatch = 500     // 单条 INSERT 最多行数
const shareBufHardCap = 200_000 // PG 长时间不可用时的内存保险丝

// NewPGLedger 构造（不起后台协程；周期 flush 由 StartFlusher 显式开启）。
func NewPGLedger(h *sql.DB, coin string, decimals int, pplnsN float64, instanceID string) *PGLedger {
	if pplnsN <= 0 {
		pplnsN = 2.0
	}
	return &PGLedger{h: h, coin: coin, decimals: decimals, pplnsN: pplnsN, instance: instanceID}
}

// StartFlusher 周期落盘 share 缓冲（ctx 结束时做最后一次 flush）。
func (l *PGLedger) StartFlusher(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Second
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := l.FlushShares(flushCtx); err != nil {
					log.Printf("[ledger %s] 关停 flush share 失败: %v", l.coin, err)
				}
				cancel()
				return
			case <-t.C:
				if err := l.FlushShares(ctx); err != nil {
					log.Printf("[ledger %s] flush share 失败（缓冲保留待重试）: %v", l.coin, err)
				}
			}
		}
	}()
}

func (l *PGLedger) toStr(sat int64) string        { return formatAmount(sat, l.decimals) }
func (l *PGLedger) parse(s string) (int64, error) { return parseAmount(s, l.decimals) }

// ---- share ----

func (l *PGLedger) RecordShare(_ context.Context, s core.Share, weight float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) >= shareBufHardCap {
		// PG 掉线过久：丢最旧（PPLNS 误差 < 记账中断；有日志有计数可审计）
		l.buf = l.buf[1:]
		l.wts = l.wts[1:]
	}
	l.buf = append(l.buf, s)
	l.wts = append(l.wts, weight)
	return nil
}

// FlushShares 同步落盘缓冲中的全部 share。失败时缓冲保留，下次重试。
func (l *PGLedger) FlushShares(ctx context.Context) error {
	l.mu.Lock()
	buf, wts := l.buf, l.wts
	l.buf, l.wts = nil, nil
	l.mu.Unlock()
	if len(buf) == 0 {
		return nil
	}
	if err := l.insertShares(ctx, buf, wts); err != nil {
		// 放回队头，保持顺序
		l.mu.Lock()
		l.buf = append(buf, l.buf...)
		l.wts = append(wts, l.wts...)
		l.mu.Unlock()
		return err
	}
	return nil
}

func (l *PGLedger) insertShares(ctx context.Context, buf []core.Share, wts []float64) error {
	for start := 0; start < len(buf); start += shareFlushBatch {
		end := start + shareFlushBatch
		if end > len(buf) {
			end = len(buf)
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO shares (poolid, blockheight, difficulty, miner, worker, useragent, ipaddress, source, solo, created) VALUES ")
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			base := len(args)
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
			s := buf[i]
			args = append(args, l.coin, int64(0), wts[i], s.Address, s.Worker, s.UserAgent, s.RemoteIP, l.instance, s.Solo, s.At.UTC())
		}
		if _, err := l.h.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// PruneShares 删除窗口重建不再需要的旧 share（默认保 3 天，docs/05 场景B 补建 round 用）。
func (l *PGLedger) PruneShares(ctx context.Context, retain time.Duration) error {
	_, err := l.h.ExecContext(ctx,
		`DELETE FROM shares WHERE poolid = $1 AND created < $2`, l.coin, time.Now().Add(-retain).UTC())
	return err
}

// ---- blocks ----

func (l *PGLedger) RecordBlock(ctx context.Context, b core.FoundBlock, rawHex string) error {
	// 爆块 share 属于意图记录，必须先同步落库（docs/05 场景C 例外条款）
	if err := l.FlushShares(ctx); err != nil {
		return fmt.Errorf("爆块前 flush share: %w", err)
	}
	// 块奖励可能为空：zoka 等 custom-http 链的 /mining/template 不下发 reward 字段
	// → FoundBlock.Reward=""。空串塞进 numeric 列会触发 22P02，让整条爆块落库失败；
	// blockSink 收到 err 即提前 return、永不 submit → 真块被丢 = 真矿工白挖
	// （2026-07-07 zoka 影子池 13 个真块被此 bug 白挖，详见 docs/BUG-zoka爆块漏判-排查.md）。
	// 一条块绝不能因金额格式化问题而丢——空奖励归一到 "0"（accrue-only 币无影响；
	// 打款币的真值应由适配器从节点 /blocks 回填，见该链适配器 TODO）。
	var reward any
	if b.RewardPending {
		reward = nil // nullable reward 即「权威值尚未回填」，ConfirmBlock 必须拒绝
	} else {
		rewardText := b.Reward
		if rewardText == "" {
			rewardText = "0"
		}
		if _, err := l.parse(rewardText); err != nil {
			return fmt.Errorf("块奖励金额非法: %w", err)
		}
		reward = rewardText
	}
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blocks (poolid, blockheight, networkdifficulty, status, transactionconfirmationdata,
		                    miner, worker, solo, reward, rawhex, effort, source, created, direct)
		VALUES ($1,$2,$3,'submitting',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (poolid, blockheight, type, transactionconfirmationdata) DO NOTHING`,
		l.coin, int64(b.Height), b.NetDiff, b.Hash, b.Finder, b.Worker, b.Solo, reward,
		nullIfEmpty(rawHex), b.Effort, l.instance, blockTime(b), b.Direct != nil); err != nil {
		return err
	}
	// 直付块（docs/07 §6）：credit/paid 分账快照在 record 时定死（confirm 只应用）。
	for _, dc := range b.Direct {
		cSat, err := l.parse(dc.Credit)
		if err != nil {
			return fmt.Errorf("直付 credit 金额非法 %q: %w", dc.Credit, err)
		}
		pSat, err := l.parse(dc.Paid)
		if err != nil {
			return fmt.Errorf("直付 paid 金额非法 %q: %w", dc.Paid, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO block_credits (poolid, blockhash, address, amount, paid) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (poolid, blockhash, address)
			DO UPDATE SET amount=EXCLUDED.amount, paid=EXCLUDED.paid, reversed=FALSE`,
			l.coin, b.Hash, dc.Address, l.toStr(cSat), l.toStr(pSat)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (l *PGLedger) MarkBlockPending(ctx context.Context, _, hash string) error {
	res, err := l.h.ExecContext(ctx, `
		UPDATE blocks SET status='pending'
		WHERE poolid=$1 AND transactionconfirmationdata=$2 AND status='submitting'`, l.coin, hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 已是 pending/confirmed（幂等）或不存在——不存在才是错
		var exists bool
		if err := l.h.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM blocks WHERE poolid=$1 AND transactionconfirmationdata=$2)`,
			l.coin, hash).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("块 %s 不存在", hash)
		}
	}
	return nil
}

func (l *PGLedger) UpdateBlockReward(ctx context.Context, _, hash, reward string) error {
	rewardSat, err := l.parse(reward)
	if err != nil {
		return fmt.Errorf("块 reward 金额非法 %q: %w", reward, err)
	}
	res, err := l.h.ExecContext(ctx, `
		UPDATE blocks SET reward=$3
		WHERE poolid=$1 AND transactionconfirmationdata=$2
		  AND status IN ('submitting','pending')`, l.coin, hash, l.toStr(rewardSat))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var status string
	err = l.h.QueryRowContext(ctx, `
		SELECT status FROM blocks WHERE poolid=$1 AND transactionconfirmationdata=$2`,
		l.coin, hash).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("块 %s 不存在", hash)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("块 %s 状态 %s 不允许回填 reward", hash, status)
}

// UpdateBlockHash 意图 hash → 节点受理后的权威 hash（blob 链「意图先落库」补录，docs/05 场景B）。
// block_credits 以 blockhash 为键（直付块 record 时已写行）→ 同步改。
func (l *PGLedger) UpdateBlockHash(ctx context.Context, _, oldHash, newHash string) error {
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE blocks SET transactionconfirmationdata=$3
		WHERE poolid=$1 AND transactionconfirmationdata=$2`, l.coin, oldHash, newHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE block_credits SET blockhash=$3
		WHERE poolid=$1 AND blockhash=$2`, l.coin, oldHash, newHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *PGLedger) PendingBlocks(ctx context.Context, _ string) ([]core.FoundBlock, error) {
	// submitting 也返回：崩溃残留的意图记录交给分类器与链上比对归位（docs/05 场景D-1）
	rows, err := l.h.QueryContext(ctx, `
		SELECT blockheight, transactionconfirmationdata, COALESCE(miner,''), COALESCE(worker,''),
		       reward::text, networkdifficulty, COALESCE(effort,0), solo, status, created
		FROM blocks WHERE poolid=$1 AND status IN ('submitting','pending') ORDER BY id`, l.coin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.FoundBlock
	for rows.Next() {
		b, err := scanBlock(rows, l.coin)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (l *PGLedger) ConfirmBlock(ctx context.Context, b core.FoundBlock, feePercent float64) error {
	if err := l.FlushShares(ctx); err != nil {
		return fmt.Errorf("confirm 前 flush share: %w", err)
	}
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var direct bool
	var blockID int64
	var storedReward sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT status, direct, id, reward::text FROM blocks
		WHERE poolid=$1 AND transactionconfirmationdata=$2 FOR UPDATE`,
		l.coin, b.Hash).Scan(&status, &direct, &blockID, &storedReward)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("块 %s 不存在", b.Hash)
	}
	if err != nil {
		return err
	}
	if status == string(core.BlockConfirmed) {
		return tx.Commit() // 幂等
	}
	if !storedReward.Valid {
		return fmt.Errorf("块 %s 的权威 reward 尚未回填", b.Hash)
	}

	rewardSat, err := l.parse(storedReward.String)
	if err != nil {
		return err
	}

	// 直付块（docs/07 §6）：按 record 时快照入账（+credit，−paid），费=E−Σcredit，
	// 忽略传入 feePercent。paid 走 usage='payment'（守恒式的已付项）+ payments 展示行
	// （batchid = −blocks.id，与打款引擎批次号空间天然隔离），txid=块 hash=
	// 「已随块直付」。不碰 debts 抵扣（carry 计划时已减掉 debts）。
	if direct {
		if err := l.confirmDirectTx(ctx, tx, b.Hash, blockID, rewardSat); err != nil {
			return err
		}
		return tx.Commit()
	}

	feeSat := int64(float64(rewardSat) * feePercent / 100.0)
	distributable := rewardSat - feeSat

	payouts := map[string]int64{}
	if b.Solo {
		payouts[b.Finder] = distributable
	} else {
		perAddr, err := l.windowByWeightTx(ctx, tx, l.pplnsN*b.NetworkDifficulty())
		if err != nil {
			return err
		}
		total := 0.0
		for _, w := range perAddr {
			total += w
		}
		if total <= 0 {
			payouts[b.Finder] = distributable // 无 share 兜底，不吞奖励
		} else {
			assigned := int64(0)
			addrs := sortedKeys(perAddr)
			for _, a := range addrs {
				share := int64(float64(distributable) * perAddr[a] / total)
				payouts[a] = share
				assigned += share
			}
			if rem := distributable - assigned; rem != 0 && len(addrs) > 0 {
				payouts[addrs[0]] += rem
			}
		}
	}

	// 快照（抵债前全额）→ 抵债 → 入余额 → 审计流水
	for a, amt := range payouts {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO block_credits (poolid, blockhash, address, amount) VALUES ($1,$2,$3,$4)
			ON CONFLICT (poolid, blockhash, address) DO UPDATE SET amount=EXCLUDED.amount, reversed=FALSE`,
			l.coin, b.Hash, a, l.toStr(amt)); err != nil {
			return err
		}
		credit, err := l.offsetDebtsTx(ctx, tx, a, amt)
		if err != nil {
			return err
		}
		if err := l.addBalanceTx(ctx, tx, a, credit, "reward", "block:"+b.Hash); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE blocks SET status='confirmed', confirmationprogress=1, feeamount=$3
		WHERE poolid=$1 AND transactionconfirmationdata=$2`, l.coin, b.Hash, l.toStr(feeSat)); err != nil {
		return err
	}
	return tx.Commit()
}

// confirmDirectTx 直付块入账（须在持有 blocks 行锁的事务内）。
func (l *PGLedger) confirmDirectTx(ctx context.Context, tx *sql.Tx, hash string, blockID, rewardSat int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT address, amount::text, COALESCE(paid,0)::text FROM block_credits
		WHERE poolid=$1 AND blockhash=$2 AND NOT reversed ORDER BY address`, l.coin, hash)
	if err != nil {
		return err
	}
	type dcRow struct {
		addr string
		cSat int64
		pSat int64
	}
	var dcs []dcRow
	for rows.Next() {
		var a, cStr, pStr string
		if err := rows.Scan(&a, &cStr, &pStr); err != nil {
			rows.Close()
			return err
		}
		cSat, err := l.parse(cStr)
		if err != nil {
			rows.Close()
			return err
		}
		pSat, err := l.parse(pStr)
		if err != nil {
			rows.Close()
			return err
		}
		dcs = append(dcs, dcRow{a, cSat, pSat})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var sumCredit int64
	for _, dc := range dcs {
		sumCredit += dc.cSat
		if err := l.addBalanceTx(ctx, tx, dc.addr, dc.cSat, "reward", "block:"+hash); err != nil {
			return err
		}
		if dc.pSat > 0 {
			if err := l.addBalanceTx(ctx, tx, dc.addr, -dc.pSat, "payment", "block:"+hash); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO payments (poolid, address, amount, batchid, transactionconfirmationdata, status, confirmations)
				VALUES ($1,$2,$3,$4,$5,'confirmed',1)
				ON CONFLICT (poolid, batchid, address) DO NOTHING`,
				l.coin, dc.addr, l.toStr(dc.pSat), -blockID, hash); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE blocks SET status='confirmed', confirmationprogress=1, feeamount=$3
		WHERE poolid=$1 AND transactionconfirmationdata=$2`, l.coin, hash, l.toStr(rewardSat-sumCredit))
	return err
}

func (l *PGLedger) OrphanBlock(ctx context.Context, b core.FoundBlock) error {
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var direct bool
	var blockID int64
	err = tx.QueryRowContext(ctx, `
		SELECT status, direct, id FROM blocks WHERE poolid=$1 AND transactionconfirmationdata=$2 FOR UPDATE`,
		l.coin, b.Hash).Scan(&status, &direct, &blockID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("块 %s 不存在", b.Hash)
	}
	if err != nil {
		return err
	}
	if status == string(core.BlockOrphaned) {
		return tx.Commit() // 幂等
	}

	// 已确认直付块回滚：coinbase 没上链=谁都没拿到——先退 paid（usage='payment_refund'
	// 对冲守恒式已付项）+ 删 payments 展示行，再走下面的通用 credit 快照反转。
	if status == string(core.BlockConfirmed) && direct {
		rows, err := tx.QueryContext(ctx, `
			SELECT address, COALESCE(paid,0)::text FROM block_credits
			WHERE poolid=$1 AND blockhash=$2 AND NOT reversed ORDER BY address`, l.coin, b.Hash)
		if err != nil {
			return err
		}
		type pr struct {
			addr string
			pSat int64
		}
		var prs []pr
		for rows.Next() {
			var a, pStr string
			if err := rows.Scan(&a, &pStr); err != nil {
				rows.Close()
				return err
			}
			pSat, err := l.parse(pStr)
			if err != nil {
				rows.Close()
				return err
			}
			prs = append(prs, pr{a, pSat})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range prs {
			if p.pSat <= 0 {
				continue
			}
			if err := l.addBalanceTx(ctx, tx, p.addr, p.pSat, "payment_refund", "block:"+b.Hash); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM payments WHERE poolid=$1 AND batchid=$2`, l.coin, -blockID); err != nil {
			return err
		}
	}

	if status == string(core.BlockConfirmed) {
		// 按快照回滚：扣得动的扣余额，扣不动的（已打款出去）记 debt
		rows, err := tx.QueryContext(ctx, `
			SELECT address, amount::text FROM block_credits
			WHERE poolid=$1 AND blockhash=$2 AND NOT reversed`, l.coin, b.Hash)
		if err != nil {
			return err
		}
		type cred struct {
			addr string
			amt  int64
		}
		var credits []cred
		for rows.Next() {
			var a, amtStr string
			if err := rows.Scan(&a, &amtStr); err != nil {
				rows.Close()
				return err
			}
			amt, err := l.parse(amtStr)
			if err != nil {
				rows.Close()
				return err
			}
			credits = append(credits, cred{a, amt})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range credits {
			bal, err := l.balanceForUpdateTx(ctx, tx, c.addr)
			if err != nil {
				return err
			}
			take := c.amt
			if take > bal {
				take = bal
			}
			if take > 0 {
				if err := l.addBalanceTx(ctx, tx, c.addr, -take, "orphan_reversal", "block:"+b.Hash); err != nil {
					return err
				}
			}
			if remain := c.amt - take; remain > 0 {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO debts (poolid, address, original, remaining, reason)
					VALUES ($1,$2,$3,$3,$4)`,
					l.coin, c.addr, l.toStr(remain), fmt.Sprintf("orphaned block %d (%s)", b.Height, b.Hash)); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE block_credits SET reversed=TRUE WHERE poolid=$1 AND blockhash=$2`, l.coin, b.Hash); err != nil {
			return err
		}
	}
	// 计提费随 status 翻转自动出账（守恒式只加 confirmed 块的 feeamount）
	if _, err := tx.ExecContext(ctx, `
		UPDATE blocks SET status='orphaned' WHERE poolid=$1 AND transactionconfirmationdata=$2`,
		l.coin, b.Hash); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- 打款侧 ----

func (l *PGLedger) PayableBalances(ctx context.Context, _ string, defaultThreshold float64, perAddr map[string]float64) (map[string]string, error) {
	rows, err := l.h.QueryContext(ctx,
		`SELECT address, amount::text FROM balances WHERE poolid=$1 AND amount > 0`, l.coin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	unit := amountUnit(l.decimals)
	for rows.Next() {
		var a, amtStr string
		if err := rows.Scan(&a, &amtStr); err != nil {
			return nil, err
		}
		sat, err := l.parse(amtStr)
		if err != nil {
			return nil, err
		}
		th := defaultThreshold
		if v, ok := perAddr[a]; ok && v > th {
			th = v
		}
		if sat >= int64(th*float64(unit)) {
			out[a] = l.toStr(sat)
		}
	}
	return out, rows.Err()
}

func (l *PGLedger) DeductForPayout(ctx context.Context, _ string, outputs map[string]string, batchID int64) error {
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 地址排序遍历：跨事务锁序一致防死锁
	for _, a := range sortedAmountKeys(outputs) {
		amt, err := l.parse(outputs[a])
		if err != nil {
			return err
		}
		bal, err := l.balanceForUpdateTx(ctx, tx, a)
		if err != nil {
			return err
		}
		if bal < amt {
			return fmt.Errorf("地址 %s 余额不足: 有 %s 需 %s", a, l.toStr(bal), outputs[a])
		}
		if err := l.addBalanceTx(ctx, tx, a, -amt, "payment", fmt.Sprintf("batch:%d", batchID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (l *PGLedger) RefundPayout(ctx context.Context, _ string, outputs map[string]string, batchID int64) error {
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// batch 级幂等：崩溃恢复可能重复退款同一批次（docs/05 场景A），只退一次
	var already bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM balance_changes
		              WHERE poolid=$1 AND usage='payment_refund' AND $2 = ANY(tags))`,
		l.coin, fmt.Sprintf("batch:%d", batchID)).Scan(&already); err != nil {
		return err
	}
	if already {
		return tx.Commit()
	}
	for _, a := range sortedAmountKeys(outputs) {
		amt, err := l.parse(outputs[a])
		if err != nil {
			return err
		}
		if err := l.addBalanceTx(ctx, tx, a, amt, "payment_refund", fmt.Sprintf("batch:%d", batchID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DirectPlanInputs 直付分账计划输入（docs/07 §6）：窗口快照 + 可用 carry。
func (l *PGLedger) DirectPlanInputs(ctx context.Context, _ string, windowWeight float64) (map[string]float64, map[string]string, error) {
	if err := l.FlushShares(ctx); err != nil {
		return nil, nil, err
	}
	tx, err := l.h.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	weights, err := l.windowByWeightTx(ctx, tx, windowWeight)
	if err != nil {
		return nil, nil, err
	}
	// carry = balance − 在飞直付预留（pending/submitting 直付块 Σmax(paid−credit,0)）− debts
	rows, err := tx.QueryContext(ctx, `
		SELECT bal.address, bal.amount::text,
		       COALESCE((SELECT SUM(GREATEST(bc.paid - bc.amount, 0)) FROM block_credits bc
		                 JOIN blocks bl ON bl.poolid = bc.poolid AND bl.transactionconfirmationdata = bc.blockhash
		                 WHERE bc.poolid = bal.poolid AND bc.address = bal.address
		                   AND bc.paid IS NOT NULL AND bl.status IN ('submitting','pending')), 0)::text,
		       COALESCE((SELECT SUM(d.remaining) FROM debts d
		                 WHERE d.poolid = bal.poolid AND d.address = bal.address), 0)::text
		FROM balances bal WHERE bal.poolid = $1 AND bal.amount > 0`, l.coin)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	carry := map[string]string{}
	for rows.Next() {
		var a, balStr, resStr, debtStr string
		if err := rows.Scan(&a, &balStr, &resStr, &debtStr); err != nil {
			return nil, nil, err
		}
		bal, err := l.parse(balStr)
		if err != nil {
			return nil, nil, err
		}
		res, err := l.parse(resStr)
		if err != nil {
			return nil, nil, err
		}
		debt, err := l.parse(debtStr)
		if err != nil {
			return nil, nil, err
		}
		if avail := bal - res - debt; avail > 0 {
			carry[a] = l.toStr(avail)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return weights, carry, tx.Commit()
}

// ---- 对账与快照 ----

func (l *PGLedger) Reconcile(ctx context.Context, _ string) (string, error) {
	if err := l.FlushShares(ctx); err != nil {
		return "", err
	}
	var confirmedStr, feesStr, balStr, debtStr, paidStr string
	err := l.h.QueryRowContext(ctx, `
		SELECT
		  COALESCE((SELECT SUM(reward)    FROM blocks   WHERE poolid=$1 AND status='confirmed'),0)::text,
		  COALESCE((SELECT SUM(feeamount) FROM blocks   WHERE poolid=$1 AND status='confirmed'),0)::text,
		  COALESCE((SELECT SUM(amount)    FROM balances WHERE poolid=$1),0)::text,
		  COALESCE((SELECT SUM(remaining) FROM debts    WHERE poolid=$1),0)::text,
		  COALESCE((SELECT -SUM(amount)   FROM balance_changes WHERE poolid=$1 AND usage IN ('payment','payment_refund')),0)::text`,
		l.coin).Scan(&confirmedStr, &feesStr, &balStr, &debtStr, &paidStr)
	if err != nil {
		return "", err
	}
	confirmed, err := l.parse(confirmedStr)
	if err != nil {
		return "", err
	}
	fees, err := l.parse(feesStr)
	if err != nil {
		return "", err
	}
	bal, err := l.parse(balStr)
	if err != nil {
		return "", err
	}
	debt, err := l.parse(debtStr)
	if err != nil {
		return "", err
	}
	paid, err := l.parse(paidStr)
	if err != nil {
		return "", err
	}
	delta := confirmed - paid - bal - fees + debt
	_, _ = l.h.ExecContext(ctx, `
		INSERT INTO reconciliations (poolid, confirmed_rewards, total_paid, total_balances, total_fees, in_flight, debts_net, delta)
		VALUES ($1,$2,$3,$4,$5,0,$6,$7)`,
		l.coin, confirmedStr, l.toStr(paid), l.toStr(bal), l.toStr(fees), l.toStr(debt), l.toStr(delta))
	return l.toStr(delta), nil
}

func (l *PGLedger) Snapshot(ctx context.Context, _ string) (Stats, error) {
	if err := l.FlushShares(ctx); err != nil {
		return Stats{}, err
	}
	st := Stats{Balances: map[string]string{}}
	rows, err := l.h.QueryContext(ctx,
		`SELECT address, amount::text FROM balances WHERE poolid=$1`, l.coin)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var a, amtStr string
		if err := rows.Scan(&a, &amtStr); err != nil {
			rows.Close()
			return st, err
		}
		sat, err := l.parse(amtStr)
		if err != nil {
			rows.Close()
			return st, err
		}
		st.Balances[a] = l.toStr(sat)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	var feesStr, debtStr, paidStr string
	var found, confirmed, orphaned, winShares int
	err = l.h.QueryRowContext(ctx, `
		SELECT
		  COALESCE((SELECT SUM(feeamount) FROM blocks WHERE poolid=$1 AND status='confirmed'),0)::text,
		  COALESCE((SELECT SUM(remaining) FROM debts  WHERE poolid=$1),0)::text,
		  COALESCE((SELECT -SUM(amount)   FROM balance_changes WHERE poolid=$1 AND usage IN ('payment','payment_refund')),0)::text,
		  (SELECT COUNT(*) FROM blocks WHERE poolid=$1),
		  (SELECT COUNT(*) FROM blocks WHERE poolid=$1 AND status='confirmed'),
		  (SELECT COUNT(*) FROM blocks WHERE poolid=$1 AND status='orphaned'),
		  (SELECT COUNT(*) FROM shares WHERE poolid=$1)`,
		l.coin).Scan(&feesStr, &debtStr, &paidStr, &found, &confirmed, &orphaned, &winShares)
	if err != nil {
		return st, err
	}
	fees, err := l.parse(feesStr)
	if err != nil {
		return st, err
	}
	debt, err := l.parse(debtStr)
	if err != nil {
		return st, err
	}
	paid, err := l.parse(paidStr)
	if err != nil {
		return st, err
	}
	st.TotalFees, st.DebtsNet, st.TotalPaid = l.toStr(fees), l.toStr(debt), l.toStr(paid)
	st.BlocksFound, st.Confirmed, st.Orphaned, st.WindowShares = found, confirmed, orphaned, winShares
	return st, nil
}

func (l *PGLedger) Blocks(ctx context.Context, _ string, offset, limit int) ([]core.FoundBlock, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	var total int
	if err := l.h.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM blocks WHERE poolid=$1`, l.coin).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := l.h.QueryContext(ctx, `
		SELECT blockheight, transactionconfirmationdata, COALESCE(miner,''), COALESCE(worker,''),
		       COALESCE(reward::text,'0'), networkdifficulty, COALESCE(effort,0), solo, status, created
		FROM blocks WHERE poolid=$1 ORDER BY id DESC OFFSET $2 LIMIT $3`, l.coin, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []core.FoundBlock
	for rows.Next() {
		b, err := scanBlock(rows, l.coin)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, rows.Err()
}

func (l *PGLedger) HasBlockHash(ctx context.Context, _ string, blockHash string) (bool, error) {
	var exists bool
	err := l.h.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM blocks WHERE poolid=$1 AND transactionconfirmationdata=$2)`,
		l.coin, blockHash).Scan(&exists)
	return exists, err
}

func (l *PGLedger) MinerSummary(ctx context.Context, _, addr string) (MinerSummary, bool, error) {
	var balStr, paidStr, debtStr string
	var hasAny bool
	err := l.h.QueryRowContext(ctx, `
		SELECT
		  COALESCE((SELECT amount FROM balances WHERE poolid=$1 AND address=$2),0)::text,
		  COALESCE((SELECT -SUM(amount) FROM balance_changes WHERE poolid=$1 AND address=$2 AND usage IN ('payment','payment_refund')),0)::text,
		  COALESCE((SELECT SUM(remaining) FROM debts WHERE poolid=$1 AND address=$2),0)::text,
		  EXISTS(SELECT 1 FROM balances WHERE poolid=$1 AND address=$2)
		    OR EXISTS(SELECT 1 FROM balance_changes WHERE poolid=$1 AND address=$2)
		    OR EXISTS(SELECT 1 FROM debts WHERE poolid=$1 AND address=$2)`,
		l.coin, addr).Scan(&balStr, &paidStr, &debtStr, &hasAny)
	if err != nil {
		return MinerSummary{}, false, err
	}
	if !hasAny {
		return MinerSummary{}, false, nil
	}
	bal, err := l.parse(balStr)
	if err != nil {
		return MinerSummary{}, false, err
	}
	paid, err := l.parse(paidStr)
	if err != nil {
		return MinerSummary{}, false, err
	}
	debt, err := l.parse(debtStr)
	if err != nil {
		return MinerSummary{}, false, err
	}
	return MinerSummary{Balance: l.toStr(bal), TotalPaid: l.toStr(paid), Debt: l.toStr(debt)}, true, nil
}

// ---- 事务内工具 ----

// windowByWeightTx 从末尾（id DESC）回溯累加权重到 windowWeight（MemLedger.windowByWeight 同义）。
func (l *PGLedger) windowByWeightTx(ctx context.Context, tx *sql.Tx, windowWeight float64) (map[string]float64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT miner, difficulty FROM shares WHERE poolid=$1 AND NOT solo ORDER BY id DESC`, l.coin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := 0.0
	perAddr := map[string]float64{}
	for rows.Next() {
		var a string
		var w float64
		if err := rows.Scan(&a, &w); err != nil {
			return nil, err
		}
		perAddr[a] += w
		acc += w
		if acc >= windowWeight {
			break
		}
	}
	// 提前 break 时丢弃剩余结果集
	rows.Close()
	return perAddr, nil
}

// offsetDebtsTx 抵扣地址欠款（老账先抵），返回抵扣后可入余额的金额。
func (l *PGLedger) offsetDebtsTx(ctx context.Context, tx *sql.Tx, addr string, amt int64) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, remaining::text FROM debts
		WHERE poolid=$1 AND address=$2 AND remaining > 0 ORDER BY id FOR UPDATE`, l.coin, addr)
	if err != nil {
		return 0, err
	}
	type debtRow struct {
		id  int64
		rem int64
	}
	var ds []debtRow
	for rows.Next() {
		var id int64
		var remStr string
		if err := rows.Scan(&id, &remStr); err != nil {
			rows.Close()
			return 0, err
		}
		rem, err := l.parse(remStr)
		if err != nil {
			rows.Close()
			return 0, err
		}
		ds = append(ds, debtRow{id, rem})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range ds {
		if amt <= 0 {
			break
		}
		take := d.rem
		if take > amt {
			take = amt
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE debts SET remaining = remaining - $3::numeric, updated = now() WHERE id = $2 AND poolid = $1`,
			l.coin, d.id, l.toStr(take)); err != nil {
			return 0, err
		}
		amt -= take
	}
	return amt, nil
}

// balanceForUpdateTx 锁定并返回地址当前余额（无行 = 0，不建行）。
func (l *PGLedger) balanceForUpdateTx(ctx context.Context, tx *sql.Tx, addr string) (int64, error) {
	var amtStr string
	err := tx.QueryRowContext(ctx,
		`SELECT amount::text FROM balances WHERE poolid=$1 AND address=$2 FOR UPDATE`, l.coin, addr).Scan(&amtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return l.parse(amtStr)
}

// addBalanceTx 余额增减（UPSERT）+ 审计流水一条。delta 可为负。
func (l *PGLedger) addBalanceTx(ctx context.Context, tx *sql.Tx, addr string, delta int64, usage, tag string) error {
	if delta == 0 {
		// 0 变动照记流水（reward=0 的地址也要可溯源），不动余额行
	} else {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO balances (poolid, address, amount) VALUES ($1,$2,$3::numeric)
			ON CONFLICT (poolid, address) DO UPDATE SET amount = balances.amount + EXCLUDED.amount, updated = now()`,
			l.coin, addr, l.toStr(delta)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO balance_changes (poolid, address, amount, usage, tags) VALUES ($1,$2,$3::numeric,$4,ARRAY[$5])`,
		l.coin, addr, l.toStr(delta), usage, tag)
	return err
}

// ---- 扫描工具 ----

type rowScanner interface{ Scan(dest ...any) error }

func scanBlock(r rowScanner, coin string) (core.FoundBlock, error) {
	var b core.FoundBlock
	var height int64
	var status string
	var created time.Time
	var reward sql.NullString
	if err := r.Scan(&height, &b.Hash, &b.Finder, &b.Worker, &reward, &b.NetDiff, &b.Effort, &b.Solo, &status, &created); err != nil {
		return b, err
	}
	b.Coin = coin
	b.Height = uint64(height)
	if reward.Valid {
		b.Reward = reward.String
	} else {
		b.RewardPending = true
	}
	b.FoundAt = created
	switch status {
	case "submitting", "pending":
		b.Status = core.BlockPending
	default:
		b.Status = core.BlockStatus(status)
	}
	return b, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func blockTime(b core.FoundBlock) time.Time {
	if b.FoundAt.IsZero() {
		return time.Now().UTC()
	}
	return b.FoundAt.UTC()
}

func sortedAmountKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	// 简单字典序（锁序一致即可）
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}
