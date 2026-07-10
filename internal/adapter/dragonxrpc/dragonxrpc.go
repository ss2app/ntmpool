// Package dragonxrpc DragonX（Hush/Komodo 系 RandomX 隐私链）节点+钱包适配器。
//
// 形态（docs/06）：bitcoin 系 JSON-RPC 节点（getblocktemplate/submitblock）×
// blob 作业管线（cnjob + CN 方言）× zcash z_* 隐私钱包（异步 opid 打款）——
// 三家族杂交，全部差异收在本包：
//
//   - GetTemplate：GBT → 140B 头 blob（节点给现成 coinbasetxn，池不自组 Sapling tx；
//     部署铁律：DRAGONX.conf 必须钉 pubkey= 到池 R 地址，见 _knowledge/地址簿.md）+
//     seed 自算（interval 1024 / lag 64 → getblockhash → 反转）。
//   - SubmitBlob：blob || 0x20 || rx_hash || txs → submitblock；块 hash = reverse(pow)。
//   - 钱包：SendMany 内部 z_sendmany → opid → 轮询 z_getoperationstatus/result → txid
//     （对上层仍是"一次调用一个 txid"；只查自己的 opid，绝不 drain-all——与主机
//     consolidate 服务的 opid-safe 纪律互不抢）。z_sendmany 4 参，绝不带第 5 参
//     privacy policy（Hush 第 5 参 = donation 坑）。只付 zs 地址。
//   - WalletMaintainer：z_shieldcoinbase R→金库 zs（coinbase 100 确认成熟后回补垫付金库）。
//
// 规则同 bitcoinrpc：无状态可重连、timeout ≥ 120s、金额十进制字符串不过浮点。
package dragonxrpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

const (
	defaultSeedInterval = 1024
	defaultSeedLag      = 64
	// zSendFee z_sendmany / z_shieldcoinbase 手续费（miningcore TransferFee 同值）。
	zSendFee = "0.0001"
	// zMinConf z_sendmany 花费输入的最小确认数。
	zMinConf = 1
	// opTimeout 单个 z_* 异步操作（含 zk 证明生成）的等待上限（miningcore shield
	// 10min 超时先例 + 余量）。
	opTimeout = 15 * time.Minute
	// opPollInterval opid 轮询间隔。
	opPollInterval = 2 * time.Second
)

// Client 一个 dragonxd 节点的适配器。节点即钱包（komodo 内置钱包）。
type Client struct {
	name     string
	url      string
	user     string
	pass     string
	decimals int
	vaultZ   string // 金库 zs：打款出账源 + shield 目标
	hc       *http.Client

	seedInterval uint64
	seedLag      uint64

	// instanceSalt 写进 nonce 保留区 [116:120]：多实例共享 Postgres 时的
	// nonce 空间隔离（miningcore instanceId 同款用途）。每进程随机一次。
	instanceSalt [4]byte

	mu             sync.Mutex
	cachedSeedH    uint64
	cachedSeedHash string // 内部序 64 hex
	seedValid      bool
}

var (
	_ adapter.NodeAdapter      = (*Client)(nil)
	_ adapter.BlobSubmitter    = (*Client)(nil)
	_ adapter.WalletAdapter    = (*Client)(nil)
	_ adapter.WalletMaintainer = (*Client)(nil)
	_ adapter.HashPSSource     = (*Client)(nil)
)

func New(name, url, user, pass, vaultZ string) *Client {
	c := &Client{
		name: name, url: url, user: user, pass: pass,
		decimals: 8, vaultZ: vaultZ,
		seedInterval: defaultSeedInterval, seedLag: defaultSeedLag,
		hc: &http.Client{Timeout: 150 * time.Second},
	}
	_, _ = rand.Read(c.instanceSalt[:])
	return c
}

func (c *Client) Name() string { return c.name }

// ---- JSON-RPC 底座（bitcoinrpc 同款）----

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

