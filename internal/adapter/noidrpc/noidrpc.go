// Package noidrpc 对接 ParanO(1)d (NOID) 官方节点的 HTTP JSON-RPC 2.0（jsonrpsee）。
//
// 事实源：coins/parano1d/REPORT-节点协议与单机测试网.md（agent 实读上游 parano1d-src）。
// 协议要点（照做别推导）：
//   - 方法名 paranoid_*；节点带 --mining-key 时【所有】RPC 要 Authorization: Bearer <token>。
//   - 无任何推送（无 ZMQ/WS/SSE）→ 只能轮询 getChainInfo（tipwatch.go 做 150ms 轮询）。
//   - ★单飞行槽模板（server.rs:499-679）：节点一次只有一个模板。状态非 Idle 时再调
//     getBlockTemplate 直接报错 "external mining attempt is already active"。模板制备
//     （HistoryStep 递归证明）耗 7-35s。失效触发：tip 变 / 30s TTL / submitBlock 消费。
//     ⇒ 本包在客户端侧维护【单槽缓存】：GetTemplate 若缓存未过期且 tip 未变即原样返回
//     （绝不重打节点，避免 "already active"）；否则串行拉一次并处理三种瞬时错误快速重试。
//     submitBlock 成功后 noidjob 调 ConsumeTemplate 作废缓存 → 下一次 GetTemplate 取新模板。
//
// 规则同 bitcoinrpc/junorpc：无状态可重连、金额/hash 不过浮点。
package noidrpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter"
)

const (
	// nonceFieldIndex 官方共识 POW_NONCE_FIELD_INDEX（noidp2b 硬编码 10）：
	// 模板必须声明 10，否则池端重算（覆写 field 10）与节点不一致 = 挖废块，直接拒。
	nonceFieldIndex = 10
	// powFieldsBytes pow_fields 解码后字节数（16 字段 × 16B LE）。
	powFieldsBytes = 256
	// targetBytes difficulty_target 字节数（256-bit LE）。
	targetBytes = 32
	// templateSafetyMargin 缓存提前失效余量：节点 30s TTL 到点前就重取，
	// 避免把刚过期的模板发给矿工（矿工提交必被 submitBlock 判 stale）。
	templateSafetyMargin = 2 * time.Second
	// fetchMaxAttempts 拉模板瞬时错误快速重试上限（制备 7-35s，250ms/次 → 覆盖）。
	fetchMaxAttempts = 200
	// fetchRetryDelay 三种瞬时错误的重试间隔（"already active" 等，200-500ms 快速重试）。
	fetchRetryDelay = 300 * time.Millisecond
)

// NoidTemplate 是 NOID 强类型单槽模板（noidjob 消费）。挂在 adapter.BlockTemplate.Raw。
type NoidTemplate struct {
	TemplateID      string
	PowFields       [256]byte // pow_fields_hex 解码（16×16B LE）；field 10 是 nonce 占位
	NonceFieldIndex int       // 恒 10
	NetworkTarget   [32]byte  // difficulty_target_hex 解码（256-bit LE，le256_lt 直用）
	Height          uint64
	ExpiresAt       time.Time // 本地记的失效时刻（fetch 时 + expires_in_seconds）
	NTxs            int
}

// Client 一个 NOID 节点的适配器。
type Client struct {
	name     string
	url      string
	token    string // Bearer（= 节点 --mining-key）；空 = 不带 Authorization
	coinbase string // getBlockTemplate 的 coinbase 参数；"" = 节点自身钱包地址（池金库）

	hc    *http.Client // 主 client：轻 RPC（chainInfo/getBlockHash/submitBlock）
	tplHC *http.Client // 拉模板专用：制备 7-35s，长超时，与 hc 分开

	// fetchMu 串行化【打节点拉模板】——单飞行槽下并发 getBlockTemplate 第二发必得
	// "already active"。Refresh 会被 2s 轮询循环 + tip 通知循环并发调用，必须串行。
	fetchMu sync.Mutex

	mu         sync.Mutex
	cur        *NoidTemplate // 单槽缓存（nil = 无）
	curTipH    uint64        // 缓存对应的 tip 高度（tip 变即失效）
	curTipHash string        // 缓存对应的 tip hash
}

