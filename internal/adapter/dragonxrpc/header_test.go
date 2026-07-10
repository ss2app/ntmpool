package dragonxrpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// 真链块 #3131000（2026-07-10 节点实取，docs/06 §4 唯一权威串）。
const (
	// 前 400 hex：173B 完整头(346) + txcount varint "02"(2) + coinbase 前缀(52)
	anchorRaw400  = "04000000281a06c48ce20f3727e32c78e60df70010bb7c64628849f6a40b3741db000000b9243da497d12f810dee48eea15ea4bb45124b5dc4b89657241c19b402a578ba68e087a00909ce98e77050f16d9be11c048ef0451d037d9ea06811f4f4e62b57e00f516ada0e011e9e020e0001408ffb00000000000000000000000000000000000000000000000020e87d60da9cd2389b691403bb76466d98a520996643871a28be0585778ceb8915020400008085202f89010000000000000000000000000000000000"
	anchorPrev    = "000000db41370ba4f6498862647cbb1000f70de6782ce327370fe28cc4061a28"
	anchorMerkle  = "ba78a502b4191c245796b8c45d4b1245bba45ea1ee48ee0d812fd197a43d24b9"
	anchorSapling = "572be6f4f41168a09e7d031d45f08e041ce19b6df15070e798ce0909a087e068"
	anchorTime    = 1783697376
	anchorBits    = "1e010eda"
	anchorHash    = "00000044826c11188c2bab69afe0f6b02772899bf714152fb10e03ee9b0661d0"
)

func sha256d(b []byte) []byte {
	d1 := sha256.Sum256(b)
	d2 := sha256.Sum256(d1[:])
	return d2[:]
}

// 组头函数 vs 真链块逐字节 + sha256d==块 hash（作业数学的地基锚）。
func TestHeaderAnchorBlock3131000(t *testing.T) {
	raw, err := hex.DecodeString(anchorRaw400)
	if err != nil {
		t.Fatal(err)
	}
	// merkle 内部序 = display 反转（本块 merkle 由链上 2 笔 tx 算出，此处直接取值；
	// merkle 计算逻辑由 TestMerkleRootInternal 单独锚）
	merkleInternal, err := reverseHex32(anchorMerkle)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := buildHeaderBlob(4, anchorPrev, anchorSapling, merkleInternal, anchorTime, anchorBits)
	if err != nil {
		t.Fatal(err)
	}
	// nonce 字段回填链上真值（矿工滚出来的），其余 108 字节必须与真链逐字节一致
	copy(blob[108:140], raw[108:140])
	if !bytes.Equal(blob, raw[:140]) {
		for i := range blob {
			if blob[i] != raw[i] {
				t.Fatalf("组头第 %d 字节失配: got %02x want %02x", i, blob[i], raw[i])
			}
		}
	}
	// 173B sha256d 反转 == 链上块 hash
	full := raw[:173]
	pow := sha256d(full)
	rev := make([]byte, 32)
	for i, v := range pow {
		rev[31-i] = v
	}
	if hex.EncodeToString(rev) != anchorHash {
		t.Fatalf("sha256d(173B) 反转 = %x, want %s", rev, anchorHash)
	}
}

// 组块序列化：header+0x20+solution+varint 的位置与真链前缀逐字节一致。
func TestSerializeBlockAnchorPrefix(t *testing.T) {
	raw, _ := hex.DecodeString(anchorRaw400)
	blob := raw[:140]
	solution := raw[141:173]
	cbHex := "0400008085202f8901deadbeef" // 假 coinbase（序列化只透传字节）
	txHex := "aa"
	out, err := serializeBlock(blob, solution, cbHex, []string{txHex})
	if err != nil {
		t.Fatal(err)
	}
	want := anchorRaw400[:346] + "02" + cbHex + txHex
	if hex.EncodeToString(out) != want {
		t.Fatalf("序列化失配:\ngot  %s\nwant %s", hex.EncodeToString(out), want)
	}
	// AuxHash 缺失必须报错（组块没有 rx_hash = 提交废块）
	if _, err := serializeBlock(blob, nil, cbHex, nil); err == nil {
		t.Fatal("nil rx_hash 未被拒绝")
	}
}

func TestMerkleRootInternal(t *testing.T) {
	a := "aa" + hexRepeat("11", 31)
	b := "bb" + hexRepeat("22", 31)
	// 单叶：root = txid 反转
	root, err := merkleRootInternal([]string{a})
	if err != nil {
		t.Fatal(err)
	}
	la, _ := reverseHex32(a)
	if !bytes.Equal(root, la) {
		t.Fatal("单叶 root 应等于反转 txid")
	}
	// 双叶：root = sha256d(la||lb)（独立内联计算比对）
	lb, _ := reverseHex32(b)
	want := sha256d(append(append([]byte{}, la...), lb...))
	root, _ = merkleRootInternal([]string{a, b})
	if !bytes.Equal(root, want) {
		t.Fatal("双叶 root 失配")
	}
	// 三叶：奇数复制末叶
	lvl1a := sha256d(append(append([]byte{}, la...), lb...))
	lc, _ := reverseHex32(a)
	lvl1b := sha256d(append(append([]byte{}, lc...), lc...))
	want = sha256d(append(append([]byte{}, lvl1a...), lvl1b...))
	root, _ = merkleRootInternal([]string{a, b, a})
	if !bytes.Equal(root, want) {
		t.Fatal("三叶（奇数复制）root 失配")
	}
}

// seed epoch 数学（interval 1024 / lag 64；真链坐标核过）。
func TestSeedHeight(t *testing.T) {
	cases := []struct{ h, want uint64 }{
		{3131000, 3130368}, // 锚块（哈希器锚3 同款坐标）
		{3131031, 3130368},
		{1024 + 64, 1024},
		{1024 + 63, 0},
		{2048 + 64, 2048},
	}
	for _, c := range cases {
		if got := seedHeight(c.h, 1024, 64); got != c.want {
			t.Fatalf("seedHeight(%d) = %d, want %d", c.h, got, c.want)
		}
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

func hexRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
