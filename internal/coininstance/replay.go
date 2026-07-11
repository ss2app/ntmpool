package coininstance

import (
	"context"
	"database/sql"
	"time"

	"github.com/scashcc/ntmpool/internal/hashrate"
)

// replayShareHistory 把 PG shares 表里 since 之后的 share 按时间序回放进算力
// tracker，使进程重启对算力曲线/在线矿工数完全透明。created 升序回放保证
// Record 内部按 at 剪窗时时间单调。
//
// bucketsFrom 语义：at ≥ bucketsFrom 的 share 走 Record（buckets+即时窗口）；
// 之前的只走 SeedRecent（即时窗口）——该时段的桶已由 minerstats 种子回灌
// （seedFromStats），再 Record 会双计。全量回放（无种子）传 bucketsFrom=since。
func replayShareHistory(ctx context.Context, db *sql.DB, coin string, tr *hashrate.Tracker, since, bucketsFrom time.Time) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT miner, COALESCE(worker, ''), difficulty, created
		FROM shares
		WHERE poolid = $1 AND created > $2
		ORDER BY created`, coin, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			miner, worker string
			diff          float64
			created       sql.NullTime
		)
		if err := rows.Scan(&miner, &worker, &diff, &created); err != nil {
			return n, err
		}
		if !created.Valid {
			continue
		}
		if created.Time.Before(bucketsFrom) {
			tr.SeedRecent(miner, worker, diff, created.Time)
		} else {
			tr.Record(miner, worker, diff, created.Time)
		}
		n++
	}
	return n, rows.Err()
}
