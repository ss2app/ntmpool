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
//   - RandomX 算力低（每核数千 H/s），4 字节 nonce 空间数小时够用 → 无需 extranonce 滚动，
//     全 nonce 给单连接（SearchLen=NonceLen=4，无连接 tag）。⚠ 上生产若单机算力极大或
//     >数百连接同挖，再评估 per-connection extranonce / ntime 滚动（TODO，非冒烟阻塞）。
package brisviarpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
)

// brisviaInitialSeedHex 高度 0..63 用的固定初始 seed = 32 × 0x54（main/test 相同，spec §6）。
var brisviaInitialSeedHex = strings.Repeat("54", 32)

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

	seedHex, err := c.seedHash(ctx, g.Height)
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
		HashingBlob:     header80,
		NonceOffset:     76,
		NonceLen:        4,
		SearchLen:       4, // 全 nonce 给单连接（RandomX 低算力足够；无连接 tag）
		SeedHash:        seedHex,
		Algo:            "rx/brva",
		NetworkTarget:   netTarget,
		HashBigEndian:   false, // rx_hash 是 LE（monero 惯例）
		TargetCompactLE: true,  // 下发 8-hex compact LE（NTMminer rx 系认）
		PowIsBlockHash:  false, // 块 hash = sha256d(header) ≠ rx_hash
		HeightHint:      g.Height,
		JobKey:          fmt.Sprintf("%d-%s", g.Height, g.PreviousBlockHash),
		SubmitRef:       &submitRef{coinbaseWitness: cbWitness, rawTxs: rawTxs},
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

// seedHash 按高度算 RandomX seed（内部序 64 hex）。h<64 用固定初始 seed；否则取
// seedHeight=((h-64)/2048)*2048 的块 hash 的内部序（= reverse(display)）。
func (c *Client) seedHash(ctx context.Context, height uint64) (string, error) {
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
