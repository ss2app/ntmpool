// Package velkarrpc Velkar(VELK) 节点适配器 —— Kaspa(rusty-kaspa) 系 gRPC 双向流链。
//
// 形态（docs/08）：节点造好含 coinbase 的完整 RpcBlock（池不自组 coinbase/merkle，比
// bitcoin GBT 简单）；池取 header 算 pre_pow_hash 喂矿工，矿工回 nonce，池把 nonce+
// timestamp 回填整块 SubmitBlock。0ms 触发 = gRPC NewBlockTemplate/BlockAdded 推送
// （velkar 无 Bitcoin ZMQ，Kaspa 系不需要）+ coininstance 2s 轮询兜底。
//
// ⚠ VelkarHash 的 stage4 吃 block target（从 header.bits 解出的网络 target）——这是与
// 普通 blob 链"target 只比较不入哈希"的唯一差别。故用 VelkarWork（非通用 BlobWork）承载
// BlockTargetLE，velkarjob 池端重算时必须把它传进 velkarhash.CalculatePow。
//
// 规则同其它适配器：无状态可重连（client.go supervise 自愈）、金额十进制字符串不过浮点。
// 打款侧（Kaspa UTXO 构造签名 / walletd）第一期延后，本包暂不实现 WalletAdapter。
package velkarrpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc/protowire"
	"github.com/scashcc/ntmpool/internal/core"
	"google.golang.org/protobuf/proto"
)

// VelkarWork 是 velkar 模板的归一化工件（放进 BlockTemplate.Raw，供 velkarjob/stratum 消费）。
// 不用通用 BlobWork：VelkarHash 需要 block target 入哈希（BlockTargetLE），且提交要整块回填。
type VelkarWork struct {
	PrePowHash    []byte              // 32B 内部序：CalculatePow 的 prePow 参数 + 矩阵种子；也是下发矿工的 jobHash
	Timestamp     uint64              // 模板 header.timestamp（毫秒）；矿工只滚 nonce，此值每 job 固定
	BlockTargetLE []byte              // 32B little-endian：CalculatePow 的 target 参数（stage4 吃它）
	BlockTargetBE *big.Int            // 大端：命中网络 target 判爆块 + 展示/难度换算
	Height        uint64              // = header.daaScore（Kaspa 用 DAA score 当"高度"）
	Block         *protowire.RpcBlock // 节点造好的完整块；提交时深拷回填 nonce/timestamp
	JobKey        string              // = hex(PrePowHash)：同 pre_pow 即同一份可挖工作（timestamp 抖动不换 job）
}

// Adapter 一个 velkard 节点的 NodeAdapter 实现。
type Adapter struct {
	name        string
	c           *client
	payAddress  string // 矿池地址（coinbase 收款）；GetBlockTemplate 传入
	coinbaseTag string // 节点写进 coinbase 的 extra data（品牌标记）
	decimals    int
}

var (
	_ adapter.NodeAdapter  = (*Adapter)(nil)
	_ adapter.Notifier     = (*velkarNotifier)(nil)
	_ adapter.HashPSSource = (*Adapter)(nil)
)

// New 建 velkar 适配器并立即启动 gRPC 流（供 GetTemplate/SubmitBlock/Notifier 复用同一条流）。
// addr = host:port（如 127.0.0.1:26210），无 grpc:// 前缀。
func New(name, addr, payAddress string) (*Adapter, error) {
	c, err := newClient(name, addr)
	if err != nil {
		return nil, err
	}
	a := &Adapter{
		name:        name,
		c:           c,
		payAddress:  payAddress,
		coinbaseTag: "NTMPool",
		decimals:    8, // Kaspa 系：1 VELK = 1e8 sompi
	}
	c.start()
	return a, nil
}

func (a *Adapter) Name() string { return a.name }

func (a *Adapter) SetDecimals(d int)         { a.decimals = d }
func (a *Adapter) SetCoinbaseTag(tag string) { a.coinbaseTag = tag }

func (a *Adapter) Close() error { return a.c.Close() }

// ---- NodeAdapter ----

// Status DAG 状态。⚠ 单机 testnet(--enable-unsynced-mining) 恒 not-synced 但仍可挖，
// 故 Synced 只反映"节点可达且有 tip"，池绝不据此拒挖（velkar 特例）。
func (a *Adapter) Status(ctx context.Context) (adapter.ChainStatus, error) {
	dag, err := a.c.getDagInfo(ctx)
	if err != nil {
		return adapter.ChainStatus{}, err
	}
	tip := ""
	if len(dag.TipHashes) > 0 {
		tip = dag.TipHashes[0]
	}
	return adapter.ChainStatus{
		Height:    dag.VirtualDaaScore,
		TipHash:   tip,
		Synced:    true,
		Connected: true,
	}, nil
}

