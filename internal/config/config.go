// Package config 池配置 + 热更新。
//
// 热更新模型：所有「热参数」（手续费/确认数/起付额/端口/开关）通过管理后台 API 修改，
// 修改即生效并落盘快照（config.state.json），同时写入审计流水（谁、何时、改了什么）。
// 重启后以最后热状态为准。静态参数（DSN、监听地址）改动仍需重启。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
	Port     int           `json:"port"`
	Mode     string        `json:"mode"`    // "pplns" | "solo"
	Dialect  string        `json:"dialect"` // "stratum1" | "cryptonote" | "ntm"
	Vardiff  VardiffConfig `json:"vardiff"`
	TLS      bool          `json:"tls"`
	Proxy    string        `json:"proxyProtocol"` // "off"|"optional"|"required"（藏转发器后必须 required）
	Enabled  bool          `json:"enabled"`
	MaxConns int           `json:"maxConns"`
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

type PayoutConfig struct {
	Enabled       bool    `json:"enabled"`       // 铁律：第一天就有的总开关
	Scheme        string  `json:"scheme"`        // "pplns"（solo 由端口 mode 决定）
	PplnsFactor   float64 `json:"pplnsFactor"`   // 窗口 = factor × 网络难度
	FeePercent    float64 `json:"feePercent"`    // 热参数
	MinPayout     string  `json:"minPayout"`     // 默认起付额（矿工可用 mp= 覆盖调高）
	Confirmations int64   `json:"confirmations"` // 打款所需确认数（热参数；低于链成熟期=预打款）
	IntervalSec   int     `json:"intervalSeconds"`
	OrphanDebts   bool    `json:"orphanDebts"` // 预打款垫付孤块后是否追缴（R4）

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
	ID          string         `json:"id"`     // 池内唯一，如 "btx"
	Symbol      string         `json:"symbol"` // 展示用
	Adapter     string         `json:"adapter"`
	Algo        string         `json:"algo"`
	Nodes       []NodeEndpoint `json:"nodes"` // 多节点 failover + 并发提交
	PoolAddress string         `json:"poolAddress"`
	FeeAddress  string         `json:"feeAddress"` // 铁律：必须 ≠ PoolAddress，启动校验
	Ports       []PortConfig   `json:"ports"`
	Payout      PayoutConfig   `json:"payout"`
	// 每币独立开关（热参数）
	MiningEnabled  bool `json:"miningEnabled"`
	NewConnsEnabled bool `json:"newConnectionsEnabled"`
}

type Config struct {
	InstanceID string       `json:"instanceId"` // 多服务器实例标识（pool1/pool2…）
	PostgresDSN string      `json:"postgresDsn"`
	PublicAPI  string       `json:"publicApiListen"` // 如 ":4000"
	AdminAPI   string       `json:"adminApiListen"`  // 独立端口，建议只绑内网/专线
	AdminToken string       `json:"adminToken"`      // 建议 ${NTMPOOL_ADMIN_TOKEN}
	MaskSecret string       `json:"maskSecret"`      // API 地址脱敏 HMAC 密钥（R14.6）；空=每次启动随机
	LogDir     string       `json:"logDir"`
	LogQuotaMB int          `json:"logQuotaMB"` // 全局日志磁盘配额（zoka 6.2GB 日志事故的教训）
	Coins      []CoinConfig `json:"coins"`
}

// Validate 启动门禁。
func (c *Config) Validate() error {
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
		for _, p := range coin.Ports {
			if ports[p.Port] {
				return fmt.Errorf("[%s] 端口重复: %d", coin.ID, p.Port)
			}
			ports[p.Port] = true
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
