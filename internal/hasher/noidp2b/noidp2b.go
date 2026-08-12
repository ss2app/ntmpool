//go:build noid

// Package noidp2b ParanO(1)d (NOID) Poseidon2b PoW 池端重算哈希器
// （cgo 链 third_party/noidpow Rust staticlib）。
//
// 同源铁律：noidpow crate 不实现任何算法，digest 只经官方
// noid_chain::consensus::pow::poseidon_pow_digest_from_fields、target 判定只经官方
// le256_lt（path 依赖直指 parano1d-src 源码树）——矿工/节点/池同一份共识引擎，
// 跨实现逐字节一致由源头保证。
//
// wire 口径（官方 noid_extminer 同款）：
//   - fields：256B = template pow_fields_hex 解码后的原始字节（16 字段 × 16B LE）；
//   - nonce：16B LE u128（矿工提交 hex::encode(nonce.to_le_bytes())）；
//     验证时覆写 fields[POW_NONCE_FIELD_INDEX=10]，wire 里 index 10 的 16B 是占位垃圾；
//   - 判定：digest 与 target 按 256-bit 小端严格 `<`（等于拒绝）。
//
// 构建（先编 Rust staticlib，再带 -tags noid 编 Go）：
//
//	Windows（本机）：
//	  set PATH=C:\msys64\mingw64\bin;%PATH%
//	  set CFLAGS_x86_64_pc_windows_gnu=-D__USE_MINGW_ANSI_STDIO=1 -Wno-format
//	  cd third_party\noidpow
//	  cargo +stable-x86_64-pc-windows-gnu build --release --target x86_64-pc-windows-gnu
//	  （⚠必须 windows-gnu 目标：cgo 用 mingw gcc，msvc ABI 的 .lib 链不进来；
//	    CFLAGS 是 libmdbx 在 mingw 下 %zu+-Werror 的解法）
//	Linux（5850U bionic chroot / 154）：
//	  cd third_party/noidpow && cargo build --release
//	  （产物 target/release/libnoidpow.a；staticlib 只含未绑定符号，glibc 版本
//	    在最终 go build 链接时才落定 → cargo 可在 chroot 外跑，go build 在 chroot 内跑）
//	之后：
//	  CGO_ENABLED=1 go test -tags noid ./internal/hasher/noidp2b/
//
// 上游 parano1d-src 必须与 pool 仓同级目录（noidpow 的 path 依赖是 ../../../parano1d-src）。
//
// 线程安全：Poseidon2b 纯函数；Rust 侧全局状态仅为 OnceLock 只写一次的懒表
// （轮常数展开表、CPU 特性探测缓存），并发调用安全，无需加锁、无 VM/cache 概念。
package noidp2b

/*
#cgo windows LDFLAGS: ${SRCDIR}/../../../third_party/noidpow/target/x86_64-pc-windows-gnu/release/libnoidpow.a -lntdll -luser32 -lkernel32 -ladvapi32 -lole32 -lbcrypt -luserenv -lws2_32 -ldbghelp
#cgo linux LDFLAGS: ${SRCDIR}/../../../third_party/noidpow/target/release/libnoidpow.a -lpthread -ldl -lm -lrt
#include <stdint.h>

// 见 third_party/noidpow/src/lib.rs（#[no_mangle] extern "C"）。
void    noid_pow_digest(const uint8_t* fields_le, const uint8_t* nonce_le, uint8_t* out);
int32_t noid_pow_check(const uint8_t* fields_le, const uint8_t* nonce_le, const uint8_t* target_le);
*/
import "C"

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/scashcc/ntmpool/internal/hasher"
)

const (
	// FieldsWireBytes pow_fields_hex 解码后的字节数（16 字段 × 16B LE）。
	FieldsWireBytes = 256
	// NonceWireBytes 矿工提交 nonce 的字节数（LE u128）。
	NonceWireBytes = 16
	// TargetBytes difficulty_target 字节数（256-bit LE）。
	TargetBytes = 32
)

