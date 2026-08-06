// Package junorpc Juno Cash（Zcash 6.x 系 RandomX 隐私链）节点+钱包适配器。
//
// 形态与 dragonxrpc 同构（三家族杂交），差异全收在本包：
//
//   - GetTemplate：GBT → 140B 头 blob。头字段直接用 GBT defaultroots（merkleroot +
//     blockcommitmentshash，display 序反转），池不自算 merkle；seed 用 GBT 权威值
//     randomxseedhash【内部序原样直用，不反转不自算】（epoch 2048/lag 96 由节点管，
//     brisvia 血泪：节点才是 seed 的 authority）。
//     部署铁律：junocashd 必须配 -mineraddress=<透明 t1 地址>，否则 GBT 无 coinbasetxn；
//     必须是透明地址（Orchard UA coinbase 无 vout，池取不到块奖励金额）。
//   - 单段 PoW：pow = RandomX(140B) = 链上块 hash（GetHash() 返回 nSolution），
//     无 dragonx 的外层 sha256d 双段。
//   - SubmitBlob：blob ‖ 0x20 ‖ rx_hash ‖ txs → submitblock；块 hash = reverse(pow)。
//   - 钱包：金库 = j1 统一地址（Orchard）。SendMany 内部 z_sendmany（4 参，fee 传
//     null = ZIP-317 自动费；juno 无第 5 参 privacy policy）→ opid → 轮询 → txid。
//     只付 j 前缀统一地址。
//   - WalletMaintainer：z_shieldcoinbase t1→金库 j1（fCoinbaseMustBeShielded=true：
//     coinbase 必须先进 Orchard 才能花，与 dragonx 的 shield 回补语义一致）。
//
// 规则同 bitcoinrpc：无状态可重连、timeout ≥ 120s、金额十进制字符串不过浮点。
package junorpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

const (
	// zMinConf z_sendmany 花费输入的最小确认数。
	zMinConf = 1
	// opTimeout 单个 z_* 异步操作（含 Orchard 证明生成）的等待上限。
	opTimeout = 15 * time.Minute
	// opPollInterval opid 轮询间隔。
	opPollInterval = 2 * time.Second
)

// Client 一个 junocashd 节点的适配器。节点即钱包（zcashd 内置钱包）。
type Client struct {
	name     string
	url      string
	user     string
	pass     string
	decimals int
	vaultUA  string // 金库 j1 统一地址（Orchard）：打款出账源 + shield 目标
	hc       *http.Client

	// instanceSalt 写进 nonce 保留区 [116:120]：多实例共享 Postgres 时的
	// nonce 空间隔离（dragonx 同款）。每进程随机一次。
	instanceSalt [4]byte

	mu             sync.Mutex
	lastLongPollID string // GBT longpollid（挂等通知用，GetTemplate/waitTip 都会刷新）
	vaultAcct      int    // 金库 UA 的账户号缓存（acctUnknown=未解析）
	// cbCacheKey/cbCacheZats 最近一次 coinbase 金额解析缓存（同一模板 curtime 刷新
	// 时 coinbasetxn.data 不变，免得每次 GBT 都多一发 decoderawtransaction）。
	cbCacheKey  string
	cbCacheZats int64
	// bcast 爆块并发广播的伙伴节点（dragonx 方案⑤同款）。启动接线时写一次。
	bcast []*Client
}

var (
	_ adapter.NodeAdapter      = (*Client)(nil)
	_ adapter.BlobSubmitter    = (*Client)(nil)
	_ adapter.WalletAdapter    = (*Client)(nil)
	_ adapter.WalletMaintainer = (*Client)(nil)
	_ adapter.HashPSSource     = (*Client)(nil)
)

