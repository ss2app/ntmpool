// Package brisviarpc Brisvia (BRVA) 适配器：透明比特币 getblocktemplate × blob 作业管线。
//
// Brisvia = Bitcoin Core v30.2 fork（透明 UTXO），PoW = stock RandomX（rx/brva），
// 难度 ASERT。节点即标准 bitcoind，故内嵌 bitcoinrpc.Client 复用全套 JSON-RPC + 透明
// 钱包（getblocktemplate/submitblock/getaddressinfo/sendmany/RawTx…）；本包只补两件
// Brisvia 特有的事：
//
//	① GetTemplate override：把标准 GBT 物化成 blob 作业（cnjob 消费的 adapter.BlobWork）——
//	   建透明 coinbase → merkle root → 拼 80 字节比特币区块头 → 按高度自算 RandomX seed。
//	② SubmitBlob：爆块时用中标 nonce 的 80B 头 + coinbase + 其余 tx 组全块 → submitblock。
//
// 关键共识约定（_knowledge/algorithms/RandomX-brisvia-spec.md）：
//   - PoW 输入 = 标准 80 字节头，nonce 在 offset 76（LE），无外层 double-SHA（单段）。
//   - rx_hash 视作 256-bit LE 整数 ≤ target 即有效 → HashBigEndian=false（monero 惯例）。
//   - seed = 高度 h 的祖先块（2048 epoch / 64 延迟）hash 的【内部序】；h<64 用初始 seed。
//   - 块 hash = SHA256d(header80) ≠ PoW → PowIsBlockHash=false。
//   - nonce 4 字节拆成【低 3 字节=矿工搜索区】+【最高 1 字节=连接 tag】（SearchLen=3）。
//     tag 由 cnjob 按 connID 写入，是矿工之间唯一的区分手段；曾经 SearchLen=4（tagBytes=0）
//     让所有连接拿到逐字节相同的 blob → 各家锄头都从 nonce 0 起扫、算出完全相同的 hash 序列，
//     池的有效算力被压成单机（重复 share 因去重是 per-connection 的而照收照计分，指标全正常）。
//     每连接搜索空间 2^24，连接数上限 256（family_brisvia 调 SetConnIDSpace(256) 池化保证不撞）。
//     ⚠ 若日后需要 >256 并发连接，改走 per-connection coinbase extranonce（BuildCoinbase 已留参数）。
package brisviarpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/bitcoinrpc"
	"github.com/scashcc/ntmpool/internal/btcwork"
)

// seed 轮换常量（与节点 src/consensus/randomx_seed.h + spec §3 一致）。
const (
	seedPeriod = 2048 // 每 2048 块轮换
	seedDelay  = 64   // 延迟 64 块

	// nonce 字段布局（比特币 80 字节头）。★searchLen 必须 < nonceLen，差值即【连接 tag】字节数：
	// tag 是矿工之间唯一的区分手段，tagBytes=0 会让所有连接拿到逐字节相同的 blob（详见
	// GetTemplate 里 SearchLen 的注释）。锄头侧对应契约 = 只滚低 searchLen 字节、原样保留高位。
	nonceOffset = 76
	nonceLen    = 4
	searchLen   = 3
)

// brisviaInitialSeedHex 高度 0..63 的固定初始 seed，仅在节点不下发权威 seed 时兜底。
// ⚠ 该常量【按链不同】：main/testnet = 0x54×32，但 regtest = 0x42×32
// （节点 src/kernel/chainparams.cpp 的 consensus.brisviaInitialSeed 三处各自定义）。
// 这里只能取一个值 → 正是不能靠自算的原因，权威来源见 seedHash 的注释。
var brisviaInitialSeedHex = strings.Repeat("54", 32)

// seedFallbackWarn 保证「节点没下发权威 seed，回退自算」只告警一次（每进程）。
var seedFallbackWarn sync.Once

// poolTag coinbase scriptSig 尾部品牌标记。
var poolTag = []byte("/NTMPool/")

// Client 内嵌 bitcoinrpc.Client（全套 Node/Wallet/RawTx 角色），补 blob 作业 + SubmitBlob。
type Client struct {
	*bitcoinrpc.Client
	poolAddress string

	mu           sync.Mutex
	payoutScript []byte // 缓存的矿池地址 scriptPubKey（首次向节点问一次）
}

// 编译期断言：blob 作业管线要的两个能力。
var _ interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error)
} = (*Client)(nil)

