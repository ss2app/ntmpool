//go:build noid

package e2e

// ParanO(1)d (NOID) 全链路 e2e：假节点（节点端真 PoW 验证）× 真 CoinInstance
// （noidrpc + noidjob + noidhttp 方言 + noidp2b cgo 哈希器）× 真 HTTP 矿工。
//
// 跑通：拿模板 → 低难度拒 → 普通有效 share（★不上行节点）→ 去重 → stale →
// 爆块（★恰好上行一次，节点端真验 PoW 通过、tip 推进）→ 换模板。
//
// 本文件锁死的四条安全不变式（错一条就丢块 / 烧模板槽 / 误封矿工 / 挖废块）：
//  ① 普通 share 期间 submitBlock 上行次数恒为 0（烧模板槽 = 全池停摆）。
//  ② 爆块恰好上行一次，且节点端【真 PoW 验证】通过（池侧判定与节点同一把尺）。
//  ③ 普通有效 share 回 JSON-RPC 错误 -32001，但★不进 autoban violent 计数
//     （juno 2780962 老锄头被误封同款坑；连发 40 条不掉线才算过）。
//  ④ getBlockTemplate 始终带一个 coinbase 字符串参数（传 [] 会被真节点判
//     "No more params"——2026-08-12 实测抓到的真 bug，假节点 badParams 计数守着）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/coininstance"
	"github.com/scashcc/ntmpool/internal/config"
	"github.com/scashcc/ntmpool/internal/hasher/noidp2b"
)

const (
	noidPort        = 15921
	noidMinerAddr   = "o1vvvp34tzrcwmexv6fygj2qxs0jdnd29hyct40052v8c0zlyqx85qvw5wze"
	noidMinerWorker = "rig1"
	// noidShareDiff share 难度 256 ⇒ share target = floor((2^256−1)/256) ≈ 2^248
	// （顶 1 字节为 0，命中概率 2^-8），严格宽于全网 target（2^240，2^-16）。
	// Min=Max=256 把 vardiff 钉死，让门只由 target 决定（测试可复现）。
	noidShareDiff = 256
)

func noidTestConfig(nodeURL string, port int) config.CoinConfig {
	return noidTestConfigDiff(nodeURL, port, noidShareDiff)
}

// noidTestConfigDiff 指定 share 难度（vardiff 钉死在该值）。
func noidTestConfigDiff(nodeURL string, port int, shareDiff float64) config.CoinConfig {
	cfg := noidBaseConfig(nodeURL, port)
	cfg.Ports[0].Vardiff = config.VardiffConfig{
		Enabled: true, StartDiff: shareDiff,
		MinDiff: shareDiff, MaxDiff: shareDiff, TargetSeconds: 5,
	}
	return cfg
}

func noidBaseConfig(nodeURL string, port int) config.CoinConfig {
	return config.CoinConfig{
		ID: "noiddemo", Symbol: "NOID", Adapter: "noid-rpc", Algo: "poseidon2b/noid",
		Decimals: 6, // 1 NOID = 1e6 μNOID
		Nodes:    []config.NodeEndpoint{{URL: nodeURL, Pass: fakeNoidMiningKey}},
		// NOID 付款走节点 mining-key 钱包（池不持钥）；poolAddress 是展示占位。
		PoolAddress: "o1pooltreasuryplaceholder",
		Ports: []config.PortConfig{{
			Port: port, Mode: "pplns", Dialect: "noidhttp", Enabled: true,
		}},
		Payout: config.PayoutConfig{
			// 永不开（无 WalletAdapter；NOID tx 144 块 epoch 过期需重签）。
			Enabled: false, PplnsFactor: 2, FeePercent: 5, MinPayout: "0.000001",
			Confirmations: 20, IntervalSec: 600, // 20 = 硬终局 18 块 + 余量
		},
		MiningEnabled: true, NewConnsEnabled: true,
	}
}

// ---- 最小 NOID 矿工（NTMminer-noid / 官方 parano1d-miner 的行为子集）----

type noidMiner struct {
	t     *testing.T
	url   string
	token string // Bearer = "地址.worker"
	hc    *http.Client
	id    int
}

