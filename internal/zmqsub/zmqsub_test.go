package zmqsub

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"github.com/scashcc/ntmpool/internal/core"
)

// fakePub 最小 ZMTP 3.0 PUB 服务端：完成握手、收订阅、发一条 bitcoind 风格
// hashblock 三帧消息。作为手写协议的对端预言机。
func fakePub(t *testing.T, ln net.Listener, blockHash []byte) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	// greeting 交换
	g := make([]byte, 64)
	g[0], g[9], g[10], g[11] = 0xFF, 0x7F, 3, 0
	copy(g[12:], "NULL")
	if _, err := conn.Write(g); err != nil {
		t.Errorf("pub greeting: %v", err)
		return
	}
	peer := make([]byte, 64)
	if _, err := io.ReadFull(conn, peer); err != nil {
		t.Errorf("pub 读 greeting: %v", err)
		return
	}
	if peer[0] != 0xFF || peer[10] != 3 {
		t.Errorf("客户端 greeting 非 ZMTP 3.0: %x", peer[:12])
		return
	}

	// 读客户端 READY，校验 Socket-Type=SUB；回 READY(Socket-Type=PUB)
	flags, body, err := readFrame(conn)
	if err != nil || flags&flagCommand == 0 || commandName(body) != "READY" {
		t.Errorf("期待 READY: flags=%#x err=%v", flags, err)
		return
	}
	meta := commandData(body)
	if want := "Socket-Type"; len(meta) < 1+len(want)+4+3 || string(meta[1:1+len(want)]) != want ||
		string(meta[1+len(want)+4:1+len(want)+4+3]) != "SUB" {
		t.Errorf("READY 元数据缺 Socket-Type=SUB: %q", meta)
		return
	}
	var ready []byte
	ready = append(ready, 5)
	ready = append(ready, "READY"...)
	ready = append(ready, byte(len("Socket-Type")))
	ready = append(ready, "Socket-Type"...)
	ready = binary.BigEndian.AppendUint32(ready, uint32(len("PUB")))
	ready = append(ready, "PUB"...)
	if err := writeFrame(conn, flagCommand, ready); err != nil {
		t.Errorf("pub READY: %v", err)
		return
	}

	// 读订阅消息（0x01+topic）
	flags, body, err = readFrame(conn)
	if err != nil || flags&flagCommand != 0 || len(body) == 0 || body[0] != 0x01 {
		t.Errorf("期待订阅消息: flags=%#x body=%q err=%v", flags, body, err)
		return
	}
	if string(body[1:]) != "hashblock" {
		t.Errorf("订阅主题错: %q", body[1:])
		return
	}

	// bitcoind hashblock：3 帧 multipart（topic + 32B hash + 4B LE seq）
	if err := writeFrame(conn, flagMore, []byte("hashblock")); err != nil {
		t.Error(err)
		return
	}
	if err := writeFrame(conn, flagMore, blockHash); err != nil {
		t.Error(err)
		return
	}
	if err := writeFrame(conn, 0, []byte{1, 0, 0, 0}); err != nil {
		t.Error(err)
		return
	}
	// 保持连接直到客户端退出（避免客户端读到 EOF 触发重连打断断言）
	buf := make([]byte, 1)
	_, _ = conn.Read(buf)
}

func TestSubscribeReceivesHashblock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	blockHash := make([]byte, 32)
	for i := range blockHash {
		blockHash[i] = byte(i)
	}
	go fakePub(t, ln, blockHash)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	z := &Notifier{Coin: "t", Endpoint: "tcp://" + ln.Addr().String(), Topic: "hashblock"}
	ch := make(chan core.TipEvent, 4)
	go func() { _ = z.Run(ctx, ch) }()

	select {
	case ev := <-ch:
		if ev.Source != "zmq" || ev.Coin != "t" {
			t.Fatalf("事件字段错: %+v", ev)
		}
		if ev.Hash != hex.EncodeToString(blockHash) {
			t.Fatalf("hash 错: %s", ev.Hash)
		}
	case <-ctx.Done():
		t.Fatal("超时未收到 hashblock 事件")
	}
}