func New(name, url, user, pass, vaultUA string) *Client {
	c := &Client{
		name: name, url: url, user: user, pass: pass,
		decimals: 8, vaultUA: vaultUA, vaultAcct: acctUnknown,
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

// parseAmountZats "6.25" / "6.25000000" → 整数 zat（十进制字符串直解，不过浮点）。
func parseAmountZats(s string, decimals int) (int64, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	if neg {
		return 0, fmt.Errorf("负金额 %q", s)
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(fracPart) > decimals {
		return 0, fmt.Errorf("金额 %q 小数位超 %d", s, decimals)
	}
	for len(fracPart) < decimals {
		fracPart += "0"
	}
	var total int64
	for _, part := range []string{intPart, fracPart} {
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("非法金额 %q", s)
			}
			total = total*10 + int64(ch-'0')
		}
	}
	return total, nil
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

// NetworkHashPS 全网算力真值（juno 保留 getnetworkhashps = networksolps 别名；
// 铁律 C7 绝不反推）。
func (c *Client) NetworkHashPS(ctx context.Context) (float64, error) {
	var hps float64
	if err := c.call(ctx, "getnetworkhashps", []any{120, -1}, &hps); err != nil {
		return 0, err
	}
	return hps, nil
}

// junoSubmitRef GBT 交易原文（组块提交用），挂在 BlobWork.SubmitRef。
type junoSubmitRef struct {
	CoinbaseData string
	TxData       []string
}

func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	var t struct {
		Version           uint32 `json:"version"`
		PreviousBlockHash string `json:"previousblockhash"`
		DefaultRoots      *struct {
			MerkleRoot           string `json:"merkleroot"`
			BlockCommitmentsHash string `json:"blockcommitmentshash"`
		} `json:"defaultroots"`
		Target           string `json:"target"`
		Bits             string `json:"bits"`
		CurTime          int64  `json:"curtime"`
		MinTime          int64  `json:"mintime"`
		Height           uint64 `json:"height"`
		LongPollID       string `json:"longpollid"`
		RandomXSeedHash  string `json:"randomxseedhash"`
		CoinbaseTxn      *struct {
			Data string `json:"data"`
			Hash string `json:"hash"`
		} `json:"coinbasetxn"`
		Transactions []struct {
			Data string `json:"data"`
			Hash string `json:"hash"`
		} `json:"transactions"`
	}
	if err := c.call(ctx, "getblocktemplate",
		[]any{map[string]any{"capabilities": []string{"coinbasetxn", "longpoll", "workid"}}}, &t); err != nil {
		return nil, err
	}
	if t.LongPollID != "" {
		c.mu.Lock()
		c.lastLongPollID = t.LongPollID
		c.mu.Unlock()
	}
	if t.CoinbaseTxn == nil || t.CoinbaseTxn.Data == "" {
		return nil, fmt.Errorf("[%s] GBT 无 coinbasetxn——junocashd 必须配 -mineraddress=<池的透明 t1 地址>", c.name)
	}
	if t.DefaultRoots == nil || t.DefaultRoots.MerkleRoot == "" || t.DefaultRoots.BlockCommitmentsHash == "" {
		return nil, fmt.Errorf("[%s] GBT 无 defaultroots（merkleroot/blockcommitmentshash）", c.name)
	}
	if err := validSeedHex(t.RandomXSeedHash); err != nil {
		return nil, fmt.Errorf("[%s] GBT seed: %w", c.name, err)
	}

	txData := make([]string, 0, len(t.Transactions))
	for _, tx := range t.Transactions {
		txData = append(txData, tx.Data)
	}

	blob, err := buildHeaderBlob(t.Version, t.PreviousBlockHash,
		t.DefaultRoots.MerkleRoot, t.DefaultRoots.BlockCommitmentsHash,
		uint32(t.CurTime), t.Bits)
	if err != nil {
		return nil, fmt.Errorf("[%s] 组头: %w", c.name, err)
	}
	// 实例盐 → nonce 保留区 [116:120]（多实例 nonce 空间隔离；junorig 只滚
	// [108:112]，其余 28 字节原样回显，PROTOCOL_STRATUM.md 已证）
	copy(blob[116:120], c.instanceSalt[:])

	cbZats, err := c.coinbaseValueZats(ctx, t.CoinbaseTxn.Data)
	if err != nil {
		return nil, fmt.Errorf("[%s] coinbase 金额: %w", c.name, err)
	}

	target, ok := new(big.Int).SetString(strings.TrimPrefix(t.Target, "0x"), 16)
	if !ok || target.Sign() <= 0 {
		return nil, fmt.Errorf("[%s] GBT target 非法 %q", c.name, t.Target)
	}

	work := &adapter.BlobWork{
		HashingBlob: blob,
		NonceOffset: nonceOffset, NonceLen: 8, SearchLen: 4,
		WireNonceLen:    nonceFieldLen,
		PowIsBlockHash:  true, // 单段：pow = RandomX hash = 链上块 hash（GetHash=nSolution）
		SeedHash:        t.RandomXSeedHash,
		Algo:            "rx/juno",
		NetworkTarget:   target,
		HashBigEndian:   false, // rx_hash 按 LE 解释（junorig 比 target 同口径）
		TargetCompactLE: true,  // junorig = xmrig，认 XMRig compact-LE target
		HeightHint:      t.Height,
		// 同高度同前块 = 同一份可挖工作；curtime（每秒变）不算换 job → 消除 stale 洪峰。
		JobKey:    fmt.Sprintf("%d|%s", t.Height, t.PreviousBlockHash),
		SubmitRef: &junoSubmitRef{CoinbaseData: t.CoinbaseTxn.Data, TxData: txData},
	}
	return &adapter.BlockTemplate{
		Height:        t.Height,
		PrevHash:      t.PreviousBlockHash,
		NetworkTarget: t.Target,
		CoinbaseValue: satoshiToCoin(cbZats, c.decimals),
		MinTime:       t.MinTime,
		Raw:           work,
		FetchedAt:     time.Now(),
	}, nil
}

// coinbaseValueZats 解 coinbase 交易的透明输出总额（= 块奖励 subsidy+fees）。
// juno GBT 不给 coinbasevalue（节点源码 TODO 禁用）→ 让节点 decoderawtransaction
// 解给我们，vout 十进制字符串求和，不过浮点。
// vout 总额为 0 = coinbase 无透明输出（-mineraddress 配了 Orchard UA）→ 硬错，
// 池的记账/打款都依赖这个金额。
func (c *Client) coinbaseValueZats(ctx context.Context, cbHex string) (int64, error) {
	c.mu.Lock()
	if c.cbCacheKey == cbHex {
		v := c.cbCacheZats
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	var tx struct {
		Vout []struct {
			Value json.Number `json:"value"`
		} `json:"vout"`
	}
	if err := c.call(ctx, "decoderawtransaction", []any{cbHex}, &tx); err != nil {
		return 0, err
	}
	var total int64
	for i, o := range tx.Vout {
		z, err := parseAmountZats(o.Value.String(), c.decimals)
		if err != nil {
			return 0, fmt.Errorf("vout[%d]: %w", i, err)
		}
		total += z
	}
	if total <= 0 {
		return 0, fmt.Errorf("coinbase 无透明输出（-mineraddress 必须是透明 t1 地址，不能是 Orchard UA）")
	}
	c.mu.Lock()
	c.cbCacheKey, c.cbCacheZats = cbHex, total
	c.mu.Unlock()
	return total, nil
}

// SetBroadcastPeers 设置「爆块并发广播」的伙伴节点（dragonx 方案⑤同款）。
// 伙伴节点只收块：不供模板、不出账、不碰钱包；本次爆块的结论完全由 nodes[0]
// 的 submitblock 决定。只在启动接线时调用一次（buildJunoFamily）。
func (c *Client) SetBroadcastPeers(peers []*Client) {
	c.mu.Lock()
	c.bcast = append([]*Client(nil), peers...)
	c.mu.Unlock()
}

// fanoutBlock 把已组好的块并发推给广播伙伴，不阻塞主提交路径。
// ⚠ 用独立 ctx 而不是调用方的：主提交一返回，调用方的 ctx 就可能被取消。
func (c *Client) fanoutBlock(blockHex string, height uint64) {
	c.mu.Lock()
	peers := c.bcast
	c.mu.Unlock()
	for i, p := range peers {
		i, p := i+1, p
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			t0 := time.Now()
			var result *string
			err := p.call(ctx, "submitblock", []any{blockHex}, &result)
			ms := time.Since(t0).Milliseconds()
			switch {
			case err != nil:
				log.Printf("[%s] 爆块广播#%d height=%d 失败(%dms): %v", c.name, i, height, ms, err)
			case result == nil || *result == "" || strings.HasPrefix(*result, "duplicate"):
				log.Printf("[%s] 爆块广播#%d height=%d ok(%dms)", c.name, i, height, ms)
			case strings.HasPrefix(*result, "inconclusive"):
				// inconclusive ≠ 拒绝（PoW 无效会返 high-hash，brisvia 血泪）；
				// 伙伴节点链头与我们不同步时很常见，不当异常报。
				log.Printf("[%s] 爆块广播#%d height=%d inconclusive(%dms)", c.name, i, height, ms)
			default:
				log.Printf("[%s] 爆块广播#%d height=%d 被拒(%dms): %s", c.name, i, height, ms, *result)
			}
		}()
	}
}

// SubmitBlob 组块 → submitblock → 返回权威块 hash（= reverse(pow)，PowIsBlockHash 链）。
// 单段链：rx_hash = sol.Hash（AuxHash 只在双段算法有值）。
func (c *Client) SubmitBlob(ctx context.Context, sol *adapter.BlobSolution) (string, error) {
	ref, ok := sol.Work.SubmitRef.(*junoSubmitRef)
	if !ok {
		return "", fmt.Errorf("[%s] SubmitRef 非 junoSubmitRef（适配器实现错误）", c.name)
	}
	block, err := serializeBlock(sol.Blob, sol.Hash, ref.CoinbaseData, ref.TxData)
	if err != nil {
		return "", fmt.Errorf("[%s] 组块: %w", c.name, err)
	}
	blockHex := hex.EncodeToString(block)
	// 先撒出去再自己提交：goroutine 起完立刻返回，两边实际是并发的。
	c.fanoutBlock(blockHex, sol.Work.HeightHint)
	var result *string
	if err := c.call(ctx, "submitblock", []any{blockHex}, &result); err != nil {
		return "", err
	}
	if result != nil && *result != "" && !strings.HasPrefix(*result, "duplicate") {
		return "", fmt.Errorf("submitblock 被拒: %s", *result)
	}
	// 块 hash = rx_hash 反转成 display 序；submitblock 接受即为权威 id。
	rev := make([]byte, len(sol.Hash))
	for i, v := range sol.Hash {
		rev[len(sol.Hash)-1-i] = v
	}
	return hex.EncodeToString(rev), nil
}

// SubmitBlock NodeAdapter 通用面（raw = 完整块 hex）。爆块正路走 SubmitBlob。
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

// SpendableBalance 金库（统一账户）可花余额。z_getbalance 对统一地址在 zcash 6.x
// 已不适用 → 走账户口径：z_listaccounts 找到金库 UA 所在账户，z_getbalanceforaccount
// 取 orchard 池 valueZat（juno 是 Orchard-only 链，sapling 恒 0）。
func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	acct, err := c.vaultAccount(ctx)
	if err != nil {
		return "", err
	}
	var res struct {
		Pools map[string]struct {
			ValueZat int64 `json:"valueZat"`
		} `json:"pools"`
	}
	if err := c.call(ctx, "z_getbalanceforaccount", []any{acct, zMinConf}, &res); err != nil {
		return "", err
	}
	var total int64
	for _, p := range res.Pools {
		total += p.ValueZat
	}
	return satoshiToCoin(total, c.decimals), nil
}

