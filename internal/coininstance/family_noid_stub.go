//go:build !noid

package coininstance

import (
	"context"

	"github.com/scashcc/ntmpool/internal/config"
)

// buildNoidFamily 未带 -tags noid 构建时的桩：NOID 家族依赖 cgo Rust staticlib
// （poseidon2b/noid 哈希器，internal/hasher/noidp2b），必须 -tags noid 才编得进来。
// 不带 tag 的构建（本机快速开发 / 无 cgo CI）遇到 adapter="noid-rpc" 直接拒，
// 别让整仓 build 因缺 tag 而失败（与 family_bitcoin.go 的 switch 分支保持有定义）。
func buildNoidFamily(_ context.Context, cfg config.CoinConfig, _ int, _ *Instance) (*familyParts, error) {
	return nil, errf("[%s] adapter=noid-rpc 需 -tags noid 构建（cgo 链 poseidon2b/noid Rust staticlib）——当前二进制未带该 tag", cfg.ID)
}
