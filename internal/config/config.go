// Package config 池配置 + 热更新。
//
// 热更新模型：所有「热参数」（手续费/确认数/起付额/端口/开关）通过管理后台 API 修改，
// 修改即生效并落盘快照（config.state.json），同时写入审计流水（谁、何时、改了什么）。
// 重启后以最后热状态为准。静态参数（DSN、监听地址）改动仍需重启。
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

const (
	DefaultMessageMaxBytes          = 32 * 1024
	HardMessageMaxBytes             = 64 * 1024
	DefaultJSONMaxDepth             = 16
	DefaultUnauthMaxMessages        = 32
	DefaultUnauthMaxBytes           = 128 * 1024
	DefaultPreAuthViolationLimit    = 3
	DefaultHandshakeTimeoutSeconds  = 10
	DefaultAuthorizeTimeoutSeconds  = 30
	DefaultPowVerifyConcurrency     = 4
	DefaultPowVerifyQueue           = 64
	DefaultPowBackpressureMillis    = 1000
	DefaultCollapseMinConnections   = 20
	DefaultCollapsePercent          = 90
	DefaultBanRejectLogsPerIPMinute = 5
	DefaultAutoBanMinSamples        = 10
	DefaultAutoBanPercent           = 50
	DefaultAutoBanBaseTTLSeconds    = 10 * 60
	DefaultAutoBanMaxTTLSeconds     = 24 * 60 * 60
)

type VardiffConfig struct {
	Enabled        bool    `json:"enabled"`
	StartDiff      float64 `json:"startDiff"`
	MinDiff        float64 `json:"minDiff"`
	MaxDiff        float64 `json:"maxDiff"`
	TargetSeconds  float64 `json:"targetSeconds"`
	RetargetMinSec float64 `json:"retargetMinSeconds"`
}

// PortConfig 一个 stratum 端口。热管理：可运行时增删启停（docs/01 R7）。
type PortConfig struct {
	Port          int           `json:"port"`
	Mode          string        `json:"mode"`    // "pplns" | "solo"
	Dialect       string        `json:"dialect"` // "stratum1" | "cryptonote" | "ntm"
	Vardiff       VardiffConfig `json:"vardiff"`
	TLS           bool          `json:"tls"`
	Proxy         string        `json:"proxyProtocol"` // "off"|"optional"|"required"（藏转发器后必须 required）
	Enabled       bool          `json:"enabled"`
	MaxConns      int           `json:"maxConns"`      // 端口在连上限（0 = 不限）
	MaxConnsPerIP int           `json:"maxConnsPerIp"` // 每真实 IP 在连上限（0 = 不限）

	// 连接攻击面护栏。零值在加载/启动端口时填为下列生产默认值；消息硬上限永远是 64 KiB。
	MessageMaxBytes       int `json:"messageMaxBytes"`
	JSONMaxDepth          int `json:"jsonMaxDepth"`
	UnauthMaxMessages     int `json:"unauthMaxMessages"`
	UnauthMaxBytes        int `json:"unauthMaxBytes"`
	PreAuthViolationLimit int `json:"preAuthViolationLimit"`
	HandshakeTimeoutSec   int `json:"handshakeTimeoutSeconds"`
	AuthorizeTimeoutSec   int `json:"authorizeTimeoutSeconds"`

	// 昂贵 PoW 校验的跨连接有界并发与等待队列。
	PowVerifyConcurrency  int `json:"powVerifyConcurrency"`
	PowVerifyQueue        int `json:"powVerifyQueue"`
	PowBackpressureMillis int `json:"powBackpressureMillis"`

	// 同一 verified IP 占据绝大多数活跃连接时，自动降级为仅断会话、不写 IP ban。
	CollapseMinConnections int `json:"collapseMinConnections"`
	CollapsePercent        int `json:"collapsePercent"`
	BanRejectLogPerMinute  int `json:"banRejectLogPerIpPerMinute"`

	// 自动 ban 判定与指数退避阈值。
	AutoBanMinSamples     int `json:"autoBanMinSamples"`
	AutoBanPercent        int `json:"autoBanPercent"`
	AutoBanBaseTTLSeconds int `json:"autoBanBaseTtlSeconds"`
	AutoBanMaxTTLSeconds  int `json:"autoBanMaxTtlSeconds"`
}

