package velkarrpc

// client.go —— Velkar(Kaspa 系) 节点 gRPC 双向流客户端。
//
// Kaspa gRPC 不是普通 unary：整条 RPC 走 service RPC.MessageStream(stream)→(stream) 单条
// 双向流，请求/响应/通知全塞进 VelkardMessage 巨型 oneof。无 request-id，靠【顺序】配对
// （bridge velkar_grpc_client 同款）——本实现用 callMu 串行化整条"请求→响应"，一次只放一个
// 在途，读循环按 payload 类型分流：response→respCh（调用方取），notification→notifyCh（Notifier）。
//
// 无状态可重连（adapter.go 铁律 C9）：supervise goroutine 独占流生命周期，Recv 出错即
// 退避重连、重订阅；节点重启自愈，调用方在流断期间拿到 error 由上层下一 tick 重试。
//
// 逐行语义参考 bridge `D:\cs\velkar-poolowners-testnet\source\bridge\src\velkarapi.rs`。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/scashcc/ntmpool/internal/adapter/velkarrpc/protowire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// maxMsgSize gRPC 单消息上限（Kaspa 块/模板可含大量 tx；bridge 设 ~500MB）。
	maxMsgSize = 512 << 20
	// reconnectMax 重连退避上限。
	reconnectMax = 5 * time.Second
)

// nodeEvent 读循环从通知解出的链头事件，供 Notifier 转 core.TipEvent。
type nodeEvent struct {
	source string // grpc-newtemplate / grpc-blockadded
	height uint64 // 0 = 未知（NewBlockTemplate 通知不带高度）
	hash   string // 空 = 未知
}

// client 一个 velkard 节点的双向流连接。
type client struct {
	name string
	addr string // host:port（无 grpc:// 前缀）

	conn *grpc.ClientConn

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	stream protowire.RPC_MessageStreamClient
	ready  chan struct{} // 当前流就绪信号（close=就绪）；重连时替换

	sendMu sync.Mutex // 串行化 stream.Send
	callMu sync.Mutex // 串行化整条 请求→响应（保证一次只一个在途，可靠顺序配对）

	respCh   chan *protowire.VelkardMessage // 读循环 → 调用方
	notifyCh chan nodeEvent                 // 读循环 → Notifier

	startOnce sync.Once
}

func newClient(name, addr string) (*client, error) {
	// 容忍配置里带 scheme 前缀（grpc://host:port / tcp://…）——gRPC 拨号只认 host:port。
	for _, p := range []string{"grpc://", "tcp://", "http://", "https://"} {
		addr = strings.TrimPrefix(addr, p)
	}
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMsgSize),
			grpc.MaxCallSendMsgSize(maxMsgSize),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("[%s] gRPC 拨号 %s: %w", name, addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{
		name:     name,
		addr:     addr,
		conn:     conn,
		ctx:      ctx,
		cancel:   cancel,
		ready:    make(chan struct{}),
		respCh:   make(chan *protowire.VelkardMessage, 8),
		notifyCh: make(chan nodeEvent, 64),
	}
	return c, nil
}

// start 惰性启动 supervise（首次被 NodeAdapter/Notifier 使用时调）。
func (c *client) start() {
	c.startOnce.Do(func() { go c.supervise() })
}

func (c *client) Close() error {
	c.cancel()
	return c.conn.Close()
}