// NetworkHashPS 全网算力真值口径 = 节点原生 estimateNetworkHashesPerSecond
//（按最近 windowSize 个块的难度×实际时间窗计算，等价 bitcoin 系 getnetworkhashps；
// 池端绝不自己反推）。startHash 传空 = 从 virtual(sink) 起算；window 1000 块 ≈ 2.8h。
func (a *Adapter) NetworkHashPS(ctx context.Context) (float64, error) {
	hps, err := a.c.estimateNetworkHashesPerSecond(ctx, 1000)
	if err != nil {
		return 0, err
	}
	return float64(hps), nil
}

// GetTemplate 拉最新模板：节点给完整 RpcBlock（含 coinbase），池取 header 算 pre_pow_hash + block target。
func (a *Adapter) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	resp, err := a.c.getTemplate(ctx, a.payAddress, a.coinbaseTag)
	if err != nil {
		return nil, err
	}
	block := resp.GetBlock()
	if block == nil || block.GetHeader() == nil {
		return nil, fmt.Errorf("[%s] 模板缺区块/头", a.name)
	}
	h := block.Header

	prePow, err := prePowHash(h)
	if err != nil {
		return nil, fmt.Errorf("[%s] 算 pre_pow_hash: %w", a.name, err)
	}
	tgtLE, tgtBE := targetFromBits(h.Bits)
	if tgtBE.Sign() <= 0 {
		return nil, fmt.Errorf("[%s] block target 非法（bits=%#x）", a.name, h.Bits)
	}

	prevHash := ""
	if len(h.Parents) > 0 && len(h.Parents[0].ParentHashes) > 0 {
		prevHash = h.Parents[0].ParentHashes[0]
	}

	work := &VelkarWork{
		PrePowHash:    prePow,
		Timestamp:     uint64(h.Timestamp),
		BlockTargetLE: tgtLE,
		BlockTargetBE: tgtBE,
		Height:        h.DaaScore,
		Block:         block,
		JobKey:        hex.EncodeToString(prePow),
	}

	return &adapter.BlockTemplate{
		Height:        h.DaaScore,
		PrevHash:      prevHash,
		NetworkTarget: fmt.Sprintf("%064x", tgtBE),
		CoinbaseValue: coinbaseValue(block, a.decimals),
		MinTime:       0,
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// SubmitSolved velkarjob 爆块正路：深拷模板块 → 回填 nonce/timestamp → SubmitBlock →
// 返回权威块 hash（= headerHash(真值)）。timestamp 保持模板值不改（矿工据它算 PoW）。
func (a *Adapter) SubmitSolved(ctx context.Context, work *VelkarWork, nonce uint64) (string, error) {
	if work == nil || work.Block == nil || work.Block.Header == nil {
		return "", fmt.Errorf("[%s] SubmitSolved: 空 work/block", a.name)
	}
	// 深拷贝，避免并发提交/复用竞态改到同一 header
	blk := proto.Clone(work.Block).(*protowire.RpcBlock)
	blk.Header.Nonce = nonce
	blk.Header.Timestamp = int64(work.Timestamp)

	resp, err := a.c.submitBlock(ctx, blk)
	if err != nil {
		return "", err
	}
	if e := resp.GetError(); e != nil {
		return "", fmt.Errorf("[%s] submitBlock: %s", a.name, e.GetMessage())
	}
	if rr := resp.GetRejectReason(); rr != protowire.SubmitBlockResponseMessage_NONE {
		return "", fmt.Errorf("[%s] submitBlock 被拒: %v", a.name, rr)
	}
	hb, err := headerHash(blk.Header, nonce, work.Timestamp)
	if err != nil {
		return "", fmt.Errorf("[%s] 算块 hash: %w", a.name, err)
	}
	return hex.EncodeToString(hb), nil
}

// BlockHashOf 计算 work+nonce 对应的链上块 id（= headerHash(真 nonce/ts)），供作业管理器
// 爆块「意图先落库」用占位 hash——与 SubmitSolved 返回的权威 hash 逐字节一致（velkar 块 id
// 就是 header hash，无 blob 链那种 id≠PoW 的两难）。
func BlockHashOf(work *VelkarWork, nonce uint64) (string, error) {
	if work == nil || work.Block == nil || work.Block.Header == nil {
		return "", fmt.Errorf("velkarrpc.BlockHashOf: 空 work/block")
	}
	hb, err := headerHash(work.Block.Header, nonce, work.Timestamp)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hb), nil
}