func New(name, url, user, pass, poolAddress string) *Client {
	return &Client{
		Client:      bitcoinrpc.New(name, url, user, pass),
		poolAddress: poolAddress,
	}
}

// submitRef 组块所需的私有引用（存进 BlobWork.SubmitRef）。
type submitRef struct {
	coinbaseWitness []byte   // 见证序列化的 coinbase（进块用）
	rawTxs          [][]byte // GBT 其余交易的原始字节（已含各自 witness）
}

// gbtView 从 raw GBT JSON 抽本包额外需要的字段（bitcoinrpc.GetTemplate 只抽了通用几项）。
type gbtView struct {
	Version                  int64  `json:"version"`
	Bits                     string `json:"bits"`
	CurTime                  int64  `json:"curtime"`
	Height                   uint64 `json:"height"`
	PreviousBlockHash        string `json:"previousblockhash"`
	CoinbaseValue            int64  `json:"coinbasevalue"`
	Target                   string `json:"target"`
	DefaultWitnessCommitment string `json:"default_witness_commitment"`
	Transactions             []struct {
		Data string `json:"data"`
		TxID string `json:"txid"`
		Hash string `json:"hash"` // 非 segwit 链（PIVX/Noctari）GBT 不出 txid，只出 hash（== txid）
	} `json:"transactions"`
	// Brisvia 节点在 GBT 里下发的 RandomX 挖矿契约（src/rpc/mining.cpp，fPowRandomX 时才有）。
	// randomx_seed_hash 是【显示序】uint256.GetHex()，用前需 Reverse 成内部序。
	Brisvia *struct {
		PowVersion      int64  `json:"pow_version"`
		RandomXSeedHash string `json:"randomx_seed_hash"`
		SeedHeight      int64  `json:"seed_height"`
		NonceOffset     int    `json:"nonce_offset"`
		NonceSize       int    `json:"nonce_size"`
	} `json:"brisvia"`
}