// WithPortDefaults 返回填好连接治理默认值的副本，避免旧配置因新增字段为零而失去护栏。
func WithPortDefaults(p PortConfig) PortConfig {
	if p.MessageMaxBytes == 0 {
		p.MessageMaxBytes = DefaultMessageMaxBytes
	}
	if p.JSONMaxDepth == 0 {
		p.JSONMaxDepth = DefaultJSONMaxDepth
	}
	if p.UnauthMaxMessages == 0 {
		p.UnauthMaxMessages = DefaultUnauthMaxMessages
	}
	if p.UnauthMaxBytes == 0 {
		p.UnauthMaxBytes = DefaultUnauthMaxBytes
	}
	if p.PreAuthViolationLimit == 0 {
		p.PreAuthViolationLimit = DefaultPreAuthViolationLimit
	}
	if p.HandshakeTimeoutSec == 0 {
		p.HandshakeTimeoutSec = DefaultHandshakeTimeoutSeconds
	}
	if p.AuthorizeTimeoutSec == 0 {
		p.AuthorizeTimeoutSec = DefaultAuthorizeTimeoutSeconds
	}
	if p.PowVerifyConcurrency == 0 {
		p.PowVerifyConcurrency = DefaultPowVerifyConcurrency
	}
	if p.PowVerifyQueue == 0 {
		p.PowVerifyQueue = DefaultPowVerifyQueue
	}
	if p.PowBackpressureMillis == 0 {
		p.PowBackpressureMillis = DefaultPowBackpressureMillis
	}
	if p.CollapseMinConnections == 0 {
		p.CollapseMinConnections = DefaultCollapseMinConnections
	}
	if p.CollapsePercent == 0 {
		p.CollapsePercent = DefaultCollapsePercent
	}
	if p.BanRejectLogPerMinute == 0 {
		p.BanRejectLogPerMinute = DefaultBanRejectLogsPerIPMinute
	}
	if p.AutoBanMinSamples == 0 {
		p.AutoBanMinSamples = DefaultAutoBanMinSamples
	}
	if p.AutoBanPercent == 0 {
		p.AutoBanPercent = DefaultAutoBanPercent
	}
	if p.AutoBanBaseTTLSeconds == 0 {
		p.AutoBanBaseTTLSeconds = DefaultAutoBanBaseTTLSeconds
	}
	if p.AutoBanMaxTTLSeconds == 0 {
		p.AutoBanMaxTTLSeconds = DefaultAutoBanMaxTTLSeconds
	}
	return p
}

func (p PortConfig) HandshakeTimeout() time.Duration {
	return time.Duration(WithPortDefaults(p).HandshakeTimeoutSec) * time.Second
}

func (p PortConfig) AuthorizeTimeout() time.Duration {
	return time.Duration(WithPortDefaults(p).AuthorizeTimeoutSec) * time.Second
}

// ConsolidationConfig 钱包整备（note/UTXO 定时合并，docs/02 §6）。
// 隐私链（dragonx 类）必开：不合并则打款 tx 要打包大量 note，体积超限/超时/被拒。
type ConsolidationConfig struct {
	Enabled      bool `json:"enabled"`
	IntervalSec  int  `json:"intervalSeconds"`  // 定时检查间隔（如 3600）
	TriggerCount int  `json:"triggerCount"`     // 碎片（note/UTXO）数超过多少触发
	MaxInputs    int  `json:"maxInputsPerTx"`   // 每笔整备 tx 最多合并数（zcash 系惯例 ~45）
	MinConf      int  `json:"minConfirmations"` // 只合并已成熟碎片
}

// FeeCollectConfig 手续费自动归集（R9）：未归集费达 minAmount 时打款周期尾部
// 自动池钱包→FeeAddress（与打款共用每币锁串行）。
type FeeCollectConfig struct {
	Enabled   bool   `json:"enabled"`
	MinAmount string `json:"minAmount"` // 十进制字符串；空 = 有多少归多少
}

