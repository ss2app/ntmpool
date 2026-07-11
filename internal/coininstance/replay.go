package coininstance

import (
	"context"
	"database/sql"

	"github.com/scashcc/ntmpool/internal/hashrate"
)

// replayShareHistory 把 PG shares 表里最近 24h 的 share 按时间序回放进算力
// tracker（buckets + 即时窗口），使进程重启对算力曲线/在线矿工数完全透明。
// created 升序回放保证 Record 内部按 at 剪窗时时间单调。
func replayShareHistory(ctx context.Context, db *sql.DB, coin string, tr *hashrate.Tracker) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT miner, COALESCE(worker, ''), difficulty, created
		FROM shares
		WHERE poolid = $1 AND created > now() - interval '24 hours'
		ORDER BY created`, coin)
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
		tr.Record(miner, worker, diff, created.Time)
		n++
	}
	return n, rows.Err()
}
