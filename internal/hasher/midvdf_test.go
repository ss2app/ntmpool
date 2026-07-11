package hasher

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// TestMidVDFCompressStandardBlake3 用官方 BLAKE3 已知值钉死单块压缩本身
// （独立于 mid 锚的第二重防线；来自 BLAKE3 官方测试向量）。
func TestMidVDFCompressStandardBlake3(t *testing.T) {
	cases := []struct {
		msg  []byte
		want string
	}{
		{[]byte{}, "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"},
		{[]byte("abc"), "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85"},
	}
	for _, c := range cases {
		var blk [16]uint32
		buf := make([]byte, 64)
		copy(buf, c.msg)
		for i := 0; i < 16; i++ {
			blk[i] = binary.LittleEndian.Uint32(buf[i*4:])
		}
		var h [8]uint32
		blake3Compress(&blake3IV, &blk, uint32(len(c.msg)), midHashFlags, &h)
		got := make([]byte, 32)
		for i, w := range h {
			binary.LittleEndian.PutUint32(got[i*4:], w)
		}
		want, _ := hex.DecodeString(c.want)
		if !bytes.Equal(got, want) {
			t.Fatalf("BLAKE3(%q): got=%x want=%s", c.msg, got, c.want)
		}
	}
}

// TestMidVDFGoldenAnchors 全 6 组 mid_kat.c 跨实现金锚（全量 1M 迭代）。
func TestMidVDFGoldenAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("1M 迭代 ×6，-short 跳过")
	}
	anchors := []struct {
		nonce uint64
		want  string
	}{
		{0x0000000000000000, "949382421e5f14787f4c0a7074c896dc12a005b6bdb78832145157f7306f4b48"},
		{0x0000000000000001, "f1e11dc61a8dfca871cd76c41e350ac637d059074669a105f0cf470453792484"},
		{0x000000000000002a, "6b5d8f7f7e8abbfdd0ae3301704fc1ffd904ca11026766bc52866c2987881a08"},
		{0x00000000deadbeef, "b5b477bd434e84931a020bc4ffddb890628c9ac033a242161e05d12060cebf17"},
		{0x0000000100000005, "8635d9819d4398c34af8515d0df095dca1d69c7fed46cc4a30320f3d1abf2f5c"},
		{0xffffffffffffffff, "746cd516f24dd3496c9af4d489c2e29068f88f93bc4bc9101ed5af01b23922ef"},
	}
	var ms [32]byte
	for i := range ms {
		ms[i] = byte(i)
	}
	h := midVDF{iters: midIterations}
	for _, a := range anchors {
		got, err := h.Hash(MidSeed(ms[:], a.nonce))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != a.want {
			t.Fatalf("nonce=%#x: got=%x want=%s", a.nonce, got, a.want)
		}
	}
}

func TestMidVDFSelfTest(t *testing.T) {
	if testing.Short() {
		t.Skip("1M 迭代 ×3，-short 跳过")
	}
	if err := (midVDF{iters: midIterations}).SelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestMidVDFInputLength(t *testing.T) {
	if _, err := (midVDF{iters: 1}).Hash(make([]byte, 39)); err == nil {
		t.Fatal("39 字节输入应报错")
	}
}

// BenchmarkMidVDFShare 一条 share 的池端重算成本（全量 1M 迭代）。
func BenchmarkMidVDFShare(b *testing.B) {
	h := midVDF{iters: midIterations}
	var ms [32]byte
	for i := 0; i < b.N; i++ {
		_, _ = h.Hash(MidSeed(ms[:], uint64(i)))
	}
}
