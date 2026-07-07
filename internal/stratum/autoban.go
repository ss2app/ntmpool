package stratum

import (
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// Banner 自动 ban 的写入口（banlist.List 满足；与 BanChecker 分开：查/写两个面）。
type Banner interface {
	Ban(target, reason string, ttl time.Duration) error
	Strikes(target string) int
}

// 自动 ban 策略（docs/03 M3：invalidPercent 阈值 + 指数退避）。
// 只数恶意信号：badpow（共识失配）/malformed（协议非法）/dup（重放）。
// stale/lowdiff 是良性（vardiff straddle、换工竞速），绝不计入——
// 否则把弱矿机 ban 了（lolMiner 连续 3 个误拒就弃池，误 ban 更致命）。
const (
	autoBanMinSamples = 10 // 至少积累多少条提交才判定
	autoBanPercent    = 50 // 恶意占比阈值（%）
	autoBanBaseTTL    = 10 * time.Minute
	autoBanMaxTTL     = 24 * time.Hour
)

// autoBan 每连接违规统计（V1/CN 方言共用）。非并发安全：每连接串行使用。
type autoBan struct {
	banner  Banner
	ip      string
	total   int
	violent int
}

// record 记一条提交结果；返回 true = 已触发 ban（调用方应立即断开连接）。
func (a *autoBan) record(outcome core.ShareOutcome) bool {
	if a == nil || a.banner == nil {
		return false
	}
	a.total++
	switch outcome {
	case core.OutcomeBadPow, core.OutcomeMalformed, core.OutcomeDup:
		a.violent++
	}
	if a.total < autoBanMinSamples || a.violent*100 < a.total*autoBanPercent {
		return false
	}
	// 指数退避：TTL = base × 2^strikes（封顶 24h）
	ttl := autoBanBaseTTL << uint(a.banner.Strikes(a.ip))
	if ttl > autoBanMaxTTL || ttl <= 0 {
		ttl = autoBanMaxTTL
	}
	_ = a.banner.Ban(a.ip, "auto: bad shares", ttl)
	return true
}