var (
	_ adapter.NodeAdapter = (*Client)(nil)
	_ adapter.Notifier    = (*TipWatcher)(nil)
)

// New 建一个 NOID 节点客户端。token = mining-key（Bearer），支持空。
func New(name, url, token string) *Client {
	return &Client{
		name: name, url: strings.TrimRight(url, "/"), token: token,
		hc:    &http.Client{Timeout: 150 * time.Second},
		tplHC: &http.Client{Timeout: 90 * time.Second}, // 覆盖 B255=21-35s 制备
	}
}

func (c *Client) Name() string { return c.name }

// SetCoinbase 设 getBlockTemplate 的 coinbase 参数（付款地址）。默认 ""（节点自身钱包
// = 池金库）。设为池自有 o1 地址时要求节点带 --allow-custom-coinbase。
func (c *Client) SetCoinbase(addr string) { c.coinbase = addr }

// ---- JSON-RPC 2.0 底座（jsonrpsee；Bearer 全局中间件）----

// RPCError JSON-RPC 2.0 error 对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (c *Client) call(ctx context.Context, hc *http.Client, method string, params any, out any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.name, method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s %s: 401 未授权——节点带 --mining-key 时 RPC 必须带 Authorization: Bearer <token>（配 nodes[0].pass）", c.name, method)
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

// ---- chain info ----

// chainInfo paranoid_getChainInfo（判 reorg / tip 变化的权威口径）。
type chainInfo struct {
	Height            uint64 `json:"height"`
	BestHash          string `json:"best_hash"`
	DifficultyTarget  string `json:"difficulty_target"`
	ActiveSlotCount   uint64 `json:"active_slot_count"`
	LogSlots          uint64 `json:"log_slots"`
	CirculatingSupply string `json:"circulating_supply"`
}

func (c *Client) getChainInfo(ctx context.Context) (chainInfo, error) {
	var ci chainInfo
	err := c.call(ctx, c.hc, "paranoid_getChainInfo", []any{}, &ci)
	return ci, err
}

// Status 节点健康面（coininstance 失联检测 + 高度轮询兜底）。
func (c *Client) Status(ctx context.Context) (adapter.ChainStatus, error) {
	ci, err := c.getChainInfo(ctx)
	if err != nil {
		return adapter.ChainStatus{}, err
	}
	return adapter.ChainStatus{
		Height:    ci.Height,
		TipHash:   ci.BestHash,
		Synced:    true, // NOID getChainInfo 无 is_syncing 字段；运营节点应已同步
		Connected: true,
	}, nil
}

// ---- 模板（单飞行槽 + 客户端侧单槽缓存）----

// GetTemplate 返回当前模板（归一化壳 + Raw=*NoidTemplate）。
//
// 单槽状态机：缓存未过期且 tip 未变 → 原样返回缓存（不打节点）；否则串行拉一次。
// 拉取用独立长超时 client，并处理三种瞬时错误快速重试。
func (c *Client) GetTemplate(ctx context.Context) (*adapter.BlockTemplate, error) {
	ci, err := c.getChainInfo(ctx)
	if err != nil {
		return nil, err
	}
	if bt := c.cachedIfFresh(ci); bt != nil {
		return bt, nil
	}

	// 串行化打节点（避免并发 getBlockTemplate 撞 "already active"）。
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	// 双检：可能有并发调用刚刚刷好了缓存。
	if bt := c.cachedIfFresh(ci); bt != nil {
		return bt, nil
	}

	tpl, err := c.fetchTemplate(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cur = tpl
	c.curTipH = ci.Height
	c.curTipHash = ci.BestHash
	c.mu.Unlock()
	return blockTemplateFrom(tpl), nil
}

// cachedIfFresh 缓存命中判定：非 nil、未过期、tip 未变。命中返回归一化壳，否则 nil。
func (c *Client) cachedIfFresh(ci chainInfo) *adapter.BlockTemplate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		return nil
	}
	if time.Now().After(c.cur.ExpiresAt.Add(-templateSafetyMargin)) {
		return nil // TTL 到点（含安全余量）
	}
	if ci.Height != c.curTipH || !strings.EqualFold(ci.BestHash, c.curTipHash) {
		return nil // tip 变 → 节点已 invalidate_for_tip，缓存失效
	}
	return blockTemplateFrom(c.cur)
}