// Digest = PoseidonPoW(fields[10]:=nonce)，返回官方 BlockHash 原样 32B。
// 长度错误 = 调用方编程错误，直接 panic（共识路径不容默默截断）。
func Digest(fields, nonce []byte) [32]byte {
	mustLen(fields, nonce, nil)
	var out [32]byte
	C.noid_pow_digest(
		(*C.uint8_t)(&fields[0]),
		(*C.uint8_t)(&nonce[0]),
		(*C.uint8_t)(&out[0]))
	return out
}

// Check 返回 digest < target（官方 le256_lt：256-bit LE 严格 `<`，等于拒绝）。
func Check(fields, nonce, target []byte) bool {
	mustLen(fields, nonce, target)
	return C.noid_pow_check(
		(*C.uint8_t)(&fields[0]),
		(*C.uint8_t)(&nonce[0]),
		(*C.uint8_t)(&target[0])) == 1
}

func mustLen(fields, nonce, target []byte) {
	if len(fields) != FieldsWireBytes {
		panic(fmt.Sprintf("noidp2b: fields 必须 %dB，得到 %dB", FieldsWireBytes, len(fields)))
	}
	if len(nonce) != NonceWireBytes {
		panic(fmt.Sprintf("noidp2b: nonce 必须 %dB，得到 %dB", NonceWireBytes, len(nonce)))
	}
	if target != nil && len(target) != TargetBytes {
		panic(fmt.Sprintf("noidp2b: target 必须 %dB，得到 %dB", TargetBytes, len(target)))
	}
}

// Hasher 实现 hasher.Hasher。
//
// Hash 的 input 约定 = fields(256B) ‖ nonce(16B)，共 272B（NOID 是"字段表+nonce 槽位"
// 模型，没有比特币式完整 header blob；调用方——未来的 noidrpc 适配层——负责拼接）。
type Hasher struct{}

var _ hasher.Hasher = Hasher{}

func (Hasher) Name() string { return "poseidon2b/noid" }

// Hash input = 272B（fields ‖ nonce），返回 32B digest。
func (Hasher) Hash(input []byte) ([]byte, error) {
	if len(input) != FieldsWireBytes+NonceWireBytes {
		return nil, fmt.Errorf("poseidon2b/noid: input 必须 %dB（fields256‖nonce16），得到 %dB",
			FieldsWireBytes+NonceWireBytes, len(input))
	}
	d := Digest(input[:FieldsWireBytes], input[FieldsWireBytes:])
	return d[:], nil
}