// SubmitBlock 通用面（raw = *protowire.RpcBlock，已回填 nonce/timestamp）。爆块正路走 SubmitSolved。
func (a *Adapter) SubmitBlock(ctx context.Context, raw any) error {
	blk, ok := raw.(*protowire.RpcBlock)
	if !ok {
		return fmt.Errorf("[%s] SubmitBlock: 期望 *protowire.RpcBlock, got %T（爆块请走 SubmitSolved）", a.name, raw)
	}
	resp, err := a.c.submitBlock(ctx, blk)
	if err != nil {
		return err
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("[%s] submitBlock: %s", a.name, e.GetMessage())
	}
	if rr := resp.GetRejectReason(); rr != protowire.SubmitBlockResponseMessage_NONE {
		return fmt.Errorf("[%s] submitBlock 被拒: %v", a.name, rr)
	}
	return nil
}

// BlockHashAt Kaspa 是 DAG，无线性高度→hash 映射。返回 ErrNoHeightIndex sentinel：
// 打款引擎见此跳过「按高度取主链块比对」这道成熟闸，孤块判定改由 Confirmations 内建
// （isChainBlock，见下）。docs/08 §7。
func (a *Adapter) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	return "", adapter.ErrNoHeightIndex
}

// Confirmations 是 DAG 链孤块 + 确认判定的唯一权威（无线性高度→hash）：
//   - 节点找不到该块 hash → -1（掉出主链，被 pruning）
//   - 块在但不在 selected parent chain（isChainBlock=false，红块/被甩块，coinbase 不会
//     被接受）→ -1（当孤块，绝不打款——防超发铁律的 DAG 等价闸）
//   - 否则 conf ≈ virtualDaaScore − 块 daaScore（Kaspa 无线性确认，用 blue DAA 差）
//
// isChainBlock 由节点 verboseData 提供，实链已验证正确填充（docs/08 §7 探针）。
func (a *Adapter) Confirmations(ctx context.Context, blockHash string, _ uint64) (int64, error) {
	resp, err := a.c.getBlock(ctx, blockHash, false)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return -1, nil
		}
		return 0, err
	}
	blk := resp.GetBlock()
	if blk == nil || blk.GetHeader() == nil {
		return -1, nil
	}
	// DAG 主链判定：块存在但不在 selected parent chain → coinbase 不成熟，当孤块。
	if vd := blk.GetVerboseData(); vd != nil && !vd.GetIsChainBlock() {
		return -1, nil
	}
	dag, err := a.c.getDagInfo(ctx)
	if err != nil {
		return 0, err
	}
	conf := int64(dag.VirtualDaaScore) - int64(blk.Header.DaaScore)
	if conf < 0 {
		conf = 0
	}
	return conf, nil
}

// ---- Notifier（0ms 主推送通道）----

// Notifier 返回本节点的 gRPC 推送通知器（NewBlockTemplate + BlockAdded）。
func (a *Adapter) Notifier() adapter.Notifier { return &velkarNotifier{a: a} }

type velkarNotifier struct{ a *Adapter }

// Run 阻塞把 client 读循环解出的链头事件转 core.TipEvent。流断重连由 client.supervise 自愈，
// 本 Run 不退出（推送断了轮询兜底仍在）。
func (n *velkarNotifier) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	n.a.c.start()
	for {
		select {
		case ev := <-n.a.c.notifyCh:
			out := core.TipEvent{
				Coin:   n.a.name,
				Height: ev.height,
				Hash:   ev.hash,
				Source: ev.source,
				At:     time.Now(),
			}
			select {
			case ch <- out:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---- 工具 ----

// coinbaseValue 汇总 coinbase(transactions[0]) 各 output.amount → 十进制币字符串。
func coinbaseValue(blk *protowire.RpcBlock, decimals int) string {
	txs := blk.GetTransactions()
	if len(txs) == 0 {
		return ""
	}
	var sum uint64
	for _, o := range txs[0].GetOutputs() {
		sum += o.GetAmount()
	}
	return sompiToCoin(sum, decimals)
}

func sompiToCoin(v uint64, decimals int) string {
	div := uint64(1)
	for i := 0; i < decimals; i++ {
		div *= 10
	}
	return fmt.Sprintf("%d.%0*d", v/div, decimals, v%div)
}
