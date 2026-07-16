// Command brvcheck 一次性验证工具：用 brisviarpc 组一个 nonce=0 的完整块 hex 并打印，
// 供 `bitcoin-cli getblocktemplate {"mode":"proposal",...}` 确定性验证组块结构
// （coinbase/merkle/witness commitment/序列化）是否被节点接受——不需要有效 PoW。
// 用法: brvcheck <rpcurl> <rpcuser> <rpcpass> <poolAddress>
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/scashcc/ntmpool/internal/adapter/brisviarpc"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "用法: brvcheck <rpcurl> <rpcuser> <rpcpass> <poolAddress>")
		os.Exit(2)
	}
	c := brisviarpc.New("brv", os.Args[1], os.Args[2], os.Args[3], os.Args[4])
	blockHex, height, err := c.BuildBlockHex(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "BuildBlockHex err:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "height=%d blockHexLen=%d\n", height, len(blockHex))
	fmt.Println(blockHex) // stdout 只出 hex，便于管道喂 proposal
}