// supervise 独占流生命周期：建流→订阅→读循环，断则退避重连。
func (c *client) supervise() {
	backoff := 250 * time.Millisecond
	for c.ctx.Err() == nil {
		rpc := protowire.NewRPCClient(c.conn)
		stream, err := rpc.MessageStream(c.ctx)
		if err != nil {
			log.Printf("[%s] velkar gRPC 建流失败: %v（%v 后重试）", c.name, err, backoff)
			if !c.sleep(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		// 订阅新模板 + 新块（fire-and-forget；ack 落 respCh，下次 call 前会被 drain）
		c.subscribe(stream)
		c.setStream(stream)
		backoff = 250 * time.Millisecond

		for c.ctx.Err() == nil {
			msg, err := stream.Recv()
			if err != nil {
				if c.ctx.Err() == nil {
					log.Printf("[%s] velkar gRPC 流断开: %v（重连）", c.name, err)
				}
				break
			}
			c.dispatch(msg)
		}
		c.clearStream()
		if !c.sleep(300 * time.Millisecond) {
			return
		}
	}
}

func (c *client) subscribe(stream protowire.RPC_MessageStreamClient) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = stream.Send(&protowire.VelkardMessage{Payload: &protowire.VelkardMessage_NotifyNewBlockTemplateRequest{
		NotifyNewBlockTemplateRequest: &protowire.NotifyNewBlockTemplateRequestMessage{},
	}})
	_ = stream.Send(&protowire.VelkardMessage{Payload: &protowire.VelkardMessage_NotifyBlockAddedRequest{
		NotifyBlockAddedRequest: &protowire.NotifyBlockAddedRequestMessage{},
	}})
}

func (c *client) setStream(s protowire.RPC_MessageStreamClient) {
	c.mu.Lock()
	c.stream = s
	close(c.ready)
	c.mu.Unlock()
}

func (c *client) clearStream() {
	c.mu.Lock()
	c.stream = nil
	c.ready = make(chan struct{})
	// 清残留响应，避免下一条流串号
	for {
		select {
		case <-c.respCh:
			continue
		default:
		}
		break
	}
	c.mu.Unlock()
}

// currentStream 返回当前就绪流；未就绪则等到就绪或 ctx 取消。
func (c *client) currentStream(ctx context.Context) (protowire.RPC_MessageStreamClient, error) {
	c.mu.Lock()
	s, ready := c.stream, c.ready
	c.mu.Unlock()
	if s != nil {
		return s, nil
	}
	select {
	case <-ready:
		c.mu.Lock()
		s = c.stream
		c.mu.Unlock()
		if s == nil {
			return nil, fmt.Errorf("[%s] 流未就绪", c.name)
		}
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, fmt.Errorf("[%s] 客户端已关闭", c.name)
	}
}

// dispatch 按 payload 类型分流：通知→notifyCh，其余(响应/ack)→respCh。
func (c *client) dispatch(msg *protowire.VelkardMessage) {
	switch p := msg.Payload.(type) {
	case *protowire.VelkardMessage_NewBlockTemplateNotification:
		c.emit(nodeEvent{source: "grpc-newtemplate"})
		return
	case *protowire.VelkardMessage_BlockAddedNotification:
		ev := nodeEvent{source: "grpc-blockadded"}
		if b := p.BlockAddedNotification.GetBlock(); b != nil {
			if h := b.GetHeader(); h != nil {
				ev.height = h.GetDaaScore()
			}
			if vd := b.GetVerboseData(); vd != nil {
				ev.hash = vd.GetHash()
			}
		}
		c.emit(ev)
		return
	}
	// 响应/订阅 ack → 调用方（非阻塞；满则丢，只可能丢陈旧 ack）
	select {
	case c.respCh <- msg:
	default:
	}
}

func (c *client) emit(ev nodeEvent) {
	select {
	case c.notifyCh <- ev:
	default: // Notifier 慢/未接：丢弃（2s 轮询兜底仍在，池不会瞎）
	}
}

// call 串行化一次请求→响应：drain 残留→发送→等到 match 命中或 ctx 超时。
func (c *client) call(ctx context.Context, req *protowire.VelkardMessage, match func(*protowire.VelkardMessage) bool) (*protowire.VelkardMessage, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()

	stream, err := c.currentStream(ctx)
	if err != nil {
		return nil, err
	}
	// drain 上一次超时/订阅 ack 的残留
	for {
		select {
		case <-c.respCh:
			continue
		default:
		}
		break
	}

	c.sendMu.Lock()
	err = stream.Send(req)
	c.sendMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("[%s] 发送请求: %w", c.name, err)
	}

	for {
		select {
		case msg := <-c.respCh:
			if match(msg) {
				return msg, nil
			}
			// 非本次响应（订阅 ack / 陈旧串号）→ 继续等
		case <-ctx.Done():
			return nil, fmt.Errorf("[%s] 等待响应超时: %w", c.name, ctx.Err())
		case <-c.ctx.Done():
			return nil, fmt.Errorf("[%s] 客户端已关闭", c.name)
		}
	}
}

