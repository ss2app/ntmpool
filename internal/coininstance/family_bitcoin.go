package coininstance

import (
	"context"
	"fmt"
	"log"

	"github.com/scashcc/ntmpool/internal/adapter"
	"github.com/scashcc/ntmpool/internal/adapter/bitcoinrpc"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/core"
	"github.com/scashcc/ntmpool/internal/hasher"
	"github.com/scashcc/ntmpool/internal/jobmanager"
	"github.com/scashcc/ntmpool/internal/metrics"
	"github.com/scashcc/ntmpool/internal/stratum"
)

// buildFamily 按 cfg.Adapter 选链家族并拼装部件（M3 选型化的入口）。
func buildFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	switch cfg.Adapter {
	case "", "bitcoin-rpc":
		return buildBitcoinFamily(ctx, cfg, decimals, inst)
	case "custom-http", "cryptonote-rpc", "zephyr-rpc":
		return buildBlobFamily(ctx, cfg, decimals, inst)
	case "dragonx-rpc":
		return buildDragonXFamily(ctx, cfg, decimals, inst)
	case "brisvia-rpc":
		return buildBrisviaFamily(ctx, cfg, decimals, inst)
	case "noctari-rpc":
		return buildNoctariFamily(ctx, cfg, decimals, inst)
	case "btc09-http":
		return buildBtc09Family(ctx, cfg, decimals, inst)
	case "midstate-rpc":
		return buildMidstateFamily(ctx, cfg, decimals, inst)
	case "velkar-rpc":
		return buildVelkarFamily(ctx, cfg, decimals, inst)
	default:
		return nil, errf("[%s] 未知适配器 %q（可选 bitcoin-rpc / cryptonote-rpc / zephyr-rpc / custom-http / dragonx-rpc / brisvia-rpc / noctari-rpc / btc09-http / midstate-rpc / velkar-rpc）", cfg.ID, cfg.Adapter)
	}
}

// buildBitcoinFamily bitcoin-rpc + GBT jobmanager + stratum1 方言（M1 竖切原管线）。
func buildBitcoinFamily(ctx context.Context, cfg config.CoinConfig, decimals int, inst *Instance) (*familyParts, error) {
	n := cfg.Nodes[0]
	node := bitcoinrpc.New(cfg.ID, n.URL, n.User, n.Pass)
	node.SetDecimals(decimals)

	hsh, err := hasher.Get(cfg.Algo)
	if err != nil {
		return nil, err
	}

	reg := stratum.NewJobRegistry()
	jm := jobmanager.New(cfg.ID, node, hsh, reg, extraNonce2Size, decimals)
	if err := jm.Init(ctx, cfg.PoolAddress); err != nil {
		return nil, errf("[%s] 初始化矿池地址脚本失败: %v", cfg.ID, err)
	}
	dialect := stratum.NewV1Dialect(cfg.ID, jm)
	sink := inst.blockSink()
	jm.SetCallbacks(dialect.BroadcastJob,
		func(ctx context.Context, b core.FoundBlock, rawHex string, submit jobmanager.SubmitFunc) error {
			return sink(ctx, b, rawHex, submit)
		}, inst.shareSink())

	return &familyParts{
		status:     node,
		hashps:     node, // bitcoin 系有 getnetworkhashps 真值口径
		classifier: node,
		wallet:     node,
		jobs:       jm,
		dialects:   map[string]stratum.Dialect{"stratum1": dialect},
		connCount:  dialect.ConnCount,
	}, nil
}

// blockSink 爆块生命周期回调（家族共用）：意图先落库(submitting) → 交节点 →
// 成功则改用权威 hash 并转 pending，失败留 submitting 交分类器/恢复扫描按链上比对
// 归位（docs/05 场景B「意图先落库，动作后执行」）。submit 由作业管理器提供：
// bitcoin 系返回原 hash，blob 系返回节点受理后的权威块 id。
func (inst *Instance) blockSink(rewardSources ...adapter.BlockRewardSource) func(ctx context.Context, b core.FoundBlock, rawHex string, submit func(context.Context) (string, error)) error {
	var rewardSource adapter.BlockRewardSource
	if len(rewardSources) > 0 {
		rewardSource = rewardSources[0]
	}
	return func(ctx context.Context, b core.FoundBlock, rawHex string, submit func(context.Context) (string, error)) error {
		coin := inst.cfg.ID
		// ① 意图先落库
		if err := inst.ledger.RecordBlock(ctx, b, rawHex); err != nil {
			log.Printf("[%s] ★爆块意图落库失败 height=%d: %v", coin, b.Height, err)
			return err
		}
		// ② 交节点
		finalHash, err := submit(ctx)
		if err != nil {
			// 提交失败：意图留 submitting，分类器按 Confirmations/主链 hash 比对归位；
			// share 已计权矿工不吃亏，未入账不多付（守恒不破）。
			log.Printf("[%s] 爆块提交失败 height=%d intent=%s: %v（留待分类器归位）",
				coin, b.Height, short(b.Hash), err)
			return nil
		}
		// ③ 补权威 hash（blob 链 id≠PoW hash）+ 转 pending
		if finalHash != "" && finalHash != b.Hash {
			if err := inst.ledger.UpdateBlockHash(ctx, coin, b.Hash, finalHash); err != nil {
				log.Printf("[%s] 爆块 hash 补录失败 %s→%s: %v", coin, short(b.Hash), short(finalHash), err)
			} else {
				b.Hash = finalHash
			}
		}
		// 多资产链的模板 expected_reward 不是池会计资产的真实矿工收入。
		// 必须先按权威 block id 查询并回填，再允许转 pending/进入确认流程。
		if rewardSource != nil {
			reward, err := rewardSource.BlockReward(ctx, b.Hash)
			if err != nil {
				log.Printf("[%s] 爆块真实 reward 查询失败 hash=%s: %v（保持 submitting）", coin, short(b.Hash), err)
				return err
			}
			if err := inst.ledger.UpdateBlockReward(ctx, coin, b.Hash, reward); err != nil {
				log.Printf("[%s] 爆块真实 reward 回填失败 hash=%s: %v（保持 submitting）", coin, short(b.Hash), err)
				return err
			}
			b.Reward = reward
		}
		if err := inst.ledger.MarkBlockPending(ctx, coin, b.Hash); err != nil {
			log.Printf("[%s] 爆块转 pending 失败 hash=%s: %v", coin, short(b.Hash), err)
		}
		metrics.BlockSubmitted(coin)
		log.Printf("[%s] ★爆块 height=%d hash=%s finder=%s", coin, b.Height, short(b.Hash), b.Finder)
		inst.notifyEvent("block_found", "★爆块", map[string]string{
			"height": fmt.Sprint(b.Height), "hash": b.Hash,
			"finder": short(b.Finder), "reward": b.Reward,
		})
		return nil
	}
}

// shareSink accepted share 记账回调（家族共用）。
func (inst *Instance) shareSink() func(ctx context.Context, s core.Share) {
	return func(ctx context.Context, s core.Share) {
		_ = inst.ledger.RecordShare(ctx, s, s.Difficulty)
		inst.tracker.Record(s.Address, s.Worker, s.Difficulty, s.At)
	}
}

// 编译期断言：bitcoinrpc.Client 同时满足家族所需的全部面。
var _ adapter.HashPSSource = (*bitcoinrpc.Client)(nil)
