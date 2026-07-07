package coininstance

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"
)

// InstanceRegistry 多实例注册表 + 心跳（M4 横向扩展 R14.2）：每个池进程周期
// upsert 自己到 instances 表，前端/网关据此列出「哪些实例在线、各挖哪些币」。
// 判活：lastseen 距今 < staleAfter。共享 Postgres 让多台服务器组成一个逻辑池。
type InstanceRegistry struct {
	db      *sql.DB
	id      string
	version string

	mu    sync.Mutex
	coins []string
}

// NewInstanceRegistry 建注册表（不启心跳；Start 显式开启）。
func NewInstanceRegistry(db *sql.DB, id, version string) *InstanceRegistry {
	return &InstanceRegistry{db: db, id: id, version: version}
}

// SetCoins 更新本实例当前承载的币列表（热加/删币后调用，心跳随之带上）。
func (r *InstanceRegistry) SetCoins(coins []string) {
	r.mu.Lock()
	r.coins = append([]string(nil), coins...)
	r.mu.Unlock()
}

// Start 立即注册一次并每 heartbeatEvery 续期，直到 ctx 结束。
func (r *InstanceRegistry) Start(ctx context.Context) {
	r.beat(ctx)
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.beat(ctx)
			}
		}
	}()
}

const heartbeatEvery = 30 * time.Second

// StaleAfter 心跳超过此时长未续 = 判定实例离线（心跳周期的 3 倍，容忍抖动）。
const StaleAfter = 90 * time.Second

func (r *InstanceRegistry) beat(ctx context.Context) {
	r.mu.Lock()
	coins := strings.Join(r.coins, ",")
	r.mu.Unlock()
	hostname := hostnameOr("unknown")
	// coins 存成 Postgres text[]：用 '{a,b}' 字面量；空列表 = '{}'
	arr := "{" + coins + "}"
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO instances (id, hostname, version, coins, lastseen)
		VALUES ($1,$2,$3,$4::text[],now())
		ON CONFLICT (id) DO UPDATE SET
		  hostname=EXCLUDED.hostname, version=EXCLUDED.version, coins=EXCLUDED.coins, lastseen=now()`,
		r.id, hostname, r.version, arr); err != nil {
		log.Printf("[instance %s] 心跳写入失败: %v", r.id, err)
	}
}

// InstanceInfo 一条实例记录（公共 API /api/instances 用）。
type InstanceInfo struct {
	ID       string   `json:"id"`
	Hostname string   `json:"hostname"`
	Version  string   `json:"version"`
	Coins    []string `json:"coins"`
	LastSeen string   `json:"lastSeen"`
	Alive    bool     `json:"alive"`
}

// ListInstances 读全部实例及判活（供 API 网关聚合多实例）。判活比较用 DB 时钟
// （make_interval 按秒），避免把 Go 的 time.Duration(int64 纳秒) 直接当 interval 传。
func ListInstances(ctx context.Context, db *sql.DB) ([]InstanceInfo, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, hostname, version, coins, lastseen,
		        (now() - lastseen) < make_interval(secs => $1) AS alive
		 FROM instances ORDER BY id`, StaleAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstanceInfo
	for rows.Next() {
		var in InstanceInfo
		var coins pgTextArray
		var lastseen time.Time
		if err := rows.Scan(&in.ID, &in.Hostname, &in.Version, &coins, &lastseen, &in.Alive); err != nil {
			return nil, err
		}
		in.Coins = coins.vals
		in.LastSeen = lastseen.UTC().Format(time.RFC3339)
		out = append(out, in)
	}
	return out, rows.Err()
}

// pgTextArray 极简 Postgres text[] 扫描器（形如 {a,b,c}；本表元素是币 ID，
// 无逗号/引号/花括号等特殊字符，故不需完整 CSV 解析）。
type pgTextArray struct{ vals []string }

func (a *pgTextArray) Scan(src any) error {
	a.vals = nil
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return nil
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "}"), "{")
	if s == "" {
		return nil
	}
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			a.vals = append(a.vals, p)
		}
	}
	return nil
}