type PayoutConfig struct {
	Enabled        bool     `json:"enabled"`                  // 铁律：第一天就有的总开关
	ChainAudit     *bool    `json:"chainAudit,omitempty"`     // nil=auto：持久账本开、内存账本关
	Scheme         string   `json:"scheme"`                   // "pplns"（solo 由端口 mode 决定）
	PplnsFactor    float64  `json:"pplnsFactor"`              // 窗口 = factor × 网络难度
	FeePercent     float64  `json:"feePercent"`               // 热参数
	SoloFeePercent *float64 `json:"soloFeePercent,omitempty"` // 热参数；solo 块费率，nil=与 feePercent 相同
	MinPayout      string   `json:"minPayout"`                // 默认起付额（矿工可用 mp= 覆盖调高）
	Confirmations  int64    `json:"confirmations"`            // 打款所需确认数（热参数；低于链成熟期=预打款）
	IntervalSec    int      `json:"intervalSeconds"`
	OrphanDebts    bool     `json:"orphanDebts"` // 预打款垫付孤块后是否追缴（R4）

	FeeCollect    FeeCollectConfig    `json:"feeCollect"`
	Consolidation ConsolidationConfig `json:"consolidation"`
}

type NodeEndpoint struct {
	URL  string `json:"url"`
	User string `json:"user,omitempty"`
	Pass string `json:"pass,omitempty"` // 建议用环境变量 ${VAR} 引用，密钥不落配置文件
	ZMQ  string `json:"zmq,omitempty"`
}

// CoinConfig 一个币的完整配置。
type CoinConfig struct {
	ID      string `json:"id"`      // 池内唯一，如 "btx"
	Symbol  string `json:"symbol"`  // 展示用
	Adapter string `json:"adapter"` // "bitcoin-rpc" | "cryptonote-rpc" | "custom-http"
	Algo    string `json:"algo"`
	// StratumAlgo 对矿工声明的 wire 算法名（login 能力协商 + job.algo）。留空=用 Algo。
	// 用于「内部算法标识 ≠ 通用矿工认识的标准名」的币：如 Brisvia 内部 algo=rx/brva（选 hasher），
	// 但它是字节兼容 stock rx/0，故 stratumAlgo=rx/0 让 xmrig/SRBMiner 等通用 RandomX 锄头能连（池是主生意）。
	StratumAlgo string         `json:"stratumAlgo"`
	Decimals    int            `json:"decimals"` // 币最小单位小数位；0 = 按适配器默认（btc 系 8，门罗系 12）
	Nodes       []NodeEndpoint `json:"nodes"`    // 多节点 failover + 并发提交
	Wallet      NodeEndpoint   `json:"wallet"`   // 钱包独立进程的链（cryptonote-rpc 必填）
	PoolAddress string         `json:"poolAddress"`
	FeeAddress  string         `json:"feeAddress"` // 铁律：必须 ≠ PoolAddress，启动校验
	Ports       []PortConfig   `json:"ports"`
	Payout      PayoutConfig   `json:"payout"`
	// 每币独立开关（热参数）
	MiningEnabled   bool `json:"miningEnabled"`
	NewConnsEnabled bool `json:"newConnectionsEnabled"`
}

// NotifyConfig 运营通知出口（docs/03 M2）。全部可选；一个不配 = 不通知。
type NotifyConfig struct {
	WebhookURL       string `json:"webhookUrl"`       // 通用 JSON POST
	TelegramBotToken string `json:"telegramBotToken"` // 建议 ${NTMPOOL_TG_TOKEN}
	TelegramChatID   string `json:"telegramChatId"`
}

type Config struct {
	InstanceID  string       `json:"instanceId"` // 多服务器实例标识（pool1/pool2…）
	PostgresDSN string       `json:"postgresDsn"`
	PublicAPI   string       `json:"publicApiListen"` // 如 ":4000"
	AdminAPI    string       `json:"adminApiListen"`  // 独立端口，建议只绑内网/专线
	AdminToken  string       `json:"adminToken"`      // 建议 ${NTMPOOL_ADMIN_TOKEN}
	MaskSecret  string       `json:"maskSecret"`      // API 地址脱敏 HMAC 密钥（R14.6）；空=每次启动随机
	DataDir     string       `json:"dataDir"`         // 状态文件目录（bans/miner_settings/config.state/config_audit）；空="."
	LogDir      string       `json:"logDir"`
	LogQuotaMB  int          `json:"logQuotaMB"` // 全局日志磁盘配额（zoka 6.2GB 日志事故的教训）
	Notify      NotifyConfig `json:"notify"`
	// ProtectedCIDRs 同时作为受保护名单与 trusted-forwarder 名单：
	// 中转机/节点/admin/VPN 永远不可 ban；只有这些 socket 对端发来的合法 PROXY 头才可信。
	ProtectedCIDRs []string     `json:"protectedCidrs"`
	Coins          []CoinConfig `json:"coins"`
}