// GetTemplate override：标准 GBT → blob 作业（adapter.BlobWork）。
func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	bt, err := c.Client.GetTemplate(ctx) // 复用：给 Height/PrevHash/NetworkTarget/CoinbaseValue/MinTime/Raw(json)
	if err != nil {
		return nil, err
	}
	raw, ok := bt.Raw.(json.RawMessage)
	if !ok {
		return nil, fmt.Errorf("brisviarpc: BlockTemplate.Raw 非 json.RawMessage（got %T）", bt.Raw)
	}
	var g gbtView
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("brisviarpc: 解析 GBT: %w", err)
	}

	payScript, err := c.getPayoutScript(ctx)
	if err != nil {
		return nil, err
	}
	var wc []byte
	if g.DefaultWitnessCommitment != "" {
		if wc, err = hex.DecodeString(g.DefaultWitnessCommitment); err != nil {
			return nil, fmt.Errorf("brisviarpc: default_witness_commitment 非 hex: %w", err)
		}
	}

	// 透明 coinbase（extranonce=0：blob 模型固定 coinbase，连接靠 nonce 区分）。
	cb, err := btcwork.BuildCoinbase(g.Height, g.CoinbaseValue, payScript, wc, poolTag, 0)
	if err != nil {
		return nil, fmt.Errorf("brisviarpc: 建 coinbase: %w", err)
	}
	cbTxid, err := cb.TxID(nil, nil)
	if err != nil {
		return nil, err
	}

	// 其余交易：txid 转内部序算 merkle；data 原样进块。
	var otherTxids, rawTxs [][]byte
	for _, tx := range g.Transactions {
		// segwit 链（brisvia/Bitcoin）GBT 出 txid（≠ wtxid，merkle 必须用 txid）；
		// 非 segwit 链（PIVX/Noctari）GBT 不出 txid、只出 hash（== txid）→ 回退 hash。
		txidHex := tx.TxID
		if txidHex == "" {
			txidHex = tx.Hash
		}
		id, err := hex.DecodeString(txidHex)
		if err != nil || len(id) != 32 {
			return nil, fmt.Errorf("brisviarpc: 交易 txid 非法 %q", txidHex)
		}
		data, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, fmt.Errorf("brisviarpc: 交易 data 非 hex")
		}
		otherTxids = append(otherTxids, btcwork.Reverse(id))
		rawTxs = append(rawTxs, data)
	}
	merkleRoot := btcwork.MerkleRootFromBranch(cbTxid, btcwork.MerkleBranch(otherTxids))

	prevBE, err := hex.DecodeString(g.PreviousBlockHash)
	if err != nil || len(prevBE) != 32 {
		return nil, fmt.Errorf("brisviarpc: previousblockhash 非法")
	}
	bits, err := parseHexU32(g.Bits)
	if err != nil {
		return nil, fmt.Errorf("brisviarpc: bits 非法 %q", g.Bits)
	}
	header80 := btcwork.SerializeHeader(uint32(g.Version), btcwork.Reverse(prevBE), merkleRoot,
		uint32(g.CurTime), bits, 0)

	seedHex, err := c.seedHash(ctx, g.Height, &g)
	if err != nil {
		return nil, err
	}
	netTarget, err := btcwork.TargetFromHex(g.Target)
	if err != nil {
		return nil, fmt.Errorf("brisviarpc: target 非法: %w", err)
	}
	cbWitness, err := cb.SerializeWitness(nil, nil)
	if err != nil {
		return nil, err
	}

	bt.Raw = &adapter.BlobWork{
		HashingBlob: header80,
		NonceOffset: nonceOffset,
		NonceLen:    nonceLen,
		// ★SearchLen=3 → 最高 1 字节留作【连接 tag】（cnjob materialize 按 connID 写入）。
		// 曾是 4（全 nonce 给单连接）＝ tagBytes 0 ＝ 所有连接拿到逐字节相同的 blob，
		// 而锄头每换 job 都从 nonce 0 起扫 → N 台矿机算出完全相同的 hash 序列。
		// 去重是 per-connection 的，重复 share 照收照计分，但池实际覆盖的 nonce 空间
		// 只等于一台矿机，爆块率不随接入算力增长（矿工侧完全看不出异常）。
		// 每连接搜索空间 2^24：20 kH/s 需 ~838s、9654 满速 ~266s 才扫完，均远长于
		// 一个 job 的寿命（目标块 120s）。连接数上限 256，由 SetConnIDSpace 池化保证不撞。
		SearchLen:       searchLen,
		SeedHash:        seedHex,
		Algo:            "rx/brva",
		NetworkTarget:   netTarget,
		HashBigEndian:   false, // rx_hash 是 LE（monero 惯例）
		TargetCompactLE: true,  // 下发 8-hex compact LE（NTMminer rx 系认）
		// xmrig-brisvia 兼容双件套：
		// ① Nicehash → login extensions 声明 "nicehash"，XMRig 只滚低 3 字节、保留
		//    blob[79] 的连接 tag（NTMminer 是硬编码契约不看扩展；stock XMRig 不声明
		//    就会滚满 4 字节覆盖 tag → 池端按 connID 重建 tag 重算 → 100% badpow）。
		// ② BrvaJobMode="pplns" → 跳过 xmrig-brisvia 的 solo coinbase 收款校验
		//    （矿池 coinbase 付池地址，不带该字段任务直接被拒 code 8）。
		Nicehash:       true,
		BrvaJobMode:    "pplns",
		PowIsBlockHash: false, // 块 hash = sha256d(header) ≠ rx_hash
		HeightHint:     g.Height,
		JobKey:         fmt.Sprintf("%d-%s", g.Height, g.PreviousBlockHash),
		SubmitRef:      &submitRef{coinbaseWitness: cbWitness, rawTxs: rawTxs},
	}
	return bt, nil
}

// SubmitBlob 组全块提交。sol.Blob = 已写中标 nonce 的 80 字节头。
func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	ref, ok := sol.Work.SubmitRef.(*submitRef)
	if !ok {
		return "", fmt.Errorf("brisviarpc: SubmitRef 类型错误 %T", sol.Work.SubmitRef)
	}
	if len(sol.Blob) != 80 {
		return "", fmt.Errorf("brisviarpc: header 非 80 字节（got %d）", len(sol.Blob))
	}
	blockHex := btcwork.AssembleBlock(sol.Blob, ref.coinbaseWitness, ref.rawTxs)
	if err := c.Client.SubmitBlock(ctx, blockHex); err != nil {
		return "", err
	}
	// 块 hash = reverse(sha256d(header80)) 显示序（节点也这么算）。
	return hex.EncodeToString(btcwork.Reverse(btcwork.HeaderHash(sol.Blob))), nil
}

