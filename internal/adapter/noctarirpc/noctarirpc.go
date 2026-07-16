// Package noctarirpc Noctari (NCTI) 适配器：透明比特币 getblocktemplate × blob 作业
// 管线（cnjob + CN 方言），PoW = quark_hash(header80)，无 key（不像 RandomX 需 seed）。
//
// Noctari = PIVX v5 fork 的 7 天公平启动 PoW 窗口（区块 1..20159），块头是标准 80 字节
// 比特币头（version|prev|merkle|time|bits|nonce，nonce@offset76 LE），节点
// CBlockHeader::GetHash() = HashQuark(header80)。故本适配【内嵌 brisviarpc.Client】
// 直接复用它已在 Brisvia 主网验证过的标准 bitcoin GBT → 80B 头 blob 物化 + coinbase/
// merkle/见证/组块提交那一整套（字节级已证），只改三处 Noctari 特有的口径：
//
//	① 算法名：BlobWork.Algo 从 "rx/brva" 改 "quark"（下发矿工 job.algo + 关掉 cnjob
//	   对 rx/ 系 seed_hash 必 64hex 的校验——quark 无 seed）。
//	② seed：BlobWork.SeedHash 清空（CN 方言 seed_hash 字段 omitempty 直接不下发，
//	   矿工忽略；seed 绝不进入 quark 哈希）。
//	③ 块 hash 口径：quark 链 PowIsBlockHash=true，块 id = reverse(quark_hash)，
//	   而非 brisviarpc 的 reverse(sha256d(header80))——SubmitBlob 覆盖为前者。
//
// ⚠ 复用 brisviarpc.GetTemplate 会顺带算一个 RandomX epoch seed（一次 getblockhash）
// 然后被本适配丢弃——为换取「组块字节序列化零重写」的共识安全，接受这点小浪费；
// 同步节点上该 RPC 不会失败。若日后要省掉，再抽公共 bitcoin-GBT→blob 底座。
//
// 透明钱包（sendmany + RawTx 拆步打款）经 brisviarpc→bitcoinrpc 内嵌继承，与 Brisvia 同。
package noctarirpc

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/brisviarpc"
	"github.com/scashcc/ntmpool/internal/btcwork"
)

// Client 内嵌 brisviarpc.Client（透明 bitcoin GBT → blob 全套 + 透明钱包），
// 覆盖 GetTemplate/SubmitBlob 两处以贴合 Noctari 的 quark 口径。
type Client struct {
	*brisviarpc.Client
}

// 编译期断言：blob 作业管线要的两个能力仍满足。
var _ interface {
	GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error)
	SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error)
} = (*Client)(nil)

// New 与 brisviarpc.New 同参：poolAddress = 矿池透明收款地址（coinbase 直付于此）。
func New(name, url, user, pass, poolAddress string) *Client {
	return &Client{Client: brisviarpc.New(name, url, user, pass, poolAddress)}
}

// GetTemplate 复用 brisviarpc 的标准 bitcoin GBT → 80B 头 blob 物化，再改成 quark 口径。
func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	bt, err := c.Client.GetTemplate(ctx)
	if err != nil {
		return nil, err
	}
	work, ok := bt.Raw.(*adapter.BlobWork)
	if !ok {
		return nil, fmt.Errorf("noctarirpc: BlockTemplate.Raw 非 *BlobWork（got %T）", bt.Raw)
	}
	// ① 算法名 → quark（下发 job.algo；cnjob.validateWork 不再要求 rx/ 系 seed）。
	work.Algo = "quark"
	// ② 清空 seed（quark 无 key；CN 方言 seed_hash omitempty 不下发，矿工忽略）。
	work.SeedHash = ""
	// ③ quark 链块 hash = reverse(quark_hash)，即 PoW 反转就是链上块 id。
	work.PowIsBlockHash = true
	return bt, nil
}

// SubmitBlob 组块提交复用 brisviarpc（标准 bitcoin 块序列化，已字节级验证），
// 但返回的权威块 hash 用 Noctari 口径：reverse(quark_hash)（节点 GetHash()=HashQuark），
// 而非 brisviarpc 返回的 reverse(sha256d(header80))。
func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	if _, err := c.Client.SubmitBlob(ctx, sol); err != nil {
		return "", err
	}
	// sol.Hash = cnjob 重算的 quark_hash(header80)（LE 原始 32B）；反转即显示序块 id。
	return hex.EncodeToString(btcwork.Reverse(sol.Hash)), nil
}
