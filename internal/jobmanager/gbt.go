// Package jobmanager 把节点模板变成可挖 job、校验 share、命中组块提交。
// 是 btcwork + adapter + hasher + stratum 的粘合核心。
package jobmanager

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// gbtTemplate 是 bitcoin 系 getblocktemplate 的解析结构（只取组块必需字段）。
type gbtTemplate struct {
	Version           uint32   `json:"version"`
	PreviousBlockHash string   `json:"previousblockhash"`
	Transactions      []gbtTx  `json:"transactions"`
	CoinbaseValue     int64    `json:"coinbasevalue"`
	Target            string   `json:"target"`
	MinTime           int64    `json:"mintime"`
	CurTime           int64    `json:"curtime"`
	Bits              string   `json:"bits"`
	Height            uint64   `json:"height"`
	WitnessCommitment string   `json:"default_witness_commitment"`
	Mutable           []string `json:"mutable"`
}

type gbtTx struct {
	Data string `json:"data"` // 完整交易 hex（含见证）
	TxID string `json:"txid"` // BE hex
	Hash string `json:"hash"` // wtxid BE hex（见证 merkle 用，M1 空块不需要）
}

func parseGBT(raw json.RawMessage) (*gbtTemplate, error) {
	var t gbtTemplate
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("解析 GBT: %w", err)
	}
	if t.Height == 0 && t.PreviousBlockHash == "" {
		return nil, fmt.Errorf("GBT 缺关键字段（height/previousblockhash）")
	}
	return &t, nil
}

// parseBits GBT bits 字段（BE hex，如 "1d00ffff"）→ uint32。
func parseBits(bitsHex string) (uint32, error) {
	b, err := hex.DecodeString(bitsHex)
	if err != nil || len(b) != 4 {
		return 0, fmt.Errorf("非法 bits: %q", bitsHex)
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), nil
}
