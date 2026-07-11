// Package zmqsub 最小纯 Go ZMTP 3.0 SUB 客户端——订阅 bitcoin 系节点的
// zmqpub 通知（zmqpubhashblock 等），实现 adapter.Notifier。
//
// 为什么手写不引库：go.mod 极简依赖面铁律（只 pgx）；cgo libzmq 会复杂化
// CI/交叉编译且引入原生依赖（工厂 pitfall：librandomx/libzmq 跨机 ISA 坑）。
// bitcoind 的 zmq pub 只用到 ZMTP 3.0 + NULL 安全机制 + PUB/SUB 的极小子集，
// 完整握手+读帧 ~200 行即可覆盖（asicseer-pool 定论：用 zmqpubhashblock，
// 别用 -blocknotify）。
//
// 协议参考 rfc.zeromq.org/spec/23 (ZMTP 3.0)：
//
//	greeting(64B) → NULL 握手（双方 READY command）→ SUB 发订阅消息
//	（0x01+topic，3.0 风格；libzmq 对 3.0 对端始终兼容）→ 循环读多帧消息。
//
// bitcoind hashblock 消息 = 3 帧：topic("hashblock") + 32B 块 hash(display 序，
// zmqpublishnotifier 发送前已反转) + 4B LE 序号。
package zmqsub

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// maxFrame 单帧上限（hashblock 帧最大 32B，1MB 纯防炸）。
const maxFrame = 1 << 20

// Notifier 一个 zmqpub 订阅（adapter.Notifier）。断线自动重连（指数退避）。
type Notifier struct {
	Coin     string
	Endpoint string // tcp://host:port（bitcoin 系 conf 的 zmqpubhashblock 值）
	Topic    string // 订阅主题，如 "hashblock"
}

// Run 阻塞运行：连接→握手→订阅→读消息→发 TipEvent；断线退避重连，ctx 取消退出。
func (z *Notifier) Run(ctx context.Context, ch chan<- core.TipEvent) error {
	addr := strings.TrimPrefix(z.Endpoint, "tcp://")
	backoff := time.Second
	for ctx.Err() == nil {
		err := z.session(ctx, addr, ch, &backoff)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			if backoff *= 2; backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return ctx.Err()
}

// session 一次连接的完整生命周期。首条消息到手即视为链路健康、重置退避。
func (z *Notifier) session(ctx context.Context, addr string, ch chan<- core.TipEvent, backoff *time.Duration) error {
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	// ctx 取消 → 关连接解除阻塞读（读循环无超时：没新块时静默是正常态）
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if err := handshake(conn, z.Topic); err != nil {
		return fmt.Errorf("zmtp 握手: %w", err)
	}

	for {
		parts, err := readMessage(conn)
		if err != nil {
			return err
		}
		if len(parts) == 0 || string(parts[0]) != z.Topic {
			continue
		}
		*backoff = time.Second
		ev := core.TipEvent{Coin: z.Coin, Source: "zmq", At: time.Now()}
		if len(parts) >= 2 {
			ev.Hash = hex.EncodeToString(parts[1])
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---- ZMTP 3.0 线协议 ----

const (
	flagMore    = 0x01
	flagLong    = 0x02
	flagCommand = 0x04
)

// handshake greeting 交换 + NULL READY 互发 + 订阅。
func handshake(conn net.Conn, topic string) error {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetDeadline(time.Time{})

	// greeting：signature(10B: FF …00… 7F) + ver 3.0 + mechanism NULL + as-server 0 + filler
	g := make([]byte, 64)
	g[0], g[9], g[10], g[11] = 0xFF, 0x7F, 3, 0
	copy(g[12:], "NULL")
	if _, err := conn.Write(g); err != nil {
		return err
	}
	peer := make([]byte, 64)
	if _, err := io.ReadFull(conn, peer); err != nil {
		return err
	}
	if peer[0] != 0xFF || peer[10] < 3 {
		return fmt.Errorf("对端不是 ZMTP 3.x (sig=%#x ver=%d)", peer[0], peer[10])
	}

	// READY: command-name "READY" + metadata Socket-Type=SUB
	var body []byte
	body = append(body, 5)
	body = append(body, "READY"...)
	body = append(body, byte(len("Socket-Type")))
	body = append(body, "Socket-Type"...)
	body = binary.BigEndian.AppendUint32(body, uint32(len("SUB")))
	body = append(body, "SUB"...)
	if err := writeFrame(conn, flagCommand, body); err != nil {
		return err
	}
	// 读对端 READY（吞掉到手的第一个 command；ERROR command 报错）
	flags, cbody, err := readFrame(conn)
	if err != nil {
		return err
	}
	if flags&flagCommand == 0 {
		return fmt.Errorf("握手期待 command 帧，收到 message")
	}
	if name := commandName(cbody); name == "ERROR" {
		return fmt.Errorf("对端拒绝: %s", commandData(cbody))
	}

	// 订阅（ZMTP 3.0 风格：一条 0x01+topic 的 message；libzmq 对 3.0 对端兼容）
	return writeFrame(conn, 0, append([]byte{0x01}, topic...))
}

// readMessage 读一条完整 multipart 消息（跳过途中 command 帧，如心跳）。
func readMessage(conn net.Conn) ([][]byte, error) {
	var parts [][]byte
	for {
		flags, body, err := readFrame(conn)
		if err != nil {
			return nil, err
		}
		if flags&flagCommand != 0 {
			// PING 须回 PONG（libzmq 开心跳时；bitcoind 默认不开，防御性支持）
			if commandName(body) == "PING" {
				pong := append([]byte{4}, "PONG"...)
				// PING body = name + 2B TTL + context；PONG 回显 context
				if data := commandData(body); len(data) >= 2 {
					pong = append(pong, data[2:]...)
				}
				if err := writeFrame(conn, flagCommand, pong); err != nil {
					return nil, err
				}
			}
			continue
		}
		parts = append(parts, body)
		if flags&flagMore == 0 {
			return parts, nil
		}
	}
}

func writeFrame(conn net.Conn, flags byte, body []byte) error {
	var head []byte
	if len(body) > 255 {
		head = binary.BigEndian.AppendUint64([]byte{flags | flagLong}, uint64(len(body)))
	} else {
		head = []byte{flags, byte(len(body))}
	}
	if _, err := conn.Write(head); err != nil {
		return err
	}
	_, err := conn.Write(body)
	return err
}

func readFrame(conn net.Conn) (byte, []byte, error) {
	var h [1]byte
	if _, err := io.ReadFull(conn, h[:]); err != nil {
		return 0, nil, err
	}
	flags := h[0]
	var size uint64
	if flags&flagLong != 0 {
		var s [8]byte
		if _, err := io.ReadFull(conn, s[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(s[:])
	} else {
		if _, err := io.ReadFull(conn, h[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(h[0])
	}
	if size > maxFrame {
		return 0, nil, fmt.Errorf("帧过大 %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, nil, err
	}
	return flags, body, nil
}

// commandName command 帧 body 的名字段（1B len + name）。
func commandName(body []byte) string {
	if len(body) == 0 || int(body[0])+1 > len(body) {
		return ""
	}
	return string(body[1 : 1+body[0]])
}

// commandData command 帧 body 的数据段（名字段之后）。
func commandData(body []byte) []byte {
	if len(body) == 0 || int(body[0])+1 > len(body) {
		return nil
	}
	return body[1+body[0]:]
}