// ---- 类型化 RPC ----

func (c *client) getInfo(ctx context.Context) (*protowire.GetInfoResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_GetInfoRequest{GetInfoRequest: &protowire.GetInfoRequestMessage{}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_GetInfoResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_GetInfoResponse).GetInfoResponse
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("[%s] getInfo: %s", c.name, e.GetMessage())
	}
	return resp, nil
}

func (c *client) getDagInfo(ctx context.Context) (*protowire.GetBlockDagInfoResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_GetBlockDagInfoRequest{GetBlockDagInfoRequest: &protowire.GetBlockDagInfoRequestMessage{}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_GetBlockDagInfoResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_GetBlockDagInfoResponse).GetBlockDagInfoResponse
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("[%s] getBlockDagInfo: %s", c.name, e.GetMessage())
	}
	return resp, nil
}

func (c *client) estimateNetworkHashesPerSecond(ctx context.Context, windowSize uint32) (uint64, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_EstimateNetworkHashesPerSecondRequest{EstimateNetworkHashesPerSecondRequest: &protowire.EstimateNetworkHashesPerSecondRequestMessage{
		WindowSize: windowSize, // StartHash 留空 = 从 virtual(sink) 起算
	}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_EstimateNetworkHashesPerSecondResponse)
		return ok
	})
	if err != nil {
		return 0, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_EstimateNetworkHashesPerSecondResponse).EstimateNetworkHashesPerSecondResponse
	if e := resp.GetError(); e != nil {
		return 0, fmt.Errorf("[%s] estimateNetworkHashesPerSecond: %s", c.name, e.GetMessage())
	}
	return resp.GetNetworkHashesPerSecond(), nil
}

func (c *client) getTemplate(ctx context.Context, payAddress, extraData string) (*protowire.GetBlockTemplateResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_GetBlockTemplateRequest{GetBlockTemplateRequest: &protowire.GetBlockTemplateRequestMessage{
		PayAddress: payAddress,
		ExtraData:  extraData,
	}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_GetBlockTemplateResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_GetBlockTemplateResponse).GetBlockTemplateResponse
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("[%s] getBlockTemplate: %s", c.name, e.GetMessage())
	}
	return resp, nil
}

func (c *client) submitBlock(ctx context.Context, block *protowire.RpcBlock) (*protowire.SubmitBlockResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_SubmitBlockRequest{SubmitBlockRequest: &protowire.SubmitBlockRequestMessage{
		Block:             block,
		AllowNonDAABlocks: false,
	}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_SubmitBlockResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_SubmitBlockResponse).SubmitBlockResponse
	return resp, nil
}

func (c *client) getBlock(ctx context.Context, hash string, includeTx bool) (*protowire.GetBlockResponseMessage, error) {
	req := &protowire.VelkardMessage{Payload: &protowire.VelkardMessage_GetBlockRequest{GetBlockRequest: &protowire.GetBlockRequestMessage{
		Hash:                hash,
		IncludeTransactions: includeTx,
	}}}
	msg, err := c.call(ctx, req, func(m *protowire.VelkardMessage) bool {
		_, ok := m.Payload.(*protowire.VelkardMessage_GetBlockResponse)
		return ok
	})
	if err != nil {
		return nil, err
	}
	resp := msg.Payload.(*protowire.VelkardMessage_GetBlockResponse).GetBlockResponse
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("[%s] getBlock: %s", c.name, e.GetMessage())
	}
	return resp, nil
}

// ---- 工具 ----

func (c *client) sleep(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-c.ctx.Done():
		return false
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > reconnectMax {
		return reconnectMax
	}
	return d
}