// selfTestVectors 内嵌金锚（gen_kat 生成，来源=官方 poseidon_pow_digest_from_fields；
// 全量 68 条见 testdata/kat.json，这里取 4 条含边界的做启动门禁）。
// 注意条目 2/4 的 fields 在 index 10（字节 160..176）放的是随机垃圾——金锚同时锚定
// "nonce 覆写槽位"这一验证语义。
var selfTestVectors = []struct{ fields, nonce, digest string }{
	{ // 全零 fields + nonce=0
		strings.Repeat("00", 256),
		"00000000000000000000000000000000",
		"226907e6c7c1de5221f9e2c049659dafb7bcef23c4199a7a11436d31d5031185",
	},
	{ // 全 0xFF fields + nonce=u128::MAX
		strings.Repeat("ff", 256),
		"ffffffffffffffffffffffffffffffff",
		"21b3c6071e2ed6a9a71548630629ec7072d9045319b22086d5fdef284ffae74b",
	},
	{ // 随机 fields + nonce=0（kat.json 条目[2]）
		"1607fd032a8988e6054a7a92a782aba209f4abf965b678dee787a33a4136e575f3973016d2bfaff9c9394fd55e9c3f6413747904dcfa5bf60d5cf6a28bd26797" +
			"eb53ea3103c6baa6f6350e9f88695d2694debf4a390f640e104fdc4c1ad5a58da6416596ddeba3f433c1004975ed8c7e183800a3bf91e4c42732d966de92d90f" +
			"debde5b6cee2918dc4f5bf077dda5a7cc99ccd8106d0a3f1623bd5df8e3363b4fdc7dbf1f600d755f9935498145b05a049e040b3c2a2c31f35d3dbb7a15bb308" +
			"134a33cc6bba6e72839cdd2a81486b491e7414069ca1747f141fa9df28fb098e7efbdffde70bc1378c0062304d63a182b16ba658edf1e688491c219c02ce8fb4",
		"00000000000000000000000000000000",
		"034646d937051cbac5d580de8b29d49fdca00abc1633d177030a479c844c6b06",
	},
	{ // 随机 fields + 随机 nonce（kat.json 条目[4]）
		"71041641560594c261b5c98109015d5a3ec1861726fe3e1da3d24da1adee1fcb621ebca356356cd976ccf38e1e45e65c9afcd6e2c1d7938763f67a99695f9b8b" +
			"ceaf29a2c1ab80f6b6a3c7e22fa7c724d2a85ff853da2494319364692db9e1d5f1184ca4929a1d2348ceb72bb5902a5ef3ad99b711d33f276397c511bc107fad" +
			"b7bf442298c3eec173b801380f59e971c5516cd57de2718047bb6e83eb2fb44d120f186f483bfdefe32533c39ccfb502cfcbece924a5bda47db8aa9c17cb1287" +
			"3af4e3b6eca3b83c55865660d10dd9711ac2d61f34b32f8606ac94e855c8d6fdbc53508bc98b2aec529241920aa3aaf3f8d9c10fb7f0206c7d3e2c7ac7ad0c4c",
		"b259fcee7151501cebfbfaf9b82a54a1",
		"2d347ad4bf547423575915bd4d0e9d5f04d28945ffb2006682463a2c330815b5",
	},
}

// SelfTest 内嵌金锚逐字节比对 + Check 边界语义（等于必须拒绝）。
func (Hasher) SelfTest() error {
	for i, v := range selfTestVectors {
		fields, err := hex.DecodeString(v.fields)
		if err != nil {
			return fmt.Errorf("poseidon2b/noid 金锚[%d]: fields hex 坏: %w", i, err)
		}
		nonce, err := hex.DecodeString(v.nonce)
		if err != nil {
			return fmt.Errorf("poseidon2b/noid 金锚[%d]: nonce hex 坏: %w", i, err)
		}
		exp, err := hex.DecodeString(v.digest)
		if err != nil {
			return fmt.Errorf("poseidon2b/noid 金锚[%d]: digest hex 坏: %w", i, err)
		}
		got := Digest(fields, nonce)
		if !bytes.Equal(got[:], exp) {
			return fmt.Errorf("poseidon2b/noid 金锚[%d] 失配: got %x want %s", i, got, v.digest)
		}
		// Check 语义锚：target=digest 必须拒绝（严格 <），max target 必须接受。
		if Check(fields, nonce, got[:]) {
			return fmt.Errorf("poseidon2b/noid 金锚[%d]: target==digest 竟被接受（le256_lt 语义坏）", i)
		}
		maxTarget := bytes.Repeat([]byte{0xFF}, TargetBytes)
		if !Check(fields, nonce, maxTarget) && !bytes.Equal(got[:], maxTarget) {
			return fmt.Errorf("poseidon2b/noid 金锚[%d]: max target 竟被拒绝", i)
		}
	}
	return nil
}

func init() {
	// 启动金锚门禁（hasher.go 铁律：SelfTest 不过，池进程拒绝启动）。
	// KAT 失配 = 链错库/上游改共识/编译坏 → 字节错 = 挖的全是废块，宁可拒绝启动。
	if err := (Hasher{}).SelfTest(); err != nil {
		panic(fmt.Sprintf("noidp2b 启动金锚自检失败: %v", err))
	}
	hasher.Register(Hasher{})
}