const rpcNotFound = -5

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0", "id": "ntmpool", "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("%s %s: 非法响应(http %d): %w", c.name, method, resp.StatusCode, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

func rpcCode(err error) (int, bool) {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

func satoshiToCoin(v int64, decimals int) string {
	div := int64(1)
	for i := 0; i < decimals; i++ {
		div *= 10
	}
	return fmt.Sprintf("%d.%0*d", v/div, decimals, v%div)
}

// ---- NodeAdapter ----

func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	var info struct {
		Blocks        uint64 `json:"blocks"`
		Headers       uint64 `json:"headers"`
		BestBlockHash string `json:"bestblockhash"`
	}
	if err := c.call(ctx, "getblockchaininfo", []any{}, &info); err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{
		Height:    info.Blocks,
		TipHash:   info.BestBlockHash,
		Synced:    info.Blocks >= info.Headers,
		Connected: true,
	}, nil
}

// NetworkHashPS 全网算力真值（komodo 系有 getnetworkhashps；铁律 C7 绝不反推）。
func (c *Client) NetworkHashPS(ctx context.Context) (float64, error) {
	var hps float64
	if err := c.call(ctx, "getnetworkhashps", []any{120, -1}, &hps); err != nil {
		return 0, err
	}
	return hps, nil
}

// drgSubmitRef GBT 交易原文（组块提交用），挂在 BlobWork.SubmitRef。
type drgSubmitRef struct {
	CoinbaseData string
	TxData       []string
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	var t struct {
		Version              uint32 `json:"version"`
		PreviousBlockHash    string `json:"previousblockhash"`
		FinalSaplingRootHash string `json:"finalsaplingroothash"`
		Target               string `json:"target"`
		Bits                 string `json:"bits"`
		CurTime              int64  `json:"curtime"`
		MinTime              int64  `json:"mintime"`
		Height               uint64 `json:"height"`
		CoinbaseTxn          *struct {
			Data          string `json:"data"`
			Hash          string `json:"hash"`
			CoinbaseValue int64  `json:"coinbasevalue"`
		} `json:"coinbasetxn"`
		Transactions []struct {
			Data string `json:"data"`
			Hash string `json:"hash"`
		} `json:"transactions"`
	}
	if err := c.call(ctx, "getblocktemplate", []any{}, &t); err != nil {
		return nil, err
	}
	if t.CoinbaseTxn == nil || t.CoinbaseTxn.Data == "" {
		return nil, fmt.Errorf("[%s] GBT 无 coinbasetxn——节点须配 pubkey=（池 R 地址公钥，见 _knowledge/地址簿.md）", c.name)
	}

	// merkle：coinbase txid + 各 tx txid（display → 内部序 → 标准 bitcoin merkle）
	txids := make([]string, 0, 1+len(t.Transactions))
	txids = append(txids, t.CoinbaseTxn.Hash)
	txData := make([]string, 0, len(t.Transactions))
	for _, tx := range t.Transactions {
		txids = append(txids, tx.Hash)
		txData = append(txData, tx.Data)
	}
	merkle, err := merkleRootInternal(txids)
	if err != nil {
		return nil, fmt.Errorf("[%s] merkle: %w", c.name, err)
	}

	blob, err := buildHeaderBlob(t.Version, t.PreviousBlockHash, t.FinalSaplingRootHash,
		merkle, uint32(t.CurTime), t.Bits)
	if err != nil {
		return nil, fmt.Errorf("[%s] 组头: %w", c.name, err)
	}
	// 实例盐 → nonce 保留区 [116:120]（多实例 nonce 空间隔离；矿工原样回显）
	copy(blob[116:120], c.instanceSalt[:])

	seedInternalHex, err := c.seedForHeight(ctx, t.Height)
	if err != nil {
		return nil, fmt.Errorf("[%s] seed: %w", c.name, err)
	}

	target, ok := new(big.Int).SetString(strings.TrimPrefix(t.Target, "0x"), 16)
	if !ok || target.Sign() <= 0 {
		return nil, fmt.Errorf("[%s] GBT target 非法 %q", c.name, t.Target)
	}

	work := &adapter.BlobWork{
		HashingBlob: blob,
		NonceOffset: nonceOffset, NonceLen: 8, SearchLen: 4,
		WireNonceLen:    nonceFieldLen,
		PowIsBlockHash:  true,
		SeedHash:        seedInternalHex,
		Algo:            "rx/dragonx",
		NetworkTarget:   target,
		HashBigEndian:   false, // sha256d 值按 bitcoin 惯例小端解释
		TargetCompactLE: true,  // drg-xmrig/NTMminer 认 XMRig compact-LE
		HeightHint:      t.Height,
		// 同高度同前块 = 同一份可挖工作；curtime（每秒变）不算换 job → 消除 stale 洪峰。
		JobKey:    fmt.Sprintf("%d|%s", t.Height, t.PreviousBlockHash),
		SubmitRef: &drgSubmitRef{CoinbaseData: t.CoinbaseTxn.Data, TxData: txData},
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.PreviousBlockHash,
		NetworkTarget: t.Target,
		CoinbaseValue: satoshiToCoin(t.CoinbaseTxn.CoinbaseValue, c.decimals),
		MinTime:       t.MinTime,
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// seedForHeight epoch seed（缓存到 seedHeight 变化；display → 内部序反转，
// miningcore DragonXJobManager 同款取法）。
func (c *Client) seedForHeight(ctx context.Context, height uint64) (string, error) {
	sh := seedHeight(height, c.seedInterval, c.seedLag)
	c.mu.Lock()
	if c.seedValid && c.cachedSeedH == sh {
		s := c.cachedSeedHash
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()

	var display string
	if err := c.call(ctx, "getblockhash", []any{sh}, &display); err != nil {
		return "", err
	}
	internal, err := reverseHex32(display)
	if err != nil {
		return "", err
	}
	s := hex.EncodeToString(internal)
	c.mu.Lock()
	c.cachedSeedH, c.cachedSeedHash, c.seedValid = sh, s, true
	c.mu.Unlock()
	return s, nil
}

// SubmitBlob 组块 → submitblock → 返回权威块 hash（= reverse(pow)，PowIsBlockHash 链）。
func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	ref, ok := sol.Work.SubmitRef.(*drgSubmitRef)
	if !ok {
		return "", fmt.Errorf("[%s] SubmitRef 非 drgSubmitRef（适配器实现错误）", c.name)
	}
	block, err := serializeBlock(sol.Blob, sol.AuxHash, ref.CoinbaseData, ref.TxData)
	if err != nil {
		return "", fmt.Errorf("[%s] 组块: %w", c.name, err)
	}
	var result *string
	if err := c.call(ctx, "submitblock", []any{hex.EncodeToString(block)}, &result); err != nil {
		return "", err
	}
	if result != nil && *result != "" && !strings.HasPrefix(*result, "duplicate") {
		return "", fmt.Errorf("submitblock 被拒: %s", *result)
	}
	// 块 hash = sha256d(173B) 反转（金锚已证）；submitblock 接受即为权威 id。
	rev := make([]byte, len(sol.Hash))
	for i, v := range sol.Hash {
		rev[len(sol.Hash)-1-i] = v
	}
	return hex.EncodeToString(rev), nil
}

// SubmitBlock NodeAdapter 通用面（raw = 完整块 hex）。爆块正路走 SubmitBlob
// （cnjob 会带 rx_hash 组块）；本方法供通用调用方直提已组好的块。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	hexStr, ok := raw.(string)
	if !ok {
		return fmt.Errorf("[%s] SubmitBlock: 期望块 hex 字符串, got %T（爆块请走 SubmitBlob）", c.name, raw)
	}
	var result *string
	if err := c.call(ctx, "submitblock", []any{hexStr}, &result); err != nil {
		return err
	}
	if result != nil && *result != "" && !strings.HasPrefix(*result, "duplicate") {
		return fmt.Errorf("submitblock 被拒: %s", *result)
	}
	return nil
}

func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var hash string
	if err := c.call(ctx, "getblockhash", []any{height}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (c *Client) Confirmations(ctx context.Context, blockHash string, _ uint64) (int64, error) {
	var hdr struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.call(ctx, "getblockheader", []any{blockHash}, &hdr); err != nil {
		if code, ok := rpcCode(err); ok && code == rpcNotFound {
			return -1, nil
		}
		return 0, err
	}
	return hdr.Confirmations, nil
}

// ---- WalletAdapter（z_*，异步 opid 内吞）----

func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "z_getbalance", []any{c.vaultZ}, &raw); err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(raw)), nil
}