// fetchTemplate 打节点拉一次模板 + 三种瞬时错误快速重试。调用方须持 fetchMu。
func (c *Client) fetchTemplate(ctx context.Context) (*NoidTemplate, error) {
	for attempt := 0; ; attempt++ {
		tpl, err := c.fetchTemplateOnce(ctx)
		if err == nil {
			return tpl, nil
		}
		if !isTransientTemplateErr(err) || attempt >= fetchMaxAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fetchRetryDelay):
		}
	}
}

// fetchTemplateOnce paranoid_getBlockTemplate（不传 coinbase → 节点用自身 mining-key
// 钱包地址付款，miner_address 进 pow_fields[8..9]；除非 --allow-custom-coinbase）。
func (c *Client) fetchTemplateOnce(ctx context.Context) (*NoidTemplate, error) {
	var r struct {
		TemplateID          string `json:"template_id"`
		PowFieldsHex        string `json:"pow_fields_hex"`
		NonceFieldIndex     int    `json:"nonce_field_index"`
		DifficultyTargetHex string `json:"difficulty_target_hex"`
		Height              uint64 `json:"height"`
		ExpiresInSeconds    int64  `json:"expires_in_seconds"`
		NTxs                int    `json:"n_txs"`
	}
	// ★paranoid_getBlockTemplate 必须传一个 coinbase 字符串参数（官方 extminer
	// main.rs:184-185 `self.call("paranoid_getBlockTemplate", [coinbase])`）；传 []
	// 会被节点 jsonrpsee 判 "Invalid params: No more params"（实测）。空串 "" = 节点用
	// 自身 mining-key 钱包地址付款（= 池金库，miner_address 进 pow_fields[8..9]）。
	// 需付到别的地址时改传该地址（要求节点 --allow-custom-coinbase）。
	if err := c.call(ctx, c.tplHC, "paranoid_getBlockTemplate", []any{c.coinbase}, &r); err != nil {
		return nil, err
	}
	if r.TemplateID == "" {
		return nil, fmt.Errorf("[%s] getBlockTemplate 无 template_id", c.name)
	}
	fields, err := hex.DecodeString(strings.TrimSpace(r.PowFieldsHex))
	if err != nil || len(fields) != powFieldsBytes {
		return nil, fmt.Errorf("[%s] pow_fields_hex 非 %dB（512 hex），得 %d hex: %v", c.name, powFieldsBytes, len(r.PowFieldsHex), err)
	}
	target, err := hex.DecodeString(strings.TrimSpace(r.DifficultyTargetHex))
	if err != nil || len(target) != targetBytes {
		return nil, fmt.Errorf("[%s] difficulty_target_hex 非 %dB（64 hex），得 %d hex: %v", c.name, targetBytes, len(r.DifficultyTargetHex), err)
	}
	if r.NonceFieldIndex != nonceFieldIndex {
		// 池端重算（noidp2b）固定覆写 field 10；节点若换位置 = 逐字节失配 = 挖废块。
		return nil, fmt.Errorf("[%s] nonce_field_index=%d ≠ %d（共识固定 10，拒绝——避免挖废块）", c.name, r.NonceFieldIndex, nonceFieldIndex)
	}
	exp := r.ExpiresInSeconds
	if exp <= 0 {
		exp = 30 // 协议硬编码 EXTERNAL_MINING_TEMPLATE_TTL=30
	}
	t := &NoidTemplate{
		TemplateID:      r.TemplateID,
		NonceFieldIndex: r.NonceFieldIndex,
		Height:          r.Height,
		ExpiresAt:       time.Now().Add(time.Duration(exp) * time.Second),
		NTxs:            r.NTxs,
	}
	copy(t.PowFields[:], fields)
	copy(t.NetworkTarget[:], target)
	return t, nil
}

// isTransientTemplateErr 三种要快速重试的拉模板错误（REPORT-节点协议 §2）：
// "already active" / "preparation was invalidated" / "chain tip changed while preparing"。
func isTransientTemplateErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already active") ||
		strings.Contains(msg, "invalidated") ||
		strings.Contains(msg, "chain tip changed")
}

