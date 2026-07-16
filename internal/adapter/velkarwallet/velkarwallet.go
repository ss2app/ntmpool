// Package velkarwallet Velkar(Kaspa 系) 打款适配器：gRPC 驱动常驻 velkar-walletd。
//
// walletd 是「常驻已解锁」模型：启动时用 --wallet/--password 打开钱包并经 wRPC 连节点，
// 之后对外暴露 gRPC。本适配器实现 adapter.WalletAdapter：
//   - SpendableBalance → GetBalance.available（成熟可花，已扣 coinbase 成熟期 + stasis）
//   - SendMany         → walletd SendMany（NTMPool 给 walletd 扩展的单笔多输出，守打款铁律 C2：
//                        绝不逐地址串行发 tx→UTXO 互撞；底层 wallet-core 一笔多输出，超 mass 自动拆）
//   - TxConfirmations  → 节点 mempool 信号（Kaspa 无 txindex：在池=待确认/离池=已入块）
//
// ★密钥不进池：walletd Send/SendMany 的 password 是「校验」非「解锁」——空 → walletd 用启动
// 密码。故池侧传空 password，钱包密钥只存在于 walletd 启动参数（铁律④）。
//
// 不实现 RawTxWallet：Kaspa Send 是 create+sign+broadcast 原子一步，无「签名即定 txid 不广播」
// 拆步原语（同 cnwallet），打款引擎自动退回 SendMany 路径。
package velkarwallet

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/scashcc/ntmpool/internal/adapter"
	pb "github.com/scashcc/ntmpool/internal/adapter/velkarwallet/pb"
)

// velkar 共识的 KIP-9 storage mass 参数与单笔上限（consensus/core/src/constants.rs
// STORAGE_MASS_PARAMETER = SOMPI_PER_VELKAR*10_000 = 1e12；wallet-core
// MAXIMUM_STANDARD_TRANSACTION_MASS = 100_000）。矿池打款 = 大 coinbase 拆小额矿工付款，
// storage mass ≈ C·Σ(1/output_sompi)，输出越小越装不下单笔，故 PlanBatches 须预分批。
const (
	velkarStorageMassParam = 100_000_000 * 10_000 // C = 1e12
	// 单笔 storage mass 装箱预算：远小于 100_000 上限，为「找零输出」预留一半空间——
	// 打款交易除矿工输出外必有一个找零回池，找零也计 storage mass（金额小则贡献大）。
	// 实测 90_000 预算装满会因找零溢出被节点拒（且旧 walletd 吞拒绝返回假 txid）。
	// ⚠ 根本对策仍是抬高 minPayout：单个输出 < ~0.15 VELK 时其自身 storage mass 就近 100_000，
	// 拆到单笔也无法打款；velkar minPayout 建议 ≥ 0.5（安全）或直接用大额（如 20，storage mass 微不足道）。
	velkarStorageMassBudget = 50_000
)

// NodeProbe 打款确认追踪所需的节点侧只读探针（velkarrpc.Adapter 实现：TxInMempool + Status）。
// 抽成接口避免 wallet 适配器强耦合 velkarrpc 具体类型。可为 nil（则确认追踪退化为 0）。
type NodeProbe interface {
	TxInMempool(ctx context.Context, txid string) (bool, error)
	Status(ctx context.Context) (adapter.ChainStatus, error) // Height = virtual DAA score
}

// Client 一个 velkar-walletd 的 gRPC 客户端。实现 adapter.WalletAdapter。
type Client struct {
	name     string
	decimals int
	from     []string // 源地址（= poolAddress，让 walletd 选中持 coinbase 的账户）；nil = 默认账户
	probe    NodeProbe

	cc  *grpc.ClientConn
	cli pb.VelkarwalletdClient

	mu      sync.Mutex
	sendDaa map[string]uint64 // txid → 广播时 virtual DAA（TxConfirmations 算深度）
}

var _ adapter.WalletAdapter = (*Client)(nil)

// New 建 walletd 客户端。addr = walletd gRPC host:port（如 127.0.0.1:28110，可带 grpc:// 前缀，
// grpc.NewClient 惰性连接，walletd 未起也不报错）。poolAddress 作 SendMany 的源账户选择。
// probe 可 nil（确认追踪退化）。
func New(name, addr, poolAddress string, decimals int, probe NodeProbe) (*Client, error) {
	if decimals <= 0 {
		decimals = 8 // Kaspa 系：1 VELK = 1e8 sompi
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("[%s] walletd 拨号 %s: %w", name, addr, err)
	}
	var from []string
	if poolAddress != "" {
		from = []string{poolAddress}
	}
	return &Client{
		name: name, decimals: decimals, from: from, probe: probe,
		cc: cc, cli: pb.NewVelkarwalletdClient(cc),
		sendDaa: make(map[string]uint64),
	}, nil
}

func (c *Client) Close() error { return c.cc.Close() }

// SpendableBalance walletd 成熟可花余额（十进制字符串，VELK）。
func (c *Client) SpendableBalance(ctx context.Context) (string, error) {
	r, err := c.cli.GetBalance(ctx, &pb.GetBalanceRequest{})
	if err != nil {
		return "", fmt.Errorf("[%s] GetBalance: %w", c.name, err)
	}
	return adapter.AtomicToDecimal(r.GetAvailable(), c.decimals), nil
}

