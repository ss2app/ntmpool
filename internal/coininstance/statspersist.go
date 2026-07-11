package coininstance

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// 算力曲线持久化（用户拍板方案，2026-07-11）：
//   - 每个曲线桶（默认 10min）翻转后，把「刚完成的桶」写进 poolstats（池级样点
//     + 网络真值快照）与 minerstats（per-矿工 per-worker 明细）；
//   - 启动时优先从 minerstats 回灌 24h 曲线（O(矿工×144) 恒定，与 shares 表保留
//     策略解耦——将来 shares 可按 PPLNS 窗口裁剪，曲线不断）；
//   - shares 回放降级为：种子之后的增量段 + 即时窗口段（见 replay.go bucketsFrom）。
// 幂等：两表带唯一索引 + ON CONFLICT DO NOTHING，崩溃重写/双实例竞写不双计。
// 真实数据铁律：写的就是 tracker 里实测 share 聚合出的桶，非估算。

const statsRetention = 30 * 24 * time.Hour // 144 行/天/币，30 天也只有 ~4.3k 行

// seedFromStats 从 minerstats 回灌最近 24h 曲线桶。返回（最新种子桶起点, 行数）。
// 池级曲线 = Σ 矿工桶（与实时口径同构），故只回灌 minerstats 即可，poolstats
// 仅作网络快照历史与外部报表，不参与回灌（避免双计）。
func seedFromStats(ctx context.Context, db *sql.DB, coin string, tr TrackerSeeder) (time.Time, int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT miner, worker, hashrate, sharespersecond, created
		FROM minerstats
		WHERE poolid = $1 AND created > now() - interval '24 hours'
		ORDER BY created`, coin)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer rows.Close()
	var last time.Time
	n := 0
	for rows.Next() {
		var (
			miner, worker string
			hr, sps       float64
			created       sql.NullTime
		)
		if err := rows.Scan(&miner, &worker, &hr, &sps, &created); err != nil {
			return last, n, err
		}
		if !created.Valid {
			continue
		}
		tr.SeedBucket(created.Time, miner, worker, hr, sps)
		if created.Time.After(last) {
			last = created.Time
		}
		n++
	}
	return last, n, rows.Err()
}

// TrackerSeeder / TrackerBucketSource：statspersist 对 tracker 的最小依赖面（可测性）。
type TrackerSeeder interface {
	SeedBucket(start time.Time, miner, worker string, hashrate, sharesPerSecond float64)
}

// netSnapshot 写 poolstats 行时的网络真值快照来源（coininstance.Network 的最小面）。
type netSnapshot func() (hashPS, difficulty float64, height int64)

// runStatsPersist 桶翻转持久化循环。对齐桶边界 + 30s 宽限（等迟到 share 入桶），
// 每次写「上一个完成桶」。ctx 取消即退出。
func (inst *Instance) runStatsPersist(ctx context.Context, db *sql.DB) {
	size := inst.tracker.BucketSize()
	const grace = 30 * time.Second
	for {
		now := time.Now()
		// 下一个写点 = 当前桶结束 + 宽限
		next := now.Truncate(size).Add(size + grace)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		prev := next.Add(-grace).Add(-size) // 刚完成的桶起点
		if err := inst.persistBucket(ctx, db, prev); err != nil {
			log.Printf("[%s] 算力桶持久化失败（下一桶重试，曲线回放会用 shares 补）: %v",
				inst.cfg.ID, err)
		}
	}
}

// persistBucket 把起点为 start 的完成桶写进 poolstats + minerstats（单事务幂等）。
func (inst *Instance) persistBucket(ctx context.Context, db *sql.DB, start time.Time) error {
	sample, miners, ok := inst.tracker.CompletedBucket(start)
	if !ok {
		return nil // 空桶不造 0 行（真实数据铁律：无数据不造点）
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hashPS, diff, height := inst.netSnapshotForStats()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO poolstats (poolid, connectedminers, poolhashrate, networkhashrate,
		                       networkdifficulty, blockheight, created)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (poolid, created) DO NOTHING`,
		inst.cfg.ID, len(miners), sample.Hashrate, hashPS, diff, height, sample.Created); err != nil {
		return err
	}
	for miner, ws := range miners {
		for worker, s := range ws {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO minerstats (poolid, miner, worker, hashrate, sharespersecond, created)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (poolid, miner, worker, created) DO NOTHING`,
				inst.cfg.ID, miner, worker, s.Hashrate, s.SharesPerSecond, sample.Created); err != nil {
				return err
			}
		}
	}
	// 保留期裁剪（索引前缀 (poolid, created)，代价可忽略）
	cut := time.Now().Add(-statsRetention)
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM poolstats WHERE poolid=$1 AND created < $2`, inst.cfg.ID, cut); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM minerstats WHERE poolid=$1 AND created < $2`, inst.cfg.ID, cut); err != nil {
		return err
	}
	return tx.Commit()
}

// netSnapshotForStats 读 Network() 缓存真值；取不到的字段写 0（poolstats 列
// NOT NULL；API 侧展示仍走省略语义，这里只是历史快照存档）。
func (inst *Instance) netSnapshotForStats() (float64, float64, int64) {
	n := inst.Network()
	return n.HashPS, n.Difficulty, int64(n.Height)
}
