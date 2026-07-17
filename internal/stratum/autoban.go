package stratum

import (
	"errors"
	"log"
	"net"
	"time"

	"github.com/scashcc/ntmpool/internal/banlist"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
)

// Banner 自动 ban 的写入口（banlist.List 满足；与 BanChecker 分开：查/写两个面）。
type Banner interface {
	Ban(target, reason string, ttl time.Duration) error
	Strikes(target string) int
	IsProtected(target string) bool
}

// 保留默认常量名供策略测试使用；生产值来自 PortConfig。
const (
	autoBanMinSamples = config.DefaultAutoBanMinSamples
	autoBanPercent    = config.DefaultAutoBanPercent
)

// autoBan 每连接违规统计。无 verified IP、受保护目标或端口坍缩降级时，
// 达阈值仍返回 true 断开坏连接，但绝不写 IP ban。
type autoBan struct {
	banner  Banner
	ip      string
	coin    string
	total   int
	violent int
	cfg     config.PortConfig
	allowed func() bool
	tripped bool
}

func newAutoBan(b Banner, conn net.Conn, coin string, pc config.PortConfig) *autoBan {
	id := connectionIdentity(conn)
	var allowed func() bool
	if gate, ok := conn.(ipAutobanGate); ok {
		allowed = gate.IPAutobanAllowed
	}
	return &autoBan{banner: b, ip: id.VerifiedClientIP, coin: coin, cfg: config.WithPortDefaults(pc), allowed: allowed}
}

// record 记一条提交结果；返回 true = 应立即断开当前连接。
func (a *autoBan) record(outcome core.ShareOutcome) bool {
	if a == nil || a.tripped {
		return a != nil && a.tripped
	}
	pc := config.WithPortDefaults(a.cfg)
	a.total++
	switch outcome {
	case core.OutcomeBadPow, core.OutcomeMalformed, core.OutcomeDup:
		a.violent++
	}
	if a.total < pc.AutoBanMinSamples || a.violent*100 < a.total*pc.AutoBanPercent {
		return false
	}
	a.tripped = true
	reason := "auto: bad shares"
	if a.banner == nil || a.ip == "" {
		log.Printf("[P0] [%s] autoban_degraded target=%q reason=%q action=%q strikes=%d ttl=%s detail=%q", a.coin, a.ip, reason, "session_disconnect", 0, 0*time.Second, "verified_client_ip unavailable")
		return true
	}
	if a.allowed != nil && !a.allowed() {
		log.Printf("[P0] [%s] autoban_degraded target=%q reason=%q action=%q strikes=%d ttl=%s detail=%q", a.coin, a.ip, reason, "session_disconnect", a.banner.Strikes(a.ip), 0*time.Second, "port collapse guard active")
		return true
	}
	if a.banner.IsProtected(a.ip) {
		log.Printf("[P0] [%s] autoban_protected target=%q reason=%q action=%q strikes=%d ttl=%s", a.coin, a.ip, reason, "session_disconnect", a.banner.Strikes(a.ip), 0*time.Second)
		return true
	}

	strikes := a.banner.Strikes(a.ip)
	ttl := time.Duration(pc.AutoBanBaseTTLSeconds) * time.Second
	maxTTL := time.Duration(pc.AutoBanMaxTTLSeconds) * time.Second
	for i := 0; i < strikes && ttl < maxTTL; i++ {
		if ttl > maxTTL/2 {
			ttl = maxTTL
			break
		}
		ttl *= 2
	}
	if ttl > maxTTL || ttl <= 0 {
		ttl = maxTTL
	}
	err := a.banner.Ban(a.ip, reason, ttl)
	if err != nil {
		action := "session_disconnect"
		level := "ERROR"
		if errors.Is(err, banlist.ErrProtected) {
			level = "P0"
		}
		log.Printf("[%s] [%s] autoban_result target=%q reason=%q action=%q strikes=%d ttl=%s error=%q", level, a.coin, a.ip, reason, action, strikes, ttl, err)
		return true
	}
	log.Printf("[%s] autoban_result target=%q reason=%q action=%q strikes=%d ttl=%s", a.coin, a.ip, reason, "ip_ban_and_session_disconnect", a.banner.Strikes(a.ip), ttl)
	return true
}
