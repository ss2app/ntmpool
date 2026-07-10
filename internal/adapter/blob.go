package adapter

import (
	"context"
	"math/big"
)

// HashPSSource 可选扩展：链提供「真实全网算力」口径（bitcoin 系 getnetworkhashps）。
// 铁律（pitfall C7）：没有真值口径的链（门罗系只有 difficulty）不实现本接口，
// API 侧省略该字段——绝不用「难度÷出块时间」反推冒充。
type HashPSSource interface {
	NetworkHashPS(ctx context.Context) (float64, error)
}

// BlobWork 是 blob 系链（CryptoNote/RandomX 家族与 zoka 类自定义链）模板的归一化工件，
// 由 cryptonote-rpc / custom-http 适配器放进 BlockTemplate.Raw。
//
// nonce 字段统一模型（覆盖两类实战布局）：
//   - monero 系：HashingBlob 偏移 39 起 4 字节 nonce；nicehash 分片时矿工只滚低 3 字节，
//     最高 1 字节由池按连接钉死（免去 extranonce→merkle 重建，代价 = 每 job ≤256 连接）。
//   - zoka 系（CRB 先例）：blob 尾部 8 字节 nonce；矿工滚低 4 字节，高 4 字节池按连接钉死。
//
// 即：nonce 字段共 NonceLen 字节（LE），低 SearchLen 字节 = 矿工搜索区（下发前清零），
// 高 NonceLen-SearchLen 字节 = 池写入的连接 tag。真正的 tx_extra reserved 空间 extranonce
// （需要重建 merkle root）暂不支持——等真门罗系币接入时按 docs/03 M3+ 补。
type BlobWork struct {
	HashingBlob []byte // PoW 输入全文（nonce 区含模板原值，物化时处理）
	NonceOffset int    // nonce 字段偏移
	NonceLen    int    // nonce 字段总字节数（≤8）
	SearchLen   int    // 矿工搜索区字节数（nonce 字段低位；< NonceLen 时高位为连接 tag）

	SeedHash string // RandomX epoch 种子（64 hex）；非 RandomX 链可为空
	Algo     string // 下发矿工的算法名（rx/0 等）

	// NetworkTarget 命中即爆块的 256-bit 目标；HashBigEndian 指明 hash 解释字节序
	// （zoka=大端 leading-zero-bits；monero=小端）。
	NetworkTarget *big.Int
	HashBigEndian bool

	// TargetCompactLE=true 下发矿工的 target 用 8-hex compact 小端（XMRig 惯例）；
	// false 用 64-hex 大端全量 target（zoka/CRB 惯例，NTMminer 认这个）。
	TargetCompactLE bool

	// Nicehash 分片模式：连接 tag 钉在 nonce 最高字节、矿工只滚低 SearchLen 字节，
	// login extensions 要声明 "nicehash"（XMRig 据此只滚低 3 字节）。
	Nicehash bool

	// WireNonceLen 矿工 submit 里回显的 nonce 字段字节数（0 = 与 NonceLen 相同）。
	// dragonx 类：块头 nonce 字段 32B，矿工只滚低 4 字节但回显完整 32B——池只滚
	// 低 NonceLen(8) 字节做搜索区+连接 tag，[NonceLen:WireNonceLen) 为模板保留区
	// （适配器可写实例盐），提交时逐字节回显校验（防伪造/坏矿工，miningcore 同款）。
	WireNonceLen int

	// PowIsBlockHash true 时 PoW hash 反转即链上块 hash（dragonx：块 id =
	// reverse(sha256d(173B))）。爆块落库占位直接用显示序真块 hash——即使提交
	// 失败/崩溃，分类器也能按链上 hash 归位（免 blob 链"先交后记"的两难）。
	PowIsBlockHash bool

	// HeightHint 模板高度（适配器提交/查块时用，与 BlockTemplate.Height 一致）。
	HeightHint uint64

	// SubmitRef 适配器组块提交所需的私有引用
	// （zoka: template_id；monero: blocktemplate_blob hex）。
	SubmitRef any
}

// BlobSolution 是 blob 系的爆块解，交给 BlobSubmitter.SubmitBlob。
type BlobSolution struct {
	Work  *BlobWork
	Blob  []byte // 已写入完整 nonce 字段的 PoW 输入
	Nonce uint64 // 完整 nonce 字段值（LE 解释；zoka 提交只要它）
	Hash  []byte // 池端重算的共识哈希（双段算法 = 外层 PoW hash）
	// AuxHash 双段算法的内层哈希（rx/dragonx：RandomX 结果 = 块序列化里的
	// nSolution 内容，组块提交必需）；单段算法为 nil。
	AuxHash []byte
}

// BlobSubmitter blob 系适配器必须实现：提交爆块解并返回链上权威块 hash
// （blob 链的块 id ≠ PoW hash，孤块比对基准只能由节点给）。
type BlobSubmitter interface {
	SubmitBlob(ctx context.Context, sol *BlobSolution) (blockHash string, err error)
}