// vaultAccount 金库 UA → 账户号（z_listaccounts 匹配；结果缓存，账户不会变）。
const acctUnknown = -1

func (c *Client) vaultAccount(ctx context.Context) (int, error) {
	c.mu.Lock()
	if c.vaultAcct != acctUnknown {
		a := c.vaultAcct
		c.mu.Unlock()
		return a, nil
	}
	c.mu.Unlock()
	var accounts []struct {
		Account   int `json:"account"`
		Addresses []struct {
			UA string `json:"ua"`
		} `json:"addresses"`
	}
	if err := c.call(ctx, "z_listaccounts", []any{}, &accounts); err != nil {
		return 0, fmt.Errorf("z_listaccounts: %w", err)
	}
	for _, a := range accounts {
		for _, ad := range a.Addresses {
			if ad.UA == c.vaultUA {
				c.mu.Lock()
				c.vaultAcct = a.Account
				c.mu.Unlock()
				return a.Account, nil
			}
		}
	}
	return 0, fmt.Errorf("金库地址 %s 不在本节点钱包任何统一账户里（z_getnewaccount + z_getaddressforaccount 生成后写进配置）", c.vaultUA)
}

// SendMany 金库 j1 → 矿工 j1 批量打款（Orchard→Orchard 全隐蔽）。
// z_sendmany 4 参（fromaddress/amounts/minconf/fee）；fee 传 null = ZIP-317 自动
// 费率（juno 是 zcash 6.x，写死 0.0001 会低于 conventional fee 被拒）。
// 非 j 前缀统一地址整批拒绝（fail-fast 发生在 RPC 前 = 肯定未广播）。
func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	if len(outputs) == 0 {
		return "", fmt.Errorf("空输出")
	}
	// 单笔 z_sendmany 安全上限（dragonx 45 同款；Orchard action 数受 tx 大小约束）：
	// 超限 fail-fast，提高起付额或等分页支持。
	if len(outputs) > 45 {
		return "", fmt.Errorf("[%s] 单批 %d 个收款人超 z_sendmany 安全上限 45——提高起付额或等分页支持", c.name, len(outputs))
	}
	amounts := make([]map[string]json.RawMessage, 0, len(outputs))
	for addr, amt := range outputs {
		if !isUnifiedAddr(addr) {
			return "", fmt.Errorf("[%s] 打款地址 %s 非 juno 统一地址（j1/jtest1/jregtest1），整批拒绝", c.name, addr)
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
		[]any{c.vaultUA, amounts, zMinConf, nil}, &opid)
	if err != nil {
		return "", fmt.Errorf("z_sendmany: %w", err)
	}
	txid, err := c.waitOperation(ctx, opid)
	if err != nil {
		return "", fmt.Errorf("z_sendmany opid=%s: %w", opid, err)
	}
	return txid, nil
}

