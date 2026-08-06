package junorpc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// 合成向量：字段摆位/字节序的结构锚（display→内部序反转、LE 数值、bits 反转）。
// 真链块锚（主网块逐字节 + rx_hash==块hash）在主网节点同步后补进哈希器
// SelfTest（见 coins/junocash/PLAN-矿池.md WP4）；本包的整链正确性由
// 5850U regtest e2e（junorig 真锄头爆块 → submitblock 接受）兜底。
const (
	tvPrev    = "aa11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	tvMerkle  = "bb00000000000000000000000000000000000000000000000000000000000000"
	tvCommit  = "cc0f0e0d0c0b0a090807060504030201f0e0d0c0b0a090807060504030201000"
	tvBits    = "1f07ffff"
	tvTime    = uint32(1780000000)
	tvVersion = uint32(4)
)

func revHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad hex %q", s)
	}
	out := make([]byte, 32)
	for i, v := range b {
		out[31-i] = v
	}
	return out
}

func TestBuildHeaderBlobLayout(t *testing.T) {
	blob, err := buildHeaderBlob(tvVersion, tvPrev, tvMerkle, tvCommit, tvTime, tvBits)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 140 {
		t.Fatalf("blob 长度 %d ≠ 140", len(blob))
	}
	if got := binary.LittleEndian.Uint32(blob[0:4]); got != tvVersion {
		t.Fatalf("version = %d, want %d", got, tvVersion)
	}
	if !bytes.Equal(blob[4:36], revHex(t, tvPrev)) {
		t.Fatal("prevhash 未按 display→内部序反转")
	}
	if !bytes.Equal(blob[36:68], revHex(t, tvMerkle)) {
		t.Fatal("merkleroot 未按 display→内部序反转")
	}
	if !bytes.Equal(blob[68:100], revHex(t, tvCommit)) {
		t.Fatal("blockcommitments 未按 display→内部序反转")
	}
	if got := binary.LittleEndian.Uint32(blob[100:104]); got != tvTime {
		t.Fatalf("time = %d, want %d", got, tvTime)
	}
	// bits "1f07ffff"（display）→ 内部 LE = ff ff 07 1f
	if want := []byte{0xff, 0xff, 0x07, 0x1f}; !bytes.Equal(blob[104:108], want) {
		t.Fatalf("bits = %x, want %x", blob[104:108], want)
	}
	for i := 108; i < 140; i++ {
		if blob[i] != 0 {
			t.Fatalf("nonce 区第 %d 字节非零", i)
		}
	}
	// 非法输入
	if _, err := buildHeaderBlob(4, "zz", tvMerkle, tvCommit, tvTime, tvBits); err == nil {
		t.Fatal("非法 prevhash 未被拒")
	}
	if _, err := buildHeaderBlob(4, tvPrev, tvMerkle, tvCommit, tvTime, "1f07"); err == nil {
		t.Fatal("非法 bits 未被拒")
	}
}

func TestSerializeBlockPrefix(t *testing.T) {
	blob, err := buildHeaderBlob(tvVersion, tvPrev, tvMerkle, tvCommit, tvTime, tvBits)
	if err != nil {
		t.Fatal(err)
	}
	rxHash := make([]byte, 32)
	for i := range rxHash {
		rxHash[i] = byte(i)
	}
	cbHex := "0500008085202f8901deadbeef" // 假 coinbase（序列化只透传字节）
	txHex := "aa"
	out, err := serializeBlock(blob, rxHash, cbHex, []string{txHex})
	if err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(blob) + "20" + hex.EncodeToString(rxHash) + "02" + cbHex + txHex
	if hex.EncodeToString(out) != want {
		t.Fatalf("序列化失配:\ngot  %s\nwant %s", hex.EncodeToString(out), want)
	}
	// rx_hash 缺失必须报错（组块没有 solution = 提交废块）
	if _, err := serializeBlock(blob, nil, cbHex, nil); err == nil {
		t.Fatal("nil rx_hash 未被拒绝")
	}
	// 无交易：txcount = 1（仅 coinbase）
	out, err = serializeBlock(blob, rxHash, cbHex, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[173] != 0x01 {
		t.Fatalf("空交易集 txcount = %02x, want 01", out[173])
	}
}

func TestCompactSize(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{1, "01"}, {0xfc, "fc"}, {0xfd, "fdfd00"}, {0xffff, "fdffff"}, {0x10000, "fe00000100"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(appendCompactSize(nil, c.n)); got != c.want {
			t.Fatalf("compactSize(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

// seed 是 GBT 权威值原样透传（内部序，不反转）——这里只锚形状校验；
// 「不反转」的语义由 regtest e2e（junorig 同一 seed 算出的 share 被池端重算认可）钉死。
func TestValidSeedHex(t *testing.T) {
	ok := "08c2af09a3f1b6d5e4c7889900aabbccddeeff00112233445566778899aabbcc"
	if err := validSeedHex(ok); err != nil {
		t.Fatal(err)
	}
	if err := validSeedHex(ok[:62]); err == nil {
		t.Fatal("短 seed 未被拒")
	}
	if err := validSeedHex("zz" + ok[2:]); err == nil {
		t.Fatal("非 hex seed 未被拒")
	}
}

func TestParseAmountZats(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"6.25", 625000000, false},
		{"6.25000000", 625000000, false},
		{"0.00000001", 1, false},
		{"12", 1200000000, false},
		{"0", 0, false},
		{"6.250000001", 0, true}, // 9 位小数
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseAmountZats(c.in, 8)
		if c.err != (err != nil) {
			t.Fatalf("parseAmountZats(%q) err=%v, want err=%v", c.in, err, c.err)
		}
		if !c.err && got != c.want {
			t.Fatalf("parseAmountZats(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIsUnifiedAddr(t *testing.T) {
	if !isUnifiedAddr("j1" + hexRepeat("q", 60)) {
		t.Fatal("主网 j1 被误拒")
	}
	if !isUnifiedAddr("jregtest1" + hexRepeat("q", 60)) {
		t.Fatal("regtest jregtest1 被误拒")
	}
	if isUnifiedAddr("t1abcdefghijklmnopqrstuvwxyz1234") {
		t.Fatal("透明地址被误收（打款只能进隐蔽池）")
	}
	if isUnifiedAddr("j1short") {
		t.Fatal("过短地址被误收")
	}
	if isUnifiedAddr("zs1" + hexRepeat("q", 60)) {
		t.Fatal("sapling zs 被误收（juno 是 Orchard-only 链）")
	}
}

func hexRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