// SendMany 金库 zs → 矿工 zs 批量打款。z_sendmany 4 参（fromaddress/amounts/minconf/fee），
// 绝不带第 5 参 privacy policy（Hush 第 5 参 = donation，会广播 0 额 tx——miningcore
// EquihashPayoutHandler 实坑）。非 zs 地址整批拒绝（防 bad-txns-acprivate-chain 拖垮全批）。
func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	if len(outputs) == 0 {
		return "", fmt.Errorf("空输出")
	}
	// 单笔 z_sendmany 安全上限（miningcore 分页阈值 50 留余量）：超限 fail-fast
	// （发生在 RPC 之前 = 肯定未广播，人工把关零歧义）。分页支持列 M5.x 硬化。
	if len(outputs) > 45 {
		return "", fmt.Errorf("[%s] 单批 %d 个收款人超 z_sendmany 安全上限 45——提高起付额或等分页支持", c.name, len(outputs))
	}
	amounts := make([]map[string]json.RawMessage, 0, len(outputs))
	for addr, amt := range outputs {
		if !strings.HasPrefix(addr, "zs1") {
			return "", fmt.Errorf("[%s] 打款地址 %s 非 zs（ac_private 链只能 z→z），整批拒绝", c.name, addr)
		}
		if !json.Valid([]byte(amt)) {
			return "", fmt.Errorf("非法金额 %q (地址 %s)", amt, addr)
		}
		amounts = append(amounts, map[string]json.RawMessage{
			"address": json.RawMessage(fmt.Sprintf("%q", addr)),
			"amount":  json.RawMessage(amt),
		})
	}
	var opid string
	err := c.call(ctx, "z_sendmany",
		[]any{c.vaultZ, amounts, zMinConf, json.RawMessage(zSendFee)}, &opid)
	if err != nil {
		return "", fmt.Errorf("z_sendmany: %w", err)
	}
	txid, err := c.waitOperation(ctx, opid)
	if err != nil {
		return "", fmt.Errorf("z_sendmany opid=%s: %w", opid, err)
	}
	return txid, nil
}

