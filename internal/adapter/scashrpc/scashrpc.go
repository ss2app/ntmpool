// Package scashrpc 对标准 bitcoinrpc.Client 增补 SCASH 的 GBT 与 112B 提交语义。
package scashrpc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/bitcoinrpc"
	"github.com/scashcc/ntmpool/internal/btcwork"
	"github.com/scashcc/ntmpool/internal/hasher/scashrx"
)

// Transaction 保留 GBT 中组 merkle 与最终块所需的完整交易信息。
type Transaction struct {
	Data string `json:"data"`
	TxID string `json:"txid"`
	Hash string `json:"hash"`
}

// Template 是 SCASH GBT 的完整池侧视图。RxEpochDuration 缺失或为零时拒绝模板，
// 不猜主网常量，避免跨 network 或未来参数变动时静默选错 key。
type Template struct {
	Version                  uint32        `json:"version"`
	PreviousBlockHash        string        `json:"previousblockhash"`
	Transactions             []Transaction `json:"transactions"`
	CoinbaseValue            int64         `json:"coinbasevalue"`
	Target                   string        `json:"target"`
	MinTime                  int64         `json:"mintime"`
	CurTime                  int64         `json:"curtime"`
	Bits                     string        `json:"bits"`
	Height                   uint64        `json:"height"`
	DefaultWitnessCommitment string        `json:"default_witness_commitment"`
	Mutable                  []string      `json:"mutable"`
	RxEpochDuration          uint64        `json:"rx_epoch_duration"`
}

// Solution 由 scashjob 携带；R 必须来自池端对同一 zeroed candidate 的重算。
type Solution struct {
	ZeroedHeader    []byte
	Nonce           uint32
	R               [32]byte
	CoinbaseWitness []byte
	RawTxs          [][]byte
}

type SolvedBlock struct {
	Header   []byte
	BlockHex string
	ID       string
}

type Client struct{ *bitcoinrpc.Client }

func New(name, url, user, pass string) *Client {
	return &Client{Client: bitcoinrpc.New(name, url, user, pass)}
}

var _ adapter.NodeAdapter = (*Client)(nil)

// GetTemplate 复用 bitcoinrpc 的认证、longpoll bookkeeping 与通用字段，再把 Raw
// 提升为经严格校验的 SCASH Template。
func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	bt, err := c.Client.GetTemplate(ctx)
	if err != nil {
		return nil, err
	}
	raw, ok := bt.Raw.(json.RawMessage)
	if !ok {
		return nil, fmt.Errorf("scashrpc: BlockTemplate.Raw 非 json.RawMessage（got %T）", bt.Raw)
	}
	var t Template
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("scashrpc: 解析 GBT: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("scashrpc: 解析 GBT 字段集合: %w", err)
	}
	for _, required := range []string{"transactions", "default_witness_commitment", "coinbasevalue", "target", "rx_epoch_duration"} {
		if _, ok := fields[required]; !ok {
			return nil, fmt.Errorf("scashrpc: GBT 缺字段 %s", required)
		}
	}
	if err := validateTemplate(&t); err != nil {
		return nil, err
	}
	bt.Raw = &t
	return bt, nil
}

func validateTemplate(t *Template) error {
	if t.RxEpochDuration == 0 {
		return fmt.Errorf("scashrpc: GBT 缺少非零 rx_epoch_duration（fail closed）")
	}
	if t.CurTime <= 0 || uint64(t.CurTime) > uint64(^uint32(0)) || t.MinTime < 0 ||
		t.Bits == "" || t.Target == "" || t.PreviousBlockHash == "" {
		return fmt.Errorf("scashrpc: GBT 缺关键字段（curtime/bits/target/previousblockhash）")
	}
	if !hexLen(t.PreviousBlockHash, 32) {
		return fmt.Errorf("scashrpc: previousblockhash 非 32 字节 hex")
	}
	if !hexLen(t.Target, 32) {
		return fmt.Errorf("scashrpc: target 非 32 字节 hex")
	}
	if !hexLen(t.Bits, 4) {
		return fmt.Errorf("scashrpc: bits 非 4 字节 hex")
	}
	if t.DefaultWitnessCommitment != "" {
		if _, err := hex.DecodeString(t.DefaultWitnessCommitment); err != nil {
			return fmt.Errorf("scashrpc: default_witness_commitment 非 hex: %w", err)
		}
	}
	for i, tx := range t.Transactions {
		if tx.Data == "" {
			return fmt.Errorf("scashrpc: transactions[%d].data 为空", i)
		}
		if _, err := hex.DecodeString(tx.Data); err != nil {
			return fmt.Errorf("scashrpc: transactions[%d].data 非 hex", i)
		}
		// SCASH 是 SegWit Bitcoin fork；merkle 必须用 txid，不能拿 wtxid/hash 猜。
		if !hexLen(tx.TxID, 32) {
			return fmt.Errorf("scashrpc: transactions[%d].txid 缺失或非法", i)
		}
	}
	return nil
}

func hexLen(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n
}

// BuildSolvedBlock 只接受 zeroed112，写入 submit nonce 与池端重算的 R 后组块。
func BuildSolvedBlock(sol *Solution) (*SolvedBlock, error) {
	if sol == nil {
		return nil, fmt.Errorf("scashrpc: nil solution")
	}
	if !scashrx.IsZeroedHeader(sol.ZeroedHeader) {
		return nil, fmt.Errorf("scashrpc: candidate 必须是尾 32B 全零的 112B header")
	}
	header := append([]byte(nil), sol.ZeroedHeader...)
	binary.LittleEndian.PutUint32(header[76:80], sol.Nonce)
	copy(header[scashrx.RXOffset:], sol.R[:])
	blockHex := btcwork.AssembleBlock(header, sol.CoinbaseWitness, sol.RawTxs)
	id := scashrx.DisplayHex(btcwork.HeaderHash(header))
	return &SolvedBlock{Header: header, BlockHex: blockHex, ID: id}, nil
}

// SubmitSolution 在 adapter 内完成 112B solved header 构造与 submitblock。
func (c *Client) SubmitSolution(ctx context.Context, sol *Solution) (string, error) {
	block, err := BuildSolvedBlock(sol)
	if err != nil {
		return "", err
	}
	if err := c.Client.SubmitBlock(ctx, block.BlockHex); err != nil {
		return "", err
	}
	return block.ID, nil
}