// ApplyDefaults 为全部端口补齐新增治理阈值。
func (c *Config) ApplyDefaults() {
	for ci := range c.Coins {
		for pi := range c.Coins[ci].Ports {
			c.Coins[ci].Ports[pi] = WithPortDefaults(c.Coins[ci].Ports[pi])
		}
	}
}

// Validate 启动门禁。
func (c *Config) Validate() error {
	for _, rule := range c.ProtectedCIDRs {
		if net.ParseIP(rule) == nil {
			if _, _, err := net.ParseCIDR(rule); err != nil {
				return fmt.Errorf("protectedCidrs 含非法 IP/CIDR: %q", rule)
			}
		}
	}
	seen := map[string]bool{}
	for _, coin := range c.Coins {
		if coin.ID == "" {
			return fmt.Errorf("coin 缺少 id")
		}
		if seen[coin.ID] {
			return fmt.Errorf("coin id 重复: %s", coin.ID)
		}
		seen[coin.ID] = true
		if coin.PoolAddress != "" && coin.PoolAddress == coin.FeeAddress {
			return fmt.Errorf("[%s] 矿池地址与手续费地址不得相同（双地址分离铁律）", coin.ID)
		}
		ports := map[int]bool{}
		for _, raw := range coin.Ports {
			p := WithPortDefaults(raw)
			if ports[p.Port] {
				return fmt.Errorf("[%s] 端口重复: %d", coin.ID, p.Port)
			}
			ports[p.Port] = true
			if p.MessageMaxBytes <= 0 || p.MessageMaxBytes > HardMessageMaxBytes {
				return fmt.Errorf("[%s:%d] messageMaxBytes 必须在 1..%d", coin.ID, p.Port, HardMessageMaxBytes)
			}
			if p.JSONMaxDepth <= 0 || p.UnauthMaxMessages <= 0 || p.UnauthMaxBytes <= 0 ||
				p.PreAuthViolationLimit <= 0 || p.HandshakeTimeoutSec <= 0 || p.AuthorizeTimeoutSec <= 0 {
				return fmt.Errorf("[%s:%d] 分级校验阈值必须为正数", coin.ID, p.Port)
			}
			if p.PowVerifyConcurrency <= 0 || p.PowVerifyQueue <= 0 || p.PowBackpressureMillis <= 0 {
				return fmt.Errorf("[%s:%d] PoW 有界队列阈值必须为正数", coin.ID, p.Port)
			}
			if p.CollapseMinConnections <= 0 || p.CollapsePercent < 1 || p.CollapsePercent > 100 {
				return fmt.Errorf("[%s:%d] 坍缩阈值非法", coin.ID, p.Port)
			}
			if p.BanRejectLogPerMinute <= 0 || p.AutoBanMinSamples <= 0 || p.AutoBanPercent < 1 || p.AutoBanPercent > 100 ||
				p.AutoBanBaseTTLSeconds <= 0 || p.AutoBanMaxTTLSeconds < p.AutoBanBaseTTLSeconds {
				return fmt.Errorf("[%s:%d] autoban/拒连阈值非法", coin.ID, p.Port)
			}
		}
	}
	return nil
}

// Store 持有当前生效配置，供热更新。读多写少，快照语义。
type Store struct {
	mu  sync.RWMutex
	cur *Config
	// OnChange 热更新回调（端口管理器/打款引擎订阅）。审计由管理 API 层负责。
	OnChange func(old, new *Config)
}

func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(os.ExpandEnv(string(b))) // 支持 ${NTMPOOL_ADMIN_TOKEN} 式密钥注入
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Store{cur: &c}, nil
}

// Snapshot 返回当前配置（调用方只读，不得修改）。
func (s *Store) Snapshot() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Apply 原子替换配置（管理 API 校验通过后调用）。
func (s *Store) Apply(next *Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	old := s.cur
	s.cur = next
	cb := s.OnChange
	s.mu.Unlock()
	if cb != nil {
		cb(old, next)
	}
	return nil
}