// SendMany 单笔多输出批量打款（walletd 一笔交易多收款人，守铁律 C2），返回主 txid。
// 金额十进制字符串 → sompi 纯整数（金额铁律）。password 传空 = walletd 用启动密码。
func (c *Client) SendMany(ctx context.Context, outputs map[string]string) (string, error) {
	if len(outputs) == 0 {
		return "", fmt.Errorf("[%s] SendMany: 空输出", c.name)
	}
	outs := make([]*pb.SendManyOutput, 0, len(outputs))
	for addr, amt := range outputs {
		sompi, err := adapter.DecimalToAtomic(amt, c.decimals)
		if err != nil {
			return "", fmt.Errorf("[%s] 金额换算 %s=%q: %w", c.name, addr, amt, err)
		}
		outs = append(outs, &pb.SendManyOutput{ToAddress: addr, Amount: sompi})
	}

	// 广播时刻的 virtual DAA，供 TxConfirmations 算深度（best-effort，探针缺失时为 0）。
	var atDaa uint64
	if c.probe != nil {
		if st, err := c.probe.Status(ctx); err == nil {
			atDaa = st.Height
		}
	}

	r, err := c.cli.SendMany(ctx, &pb.SendManyRequest{
		Outputs:  outs,
		Password: "", // 空 = walletd 启动密码；池侧不持钱包密码（铁律④）
		From:     c.from,
		// FeePolicy nil → 生成器按 mass 自算最小费
	})
	if err != nil {
		// storage mass 超限是 walletd generator 的构造阶段拒绝（PSKB create，未 broadcast）→
		// 包 ErrNotBroadcast 让引擎安全退回余额。其余错误可能已广播超时，保持 unknown 语义。
		if isStorageMassError(err) {
			return "", fmt.Errorf("[%s] SendMany 构造被拒(storage mass，%d 输出): %v: %w",
				c.name, len(outs), err, adapter.ErrNotBroadcast)
		}
		return "", fmt.Errorf("[%s] SendMany: %w", c.name, err)
	}
	txids := r.GetTxIDs()
	if len(txids) == 0 {
		return "", fmt.Errorf("[%s] SendMany 未返回 txid", c.name)
	}
	primary := txids[0]
	c.mu.Lock()
	for _, id := range txids {
		c.sendDaa[id] = atDaa
	}
	c.mu.Unlock()
	// 一批理应一笔；若 walletd 因 mass 上限拆多笔，记日志（引擎按主 txid 追踪，其余仍已上链）。
	if len(txids) > 1 {
		log.Printf("[%s] ⚠ SendMany 拆成 %d 笔（超单 tx mass 上限），引擎按主 txid=%s 追踪；全部=%v",
			c.name, len(txids), primary, txids)
	}
	return primary, nil
}

// PlanBatches 实现 adapter.BatchPlanner：按 KIP-9 storage mass 贪心装箱，把一批打款输出
// 预分成若干可安全放进单笔交易的子批。每个输出的 storage mass 保守估作 C/amount_sompi
// （忽略 input 抵消 → 偏大 → 安全），累加到预算 velkarStorageMassBudget 就开新子批。
// 引擎逐子批独立原子打款，从根上避免整批一次 SendMany 撞 storage mass 上限被拒。
func (c *Client) PlanBatches(outputs map[string]string) []map[string]string {
	if len(outputs) <= 1 {
		if len(outputs) == 0 {
			return nil
		}
		return []map[string]string{outputs}
	}
	// 金额升序（小额 storage mass 大，先装）→ 装箱紧凑且结果稳定（不依赖 map 迭代序）。
	addrs := make([]string, 0, len(outputs))
	for a := range outputs {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		si, _ := adapter.DecimalToAtomic(outputs[addrs[i]], c.decimals)
		sj, _ := adapter.DecimalToAtomic(outputs[addrs[j]], c.decimals)
		if si != sj {
			return si < sj
		}
		return addrs[i] < addrs[j]
	})

	var batches []map[string]string
	cur := map[string]string{}
	var curMass float64
	flush := func() {
		if len(cur) > 0 {
			batches = append(batches, cur)
			cur = map[string]string{}
			curMass = 0
		}
	}
	for _, addr := range addrs {
		amt := outputs[addr]
		sompi, err := adapter.DecimalToAtomic(amt, c.decimals)
		if err != nil || sompi <= 0 {
			// 换算失败/零额：单独成批，让 SendMany 自然报错（绝不静默吞一笔打款）。
			flush()
			batches = append(batches, map[string]string{addr: amt})
			continue
		}
		sm := float64(velkarStorageMassParam) / float64(sompi)
		if len(cur) > 0 && curMass+sm > velkarStorageMassBudget {
			flush()
		}
		cur[addr] = amt
		curMass += sm
	}
	flush()
	return batches
}

// isStorageMassError 识别 walletd generator 的 storage mass 超限错误（构造阶段，未广播）。
func isStorageMassError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "storage mass") || strings.Contains(s, "mass exceeds")
}

// TxConfirmations 打款交易确认追踪（Kaspa 无 txindex → 用节点 mempool 信号）：
//   - 仍在内存池 → 0（尚未入块）
//   - 已离开内存池 → 已被接受入块，返回自广播以来的 virtual DAA 增量（≥1）
//   - 无节点探针 → 0（无法判定，保守当未确认）
//
// ⚠ 掉链/被拒的极端情形无法与「已接受」区分（无 txindex）；对成熟 coinbase 打款可忽略
// （成熟 UTXO 花费必被接受，Kaspa 无 RBF、接受后 GHOSTDAG 快速终局）。
func (c *Client) TxConfirmations(ctx context.Context, txid string) (int64, error) {
	if c.probe == nil {
		return 0, nil
	}
	inMem, err := c.probe.TxInMempool(ctx, txid)
	if err != nil {
		return 0, err
	}
	if inMem {
		return 0, nil // 待接受
	}
	st, err := c.probe.Status(ctx)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	at := c.sendDaa[txid]
	c.mu.Unlock()
	conf := int64(st.Height) - int64(at)
	if conf < 1 {
		conf = 1 // 已离池 = 至少入块 1 次
	}
	return conf, nil
}