// isUnifiedAddr j 前缀统一地址粗检（bech32m 细校验交给节点，节点拒绝时
// waitOperation 会把 failed 包成 ErrNotBroadcast 安全退回）。
func isUnifiedAddr(addr string) bool {
	for _, p := range unifiedAddrPrefixes {
		if strings.HasPrefix(addr, p) && len(addr) > len(p)+20 {
			return true
		}
	}
	return false
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
// 只 status 只读探测 + 只对自己的 opid 调 z_getoperationresult 回收，绝不 drain-all）。
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
				// status=failed = 广播前失败（余额不足/地址非法/费率不足等），交易
				// 确定未离开节点 → 包 ErrNotBroadcast，打款引擎安全退回矿工余额、
				// 下轮重付（dragonx 18 笔漏付血泪的修复语义，一字不动）。
				return "", fmt.Errorf("操作失败（未广播）: %s: %w", msg, adapter.ErrNotBroadcast)
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

// NeedsMaintenance 有成熟（≥100 确认）coinbase UTXO 即需要 shield
// （fCoinbaseMustBeShielded：coinbase 不 shield 就永远花不掉）。
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

// Maintain z_shieldcoinbase 全部 t1 coinbase → 金库 j1（单次 ≤ 节点默认 50 个 UTXO，
// 剩余下轮再收）。fee 传 null = ZIP-317 自动。无可 shield 时安静返回。
func (c *Client) Maintain(ctx context.Context) ([]string, error) {
	var res struct {
		OpID string `json:"opid"`
	}
	err := c.call(ctx, "z_shieldcoinbase", []any{"*", c.vaultUA, nil}, &res)
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