func newNoidMiner(t *testing.T, port int, token string) *noidMiner {
	return &noidMiner{
		t:     t,
		url:   fmt.Sprintf("http://127.0.0.1:%d/", port),
		token: token,
		// keep-alive 复用单连接 = 一台矿机（autoban 按连接记账）。
		hc: &http.Client{Timeout: 20 * time.Second},
	}
}

type noidRPCErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *noidRPCErr) String() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("code=%d msg=%q", e.Code, e.Message)
}

func (m *noidMiner) call(method string, params any) (json.RawMessage, *noidRPCErr, error) {
	m.id++
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": m.id, "method": method, "params": params,
	})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *noidRPCErr     `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("非法响应 %q: %w", string(raw), err)
	}
	return env.Result, env.Error, nil
}

type noidWireTemplate struct {
	TemplateID          string `json:"template_id"`
	PowFieldsHex        string `json:"pow_fields_hex"`
	NonceFieldIndex     int    `json:"nonce_field_index"`
	DifficultyTargetHex string `json:"difficulty_target_hex"`
	Height              uint64 `json:"height"`
	ExpiresInSeconds    int64  `json:"expires_in_seconds"`
	NTxs                int    `json:"n_txs"`
}

// getTemplate 拉一次模板；池暖机（无模板）时回 -32000，由调用方重试。
func (m *noidMiner) getTemplate() (*noidWireTemplate, *noidRPCErr, error) {
	res, rerr, err := m.call("paranoid_getBlockTemplate", []string{""})
	if err != nil || rerr != nil {
		return nil, rerr, err
	}
	var tw noidWireTemplate
	if err := json.Unmarshal(res, &tw); err != nil {
		return nil, nil, err
	}
	return &tw, nil, nil
}

// waitTemplate 轮询直到池装载好模板（noidrpc 单飞行槽制备 + coininstance 启动窗口）。
func (m *noidMiner) waitTemplate(timeout time.Duration) (*noidWireTemplate, error) {
	m.t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr string
	for time.Now().Before(deadline) {
		tw, rerr, err := m.getTemplate()
		if err != nil {
			lastErr = err.Error()
		} else if rerr != nil {
			lastErr = rerr.String()
		} else if tw != nil && tw.TemplateID != "" {
			return tw, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("等模板超时；最后一次: %s", lastErr)
}

// submit 提交一条 (template_id, nonce)。
func (m *noidMiner) submit(templateID string, nonce []byte) (json.RawMessage, *noidRPCErr, error) {
	return m.call("paranoid_submitBlock", []string{templateID, hex.EncodeToString(nonce)})
}

// ---- nonce 搜索（矿工侧本地 poseidon，与池/节点同一份 cgo 共识引擎）----

// findNoidNonce 从 start 起自增搜一个满足 accept 的 16B LE nonce。
// 返回 (nonce, 试过的次数, 是否找到)。
func findNoidNonce(fields []byte, start, limit uint64, accept func(nonce []byte) bool) ([]byte, uint64, bool) {
	nonce := make([]byte, noidp2b.NonceWireBytes)
	for i := uint64(0); i < limit; i++ {
		binary.LittleEndian.PutUint64(nonce[:8], start+i)
		if accept(nonce) {
			out := make([]byte, noidp2b.NonceWireBytes)
			copy(out, nonce)
			return out, i + 1, true
		}
	}
	return nil, limit, false
}

// leBig 32B 小端 → big.Int（比较 share/network target 松紧用）。
func leBig(b []byte) *big.Int {
	be := make([]byte, len(b))
	for i := range b {
		be[i] = b[len(b)-1-i]
	}
	return new(big.Int).SetBytes(be)
}

func mustDecodeHex(t *testing.T, s string, want int) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败 %q: %v", s, err)
	}
	if want > 0 && len(b) != want {
		t.Fatalf("hex 长度 %d，期望 %d（%q）", len(b), want, s)
	}
	return b
}

// ---- 主 e2e ----

func TestNoidFullPipeline(t *testing.T) {
	node := newFakeNoidNode()
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, noidTestConfig(node.URL(), noidPort), coininstance.Deps{})
	if err != nil {
		t.Fatalf("启动 NOID 币实例失败: %v", err)
	}
	defer inst.Stop()

	miner := newNoidMiner(t, noidPort, noidMinerAddr+"."+noidMinerWorker)
	tw, err := miner.waitTemplate(30 * time.Second)
	if err != nil {
		t.Fatalf("矿工拿模板: %v", err)
	}

	// —— 模板契约校验（挖废块的第一道闸）——
	if tw.NonceFieldIndex != 10 {
		t.Fatalf("nonce_field_index=%d，共识固定 10（错了就是挖废块）", tw.NonceFieldIndex)
	}
	fields := mustDecodeHex(t, tw.PowFieldsHex, noidp2b.FieldsWireBytes)
	shareTarget := mustDecodeHex(t, tw.DifficultyTargetHex, noidp2b.TargetBytes)
	netTarget := node.netTargetLE()
	if leBig(shareTarget).Cmp(leBig(netTarget)) <= 0 {
		t.Fatalf("share target 未严格宽于全网 target（share=%x net=%x）——本 e2e 的分岔前提不成立",
			leBig(shareTarget), leBig(netTarget))
	}
	t.Logf("✓ 模板契约：h=%d nonce_field_index=10 pow_fields=256B share_target 严格宽于全网 target", tw.Height)

	meetsShare := func(n []byte) bool { return noidp2b.Check(fields, n, shareTarget) }
	meetsNet := func(n []byte) bool { return noidp2b.Check(fields, n, netTarget) }

	// —— ① 低难度 share：连 share target 都不满足 → -32003 low difficulty ——
	lowNonce, _, ok := findNoidNonce(fields, 1, 4096, func(n []byte) bool { return !meetsShare(n) })
	if !ok {
		t.Fatal("找不到低难度 nonce（share target 过宽？）")
	}
	_, rerr, err := miner.submit(tw.TemplateID, lowNonce)
	if err != nil {
		t.Fatalf("提交低难度 share: %v", err)
	}
	if rerr == nil || rerr.Code != -32003 || !strings.Contains(rerr.Message, "low difficulty") {
		t.Fatalf("低难度 share 应回 -32003 low difficulty，实得 %s", rerr.String())
	}
	if n := node.submits(); n != 0 {
		t.Fatalf("★低难度 share 竟上行节点 %d 次（必须 0）", n)
	}
	t.Logf("✓ 低难度 share 被拒（-32003），未上行节点")

	// —— ② 普通有效 share：满足 share target 但不满足全网 target ——
	shareNonce, tries, ok := findNoidNonce(fields, 1, 200_000, func(n []byte) bool {
		return meetsShare(n) && !meetsNet(n)
	})
	if !ok {
		t.Fatal("找不到「满足 share 但不满足全网」的 nonce")
	}
	_, rerr, err = miner.submit(tw.TemplateID, shareNonce)
	if err != nil {
		t.Fatalf("提交普通 share: %v", err)
	}
	// ★官方 miner submit 成功后同高度不再挖 ⇒ 池必须回错误（非 stale）让它继续挖。
	if rerr == nil || rerr.Code != -32001 {
		t.Fatalf("普通有效 share 应回 -32001（让矿工继续同高度挖），实得 %s", rerr.String())
	}
	if !strings.Contains(rerr.Message, "keep mining") {
		t.Fatalf("-32001 文案应提示继续挖，实得 %q", rerr.Message)
	}
	// ★★核心安全不变式①：普通 share 绝不上行节点（否则烧掉单飞行槽，全池停摆）。
	if n := node.submits(); n != 0 {
		t.Fatalf("★★普通 share 竟上行 submitBlock %d 次（必须 0，否则烧模板槽）", n)
	}
	t.Logf("✓ 普通有效 share 回 -32001（搜了 %d 个 nonce），★未上行节点", tries)

	// —— ③ 去重：同 (template_id, nonce) 再提交 → duplicate ——
	_, rerr, err = miner.submit(tw.TemplateID, shareNonce)
	if err != nil {
		t.Fatalf("重复提交: %v", err)
	}
	if rerr == nil || !strings.Contains(rerr.Message, "duplicate") {
		t.Fatalf("重复 nonce 应判 duplicate，实得 %s", rerr.String())
	}
	t.Logf("✓ 重复 nonce 被去重（全池维度）")

	// —— ④ stale：未知 template_id → -32002（不进 violent 计数）——
	_, rerr, err = miner.submit("tpl-nonexistent-999", shareNonce)
	if err != nil {
		t.Fatalf("stale 提交: %v", err)
	}
	if rerr == nil || rerr.Code != -32002 {
		t.Fatalf("未知 template_id 应回 -32002 stale，实得 %s", rerr.String())
	}
	if n := node.submits(); n != 0 {
		t.Fatalf("★stale share 竟上行节点 %d 次", n)
	}
	t.Logf("✓ 未知 template_id 判 stale（-32002），未上行节点")

	// —— share 已进账本 ——
	if snap, err := inst.Ledger().Snapshot(ctx, "noiddemo"); err != nil {
		t.Fatalf("账本快照: %v", err)
	} else if snap.WindowShares < 1 {
		t.Fatalf("普通有效 share 未进账本: %+v", snap)
	}

	// —— ⑤ 爆块：满足全网 target ——
	t0 := time.Now()
	blockNonce, tries, ok := findNoidNonce(fields, 1_000_000, 4_000_000, meetsNet)
	if !ok {
		t.Fatal("预算内未找到爆块 nonce（全网 target 过严？）")
	}
	t.Logf("  爆块 nonce 搜索：%d 次 poseidon，耗时 %.2fs", tries, time.Since(t0).Seconds())

	res, rerr, err := miner.submit(tw.TemplateID, blockNonce)
	if err != nil {
		t.Fatalf("提交爆块解: %v", err)
	}
	if rerr != nil {
		t.Fatalf("爆块解应成功，实得错误 %s", rerr.String())
	}
	var blockHash string
	if err := json.Unmarshal(res, &blockHash); err != nil || len(blockHash) != 64 {
		t.Fatalf("爆块返回应是 64-hex 块 hash，实得 %s (%v)", string(res), err)
	}

	// ★★核心安全不变式②：爆块恰好上行一次，且节点端【真 PoW 验证】通过。
	tplCalls, submitCalls, accepted, badPoW, badParams, unauth := node.stats()
	if submitCalls != 1 {
		t.Fatalf("★★爆块应恰好上行 submitBlock 1 次，实得 %d", submitCalls)
	}
	if accepted != 1 {
		t.Fatalf("★★节点端真 PoW 验证应接受 1 个块，实得 %d（badPoW=%d）", accepted, badPoW)
	}
	if badPoW != 0 {
		t.Fatalf("★★池把 PoW 不达标的解上行了 %d 次（池侧判定与节点不同尺 = 会丢块）", badPoW)
	}
	// 节点 tip 已推进到模板高度，且 hash = 池算的 digest（逐字节同一把尺）。
	tipH, tipHash := node.tip()
	if tipH != tw.Height {
		t.Fatalf("节点 tip 应推进到 %d，实得 %d", tw.Height, tipH)
	}
	if !strings.EqualFold(tipHash, blockHash) {
		t.Fatalf("节点权威 hash %s ≠ 回给矿工的 %s（池/节点 digest 不一致）", tipHash, blockHash)
	}
	// 池账本已记块。
	snap, err := inst.Ledger().Snapshot(ctx, "noiddemo")
	if err != nil {
		t.Fatalf("账本快照: %v", err)
	}
	if snap.BlocksFound < 1 {
		t.Fatalf("池未记录爆块: %+v", snap)
	}
	t.Logf("✓ 爆块：池上行 1 次 → 节点真 PoW 验证通过 → tip %d→%d，池账本记块 %d 个",
		tipH-1, tipH, snap.BlocksFound)

	// —— ⑥ 协议契约：始终带 coinbase 参数；Bearer 正确；单槽缓存挡住了重复打节点 ——
	if badParams != 0 {
		t.Fatalf("★getBlockTemplate 有 %d 次传了空参（真节点会判 \"No more params\"）", badParams)
	}
	if unauth != 0 {
		t.Fatalf("★有 %d 次 RPC 缺/错 Bearer（真节点带 --mining-key 时全 RPC 要鉴权）", unauth)
	}
	if tplCalls > 4 {
		t.Fatalf("★打节点拉模板 %d 次——单飞行槽下客户端单槽缓存失效了（真节点会回 already active）", tplCalls)
	}
	t.Logf("✓ 协议契约：coinbase 参数恒带、Bearer 全程正确、打节点拉模板仅 %d 次（单槽缓存生效）", tplCalls)

	// —— ⑦ 爆块后换模板：矿工能拿到下一高度的新模板 ——
	next, err := miner.waitTemplate(60 * time.Second)
	if err != nil {
		t.Fatalf("爆块后拿新模板: %v", err)
	}
	if next.TemplateID == tw.TemplateID {
		t.Fatalf("爆块后仍在下发已消费的模板 %s（应停发并换新）", tw.TemplateID)
	}
	if next.Height != tw.Height+1 {
		t.Fatalf("新模板高度 %d，期望 %d", next.Height, tw.Height+1)
	}
	t.Logf("✓ 爆块后换模板：h=%d → h=%d，template_id 已更新", tw.Height, next.Height)

	t.Logf("✓✓ NOID 全链路端到端通过：模板 → 低难度拒 → 普通 share(不上行) → 去重 → stale → 爆块(上行1次·节点真验) → 换模板")
}

// TestNoidValidSharesNeverBan 锁死 juno 2780962 同款坑：普通有效 share 回的是
// JSON-RPC 错误（-32001），但它记为 core.OutcomeAccepted，★绝不能进 autoban 的
// violent 计数——否则正常挖矿的矿工会被自家池封掉。
func TestNoidValidSharesNeverBan(t *testing.T) {
	node := newFakeNoidNode()
	defer node.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := coininstance.Start(ctx, noidTestConfig(node.URL(), noidPort+1), coininstance.Deps{})
	if err != nil {
		t.Fatalf("启动 NOID 币实例失败: %v", err)
	}
	defer inst.Stop()

	miner := newNoidMiner(t, noidPort+1, noidMinerAddr+".banrig")
	tw, err := miner.waitTemplate(30 * time.Second)
	if err != nil {
		t.Fatalf("矿工拿模板: %v", err)
	}
	fields := mustDecodeHex(t, tw.PowFieldsHex, noidp2b.FieldsWireBytes)
	shareTarget := mustDecodeHex(t, tw.DifficultyTargetHex, noidp2b.TargetBytes)
	netTarget := node.netTargetLE()

	// 连发 40 条普通有效 share（每条都回 -32001）。若 -32001 被误计 violent，
	// autoban 会在阈值处断连 → 后续请求报错，测试炸。
	const want = 40
	var start uint64 = 1
	for i := 0; i < want; i++ {
		nonce, tried, ok := findNoidNonce(fields, start, 200_000, func(n []byte) bool {
			return noidp2b.Check(fields, n, shareTarget) && !noidp2b.Check(fields, n, netTarget)
		})
		if !ok {
			t.Fatalf("第 %d 条 share 搜不到 nonce", i+1)
		}
		start += tried // 下一条从这之后继续搜，天然避开 dup
		_, rerr, err := miner.submit(tw.TemplateID, nonce)
		if err != nil {
			t.Fatalf("第 %d 条 share 提交失败（连接被 autoban 断了？）: %v", i+1, err)
		}
		if rerr == nil || rerr.Code != -32001 {
			t.Fatalf("第 %d 条 share 应回 -32001，实得 %s", i+1, rerr.String())
		}
	}

	// 连接仍然健康：还能正常拿模板。
	if _, rerr, err := miner.getTemplate(); err != nil || rerr != nil {
		t.Fatalf("★发完 %d 条有效 share 后矿工被封/断开了（-32001 被误计 violent）: err=%v rpc=%s",
			want, err, rerr.String())
	}
	if n := node.submits(); n != 0 {
		t.Fatalf("★★%d 条普通 share 竟上行 submitBlock %d 次（必须 0）", want, n)
	}
	snap, _ := inst.Ledger().Snapshot(ctx, "noiddemo")
	if snap.WindowShares < want {
		t.Fatalf("账本应记满 %d 条 share，实得 %d", want, snap.WindowShares)
	}
	t.Logf("✓ 连发 %d 条有效 share：全部 -32001、连接不掉、账本记 %d 条、上行节点 0 次",
		want, snap.WindowShares)
}