// BuildBlockHex 组一个 nonce=0 的完整块 hex（仅供 getblocktemplate proposal 验证组块结构；不提交）。
// 返回 (blockHex, height)。用于确定性验证 coinbase/merkle/witness commitment/序列化正确。
func (c *Client) BuildBlockHex(ctx context.Context) (string, uint64, error) {
	bt, err := c.GetTemplate(ctx)
	if err != nil {
		return "", 0, err
	}
	work, ok := bt.Raw.(*adapter.BlobWork)
	if !ok {
		return "", 0, fmt.Errorf("brisviarpc: Raw 非 BlobWork")
	}
	ref, ok := work.SubmitRef.(*submitRef)
	if !ok {
		return "", 0, fmt.Errorf("brisviarpc: SubmitRef 非 submitRef")
	}
	return btcwork.AssembleBlock(work.HashingBlob, ref.coinbaseWitness, ref.rawTxs), work.HeightHint, nil
}

// seedHash 取本高度的 RandomX seed（内部序 64 hex）。
//
// ★权威来源 = 节点 GBT 的 brisvia.randomx_seed_hash。节点源码 rpc/mining.cpp 注释明写
// 「The node (Core) is the authority for the seed」，且它按【活跃分支的父块 + 高度】解析，
// 跨 seed 轮换边界（64、2112、…）与重组时都与验证端一致。
//
// 池按高度自算会在两处偏离节点（2026-07-31 regtest 实测踩到）：
//
//	① 初始 seed（h<64）是【每条链各自的 chainparams 常量】——regtest 是 0x42×32，
//	   main/testnet 才是 0x54×32。硬编码 0x54 会让 regtest 的 h<64 全程算错 seed：
//	   池算出的 hash 与节点不一致，而 regtest target 极松（0x207fffff）→ 错的 hash 仍有
//	   约一半概率满足 target，于是「一半爆块成功、一半 bad-randomx-pow」，池侧
//	   badpow=0 照旧（那只证明矿工↔池一致，从不证明池↔节点一致）→ 极难察觉。
//	② 自算走主链 BlockHashAt，重组时可能取到与节点解析分支不同的块。
//
// 节点未下发该字段（旧版节点）时回退自算，并告警一次。
func (c *Client) seedHash(ctx context.Context, height uint64, g *gbtView) (string, error) {
	if g != nil && g.Brisvia != nil && g.Brisvia.RandomXSeedHash != "" {
		b, err := hex.DecodeString(g.Brisvia.RandomXSeedHash)
		if err != nil || len(b) != 32 {
			return "", fmt.Errorf("brisviarpc: GBT randomx_seed_hash 非法 %q", g.Brisvia.RandomXSeedHash)
		}
		return hex.EncodeToString(btcwork.Reverse(b)), nil // 显示序 → 内部序 = RandomX key
	}
	seedFallbackWarn.Do(func() {
		log.Printf("[brisviarpc] ⚠ 节点 GBT 未下发 brisvia.randomx_seed_hash，回退按高度自算 seed；"+
			"若本链的初始 seed 不是 %s… 则 h<%d 的块会被节点判 bad-randomx-pow", brisviaInitialSeedHex[:8], seedDelay)
	})
	if height < seedDelay {
		return brisviaInitialSeedHex, nil
	}
	seedHeight := ((height - seedDelay) / seedPeriod) * seedPeriod
	disp, err := c.Client.BlockHashAt(ctx, seedHeight)
	if err != nil {
		return "", fmt.Errorf("brisviarpc: 取 seed 块 %d hash: %w", seedHeight, err)
	}
	b, err := hex.DecodeString(disp)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("brisviarpc: seed 块 hash 非法 %q", disp)
	}
	return hex.EncodeToString(btcwork.Reverse(b)), nil // 内部序 = RandomX key
}

func (c *Client) getPayoutScript(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if c.payoutScript != nil {
		defer c.mu.Unlock()
		return c.payoutScript, nil
	}
	c.mu.Unlock()
	scriptHex, err := c.Client.PayoutScript(ctx, c.poolAddress)
	if err != nil {
		return nil, fmt.Errorf("brisviarpc: 取矿池地址 scriptPubKey: %w", err)
	}
	script, err := hex.DecodeString(scriptHex)
	if err != nil {
		return nil, fmt.Errorf("brisviarpc: scriptPubKey 非 hex")
	}
	c.mu.Lock()
	c.payoutScript = script
	c.mu.Unlock()
	return script, nil
}

func parseHexU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 32)
	return uint32(v), err
}