// blockTemplateFrom 把强类型模板包成 adapter.BlockTemplate（Height/NetworkTarget 归一化；
// Raw = *NoidTemplate 供 noidjob 消费）。NOID 奖励不在模板里（节点钱包付），Reward 由
// noidjob 置占位（payout 关；接钱包适配器后补真值）。
func blockTemplateFrom(t *NoidTemplate) *adapter.BlockTemplate {
	return &adapter.BlockTemplate{
		Height:        t.Height,
		NetworkTarget: hex.EncodeToString(t.NetworkTarget[:]), // 归一化展示（LE 原字节）
		Raw:           t,
		FetchedAt:     time.Now(),
	}
}

// ConsumeTemplate submitBlock 成功后作废本地缓存（节点侧该模板槽已被消费）。
// 只作废 id 匹配的那份（防误清并发刚换上的新模板）。
func (c *Client) ConsumeTemplate(templateID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.cur.TemplateID == templateID {
		c.cur = nil
	}
}

// ---- 提交 ----

// SubmitNonce paranoid_submitBlock(template_id, nonce_hex)。nonce_hex = 16B LE 小写 hex。
// 成功返回节点权威块 hash 串；错误串含 "stale" = 模板过期（noidjob 转 stale，不重交）。
func (c *Client) SubmitNonce(ctx context.Context, templateID, nonceHex string) (string, error) {
	var hash string
	if err := c.call(ctx, c.hc, "paranoid_submitBlock", []any{templateID, nonceHex}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// SubmitBlock 通用 NodeAdapter 面（多节点并发提交用）。raw = *NoidSubmit。
// 爆块正路是 noidjob → SubmitNonce；本方法仅为满足接口 + 未来多节点广播。
func (c *Client) SubmitBlock(ctx context.Context, raw any) error {
	s, ok := raw.(*NoidSubmit)
	if !ok {
		return fmt.Errorf("[%s] SubmitBlock: 期望 *noidrpc.NoidSubmit, got %T（爆块请走 SubmitNonce）", c.name, raw)
	}
	_, err := c.SubmitNonce(ctx, s.TemplateID, s.NonceHex)
	return err
}

// NoidSubmit 通用 SubmitBlock 的载荷。
type NoidSubmit struct {
	TemplateID string
	NonceHex   string
}

// ---- 孤块分类（payout.NodeClassifier = BlockHashAt + Confirmations）----

// BlockHashAt 主链该高度的块 hash（paranoid_getBlockHash）。高度超过 tip 时节点返回
// null/错误 → 交给上层（Confirmations 先用 getChainInfo.height 挡住越界）。
func (c *Client) BlockHashAt(ctx context.Context, height uint64) (string, error) {
	var hash *string
	if err := c.call(ctx, c.hc, "paranoid_getBlockHash", []any{height}, &hash); err != nil {
		return "", err
	}
	if hash == nil {
		return "", fmt.Errorf("[%s] getBlockHash(%d) 返回 null（高度未上链）", c.name, height)
	}
	return *hash, nil
}

// Confirmations 我们提交的块当前确认数（<0 = 已不在主链 = 孤块）。
// 口径（施工图）：确认数 = getChainInfo.height − blockHeight；掉出主链（同高度块 hash
// 与我们的不符）返 -1。硬终局 18 块 ⇒ 成熟建议设 20。
func (c *Client) Confirmations(ctx context.Context, blockHash string, height uint64) (int64, error) {
	ci, err := c.getChainInfo(ctx)
	if err != nil {
		return 0, err
	}
	if ci.Height < height {
		return 0, nil // 链还没收进该高度（提交在飞）：继续等，别误判孤块
	}
	mainHash, err := c.BlockHashAt(ctx, height)
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(strings.TrimSpace(mainHash), strings.TrimSpace(blockHash)) {
		return -1, nil // 同高度是别人的块 → 我们的被甩掉
	}
	return int64(ci.Height - height), nil
}

// rpcCode 便于外部按错误码判定（保留，暂无调用点）。
func rpcCode(err error) (int, bool) {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

var _ = rpcCode // 保留（未来打款/退款分类可能用）
