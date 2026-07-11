package hasher

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
)

// TestMidVDFChainKAT 真链块终极锚：用主网真实纯 coinbase 块（/block/195389，
// 2026-07-11 取自生产节点）离线重放整条挖矿共识路径：
//
//	post_tx_midstate = fold(prev_midstate, coinbase coin_ids…, state_root)
//	mining_hash      = blake3(prev_header_hash ‖ post_tx ‖ state_root ‖ ts_le8 ‖ target)
//	midvdf(mining_hash, nonce) == extension.final_hash 且 < target
//
// 一次钉死 coin_id / hash_concat 折叠 / header hash / VDF vs 真主链。
// （重放代码只活在测试；生产池对节点下发的 mining_midstate 纯透传。）
func TestMidVDFChainKAT(t *testing.T) {
	if testing.Short() {
		t.Skip("全量 1M 迭代 VDF，-short 跳过")
	}
	raw, err := os.ReadFile("testdata/midstate_block_195389.json")
	if err != nil {
		t.Fatal(err)
	}
	var blk midBatchJSON
	if err := json.Unmarshal(raw, &blk); err != nil {
		t.Fatal(err)
	}
	if len(blk.Transactions) != 0 {
		t.Fatalf("KAT 块必须纯 coinbase（txs=%d）", len(blk.Transactions))
	}

	// ① 折叠 coinbase coin_ids + state_root（Batch::header() 重放，空 tx 路径）
	ms := [32]byte(blk.PrevMidstate)
	for _, cb := range blk.Coinbase {
		var buf [72]byte
		copy(buf[:32], cb.Address[:])
		binary.LittleEndian.PutUint64(buf[32:40], cb.Value)
		copy(buf[40:], cb.Salt[:])
		coinID := blake3Short(buf[:])
		ms = blake3Short(append(append([]byte{}, ms[:]...), coinID[:]...))
	}
	zero := [32]byte{}
	if blk.StateRoot != zero {
		ms = blake3Short(append(append([]byte{}, ms[:]...), blk.StateRoot[:]...))
	}

	// ② compute_header_hash（types.rs）：prev_header_hash‖post_tx‖state_root‖ts_le8‖target
	pre := make([]byte, 0, 136)
	pre = append(pre, blk.PrevHeaderHash[:]...)
	pre = append(pre, ms[:]...)
	pre = append(pre, blk.StateRoot[:]...)
	var ts [8]byte
	binary.LittleEndian.PutUint64(ts[:], blk.Timestamp)
	pre = append(pre, ts[:]...)
	pre = append(pre, blk.Target[:]...)
	miningHash := blake3Short(pre)

	// ③ 全量 VDF：== 链上 final_hash 且 < target
	got, err := (midVDF{iters: midIterations}).Hash(MidSeed(miningHash[:], blk.Extension.Nonce))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blk.Extension.FinalHash[:]) {
		t.Fatalf("真链 KAT 失配:\n got=%x\nwant=%x", got, blk.Extension.FinalHash)
	}
	if bytes.Compare(got, blk.Target[:]) >= 0 {
		t.Fatalf("final_hash 未小于 target: %x >= %x", got, blk.Target)
	}
}

// ---- 测试专用：真链块 JSON 解码 + 单 chunk 多块 BLAKE3 ----

type byte32Arr [32]byte

func (a *byte32Arr) UnmarshalJSON(b []byte) error {
	var v [32]uint8
	type alias [32]uint8
	if err := json.Unmarshal(b, (*alias)(&v)); err != nil {
		return err
	}
	*a = v
	return nil
}

type midBatchJSON struct {
	PrevMidstate   byte32Arr         `json:"prev_midstate"`
	PrevHeaderHash byte32Arr         `json:"prev_header_hash"`
	StateRoot      byte32Arr         `json:"state_root"`
	Target         byte32Arr         `json:"target"`
	Timestamp      uint64            `json:"timestamp"`
	Transactions   []json.RawMessage `json:"transactions"`
	Coinbase       []struct {
		Address byte32Arr `json:"address"`
		Value   uint64    `json:"value"`
		Salt    byte32Arr `json:"salt"`
	} `json:"coinbase"`
	Extension struct {
		Nonce     uint64    `json:"nonce"`
		FinalHash byte32Arr `json:"final_hash"`
	} `json:"extension"`
}

// （blake3Short 通用短消息 BLAKE3 在 midvdf.go——盐派生与本测试重放共用。）
