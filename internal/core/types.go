// Package core 定义全池共享的基础类型。
// 设计出处见 docs/02-架构设计.md；命名尽量对齐 miningcore 概念，方便复用其前端/工具。
package core

import "time"

// ShareOutcome 是 share 处理结果的完整分类。
// 铁律：badpow（池端重算与矿工声称不符/共识失配）必须与 lowdiff（良性难度不足，
// 常见于 vardiff 切换瞬间）分开计数——badpow 是唯一的共识 tripwire，不许被污染。
type ShareOutcome int

const (
	OutcomeAccepted  ShareOutcome = iota // 计入 PPLNS/SOLO 权重
	OutcomeBlock                         // 同时命中全网难度（爆块）
	OutcomeStale                         // job 已过期（超过一步 grace 窗）
	OutcomeDup                           // 重复 nonce
	OutcomeLowDiff                       // 未达 share 难度（含 vardiff straddle 良性拒）
	OutcomeBadPow                        // 池端重算失配 —— 共识 tripwire，出现即告警
	OutcomeMalformed                     // 协议层非法提交
)

func (o ShareOutcome) String() string {
	switch o {
	case OutcomeAccepted:
		return "accepted"
	case OutcomeBlock:
		return "block"
	case OutcomeStale:
		return "stale"
	case OutcomeDup:
		return "dup"
	case OutcomeLowDiff:
		return "lowdiff"
	case OutcomeBadPow:
		return "badpow"
	default:
		return "malformed"
	}
}

// Share 是一条已解析的矿工提交。
type Share struct {
	Coin       string
	Address    string  // 矿工钱包地址（对外 API 必须脱敏，见 docs/01 R14.6）
	Worker     string  // 矿工名（address.worker 惯例）
	UserAgent  string  // 锄头软件/版本（subscribe/login 时记录）
	RemoteIP   string  // 经 PROXY protocol 还原的真实 IP
	Difficulty float64 // 计入权重的实际满足难度（一步 grace 下可能是 prevDiff）
	Solo       bool    // 该端口是否 SOLO 模式
	At         time.Time
}

// BlockStatus 是池找到的块的状态机。
// pending → confirmed（成熟且仍在主链）/ orphaned（被主链甩掉）。
// 铁律：入账前必须按高度取主链块 ID 与 Hash 逐字节比对（不是比高度）。
type BlockStatus string

const (
	BlockPending   BlockStatus = "pending"
	BlockConfirmed BlockStatus = "confirmed"
	BlockOrphaned  BlockStatus = "orphaned"
)

// DirectCredit 直付块（midstate model-a coinbase 直付，docs/07 §6）的一条分账：
// Credit = 模板时按 PPLNS 窗口算好的应得；Paid = 随 coinbase 实付上链的金额
// （含 carry 兑付，可大于 Credit；0 = 低于尘埃阈值本块结转）。金额十进制字符串。
type DirectCredit struct {
	Address string
	Credit  string
	Paid    string
}

// FoundBlock 记录池挖到的一个块。
type FoundBlock struct {
	Coin    string
	Height  uint64
	Hash    string // 我们提交的块 ID —— 孤块比对的基准
	Finder  string // 爆块矿工地址
	Worker  string
	Reward  string  // 十进制字符串，币为单位（对齐 miningcore NUMERIC）
	NetDiff float64 // 该块的网络难度（PPLNS 窗口 = pplnsN × NetDiff）
	Effort  float64
	Status  BlockStatus
	FoundAt time.Time
	Solo    bool
	// Direct 非 nil = 直付块：分账在模板时已定死并嵌进 coinbase（可为空切片=
	// 全额归费侧）。ConfirmBlock 按记录快照入账，绝不重算窗口（账实一致铁律）。
	Direct []DirectCredit
}

// NetworkDifficulty 该块的网络难度。
func (b FoundBlock) NetworkDifficulty() float64 { return b.NetDiff }

// PaymentStatus 是打款状态机。
// created(先扣余额,txid=NULL) → sent(拿到 txid) → confirming → confirmed / failed。
// 铁律：failed 绝不自动重发，人工介入（防 sendmany 超时双花，pitfall C4）。
type PaymentStatus string

const (
	PaymentCreated    PaymentStatus = "created"
	PaymentSent       PaymentStatus = "sent"
	PaymentConfirming PaymentStatus = "confirming"
	PaymentConfirmed  PaymentStatus = "confirmed"
	PaymentFailed     PaymentStatus = "failed"
)

// NetworkSnapshot 供公共 API 读的链上状态缓存（coininstance 周期刷新，API 零 RPC）。
// 全部为节点真值：高度/难度来自当前 job（GBT），HashPS 来自 getnetworkhashps（pitfall C7）。
type NetworkSnapshot struct {
	Height     uint64
	Difficulty float64
	HashPS     float64 // 0 = 尚未取到（API 侧省略该字段，不造 0 值）
	UpdatedAt  time.Time
}

// TipEvent 是节点侧「链头变化」事件，来自任一通知通道（ZMQ/轮询/进程内回调/QUIC push）。
// 多通道并存时取最先到者，按 (Height,Hash) 去重。
type TipEvent struct {
	Coin   string
	Height uint64 // 0 = 该通道不知道高度（如 ZMQ hashblock 只有 hash）
	Hash   string // display hex；可为空（去重键退化为 Height）
	Source string // 通道名（longpoll/zmq/…），仅供日志
	At     time.Time
}