func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	var tx struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := c.call(ctx, "gettransaction", []any{txid}, &tx); err != nil {
		return 0, err
	}
	return tx.Confirmations, nil
}

// waitOperation 轮询【自己发起的】opid 直到 success/failed（opid-safe：
// 只 status 只读探测 + 只对自己的 opid 调 z_getoperationresult 回收，
// 绝不 drain-all——不抢主机 consolidate 服务与其他调用方的结果）。
func (c *Client) waitOperation(ctx context.Context, opid string) (string, error) {
	deadline := time.Now().Add(opTimeout)
	wctx := ctx
	if d, ok := ctx.Deadline(); !ok || d.After(deadline) {
		var cancel context.CancelFunc
		wctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	ids := []any{[]string{opid}}
	for {
		var ops []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Result *struct {
				TxID string `json:"txid"`
			} `json:"result"`
		}
		if err := c.call(wctx, "z_getoperationstatus", ids, &ops); err != nil {
			return "", err
		}
		for _, op := range ops {
			if op.ID != opid {
				continue
			}
			switch op.Status {
			case "success":
				// 回收（从节点内存队列删除）；txid 以 result 为准
				var res []struct {
					Result *struct {
						TxID string `json:"txid"`
					} `json:"result"`
				}
				_ = c.call(wctx, "z_getoperationresult", ids, &res)
				if op.Result != nil && op.Result.TxID != "" {
					return op.Result.TxID, nil
				}
				if len(res) > 0 && res[0].Result != nil && res[0].Result.TxID != "" {
					return res[0].Result.TxID, nil
				}
				return "", fmt.Errorf("操作 success 但无 txid（异常，人工核对）")
			case "failed":
				msg := "unknown"
				if op.Error != nil {
					msg = op.Error.Message
				}
				_ = c.call(wctx, "z_getoperationresult", ids, nil) // 回收失败记录
				return "", fmt.Errorf("操作失败（未广播）: %s", msg)
			}
		}
		select {
		case <-wctx.Done():
			return "", fmt.Errorf("等待 opid 超时（状态未知，人工核对）: %w", wctx.Err())
		case <-time.After(opPollInterval):
		}
	}
}

// ---- WalletMaintainer（shield 回补金库）----

// NeedsMaintenance 有成熟（≥100 确认）coinbase UTXO 即需要 shield。
func (c *Client) NeedsMaintenance(ctx context.Context) (bool, string, error) {
	var utxos []struct {
		Generated bool `json:"generated"`
	}
	if err := c.call(ctx, "listunspent", []any{100, 99999999}, &utxos); err != nil {
		return false, "", err
	}
	n := 0
	for _, u := range utxos {
		if u.Generated {
			n++
		}
	}
	if n == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%d 个成熟 coinbase UTXO 待 shield 回金库", n), nil
}

// Maintain z_shieldcoinbase 全部 R coinbase → 金库 zs（单次 ≤ 节点默认 50 个 UTXO，
// 剩余下轮再收）。无可 shield 时安静返回。
func (c *Client) Maintain(ctx context.Context) ([]string, error) {
	var res struct {
		OpID string `json:"opid"`
	}
	err := c.call(ctx, "z_shieldcoinbase", []any{"*", c.vaultZ}, &res)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "could not find any coinbase") {
			return nil, nil // 与 NeedsMaintenance 之间的竞态：已被收走，无事
		}
		return nil, fmt.Errorf("z_shieldcoinbase: %w", err)
	}
	txid, err := c.waitOperation(ctx, res.OpID)
	if err != nil {
		return nil, fmt.Errorf("z_shieldcoinbase opid=%s: %w", res.OpID, err)
	}
	return []string{txid}, nil
}
