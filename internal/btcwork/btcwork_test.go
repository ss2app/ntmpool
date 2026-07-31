package btcwork

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func fromBE(t *testing.T, beHex string) []byte {
	t.Helper()
	b, err := hex.DecodeString(beHex)
	if err != nil {
		t.Fatal(err)
	}
	return Reverse(b)
}

// 金锚①：比特币创世块 header 逐字节重建 + 哈希比对。
func TestSerializeHeaderGenesis(t *testing.T) {
	merkleInternal, _ := hex.DecodeString("3ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a")
	h := SerializeHeader(1, make([]byte, 32), merkleInternal, 1231006505, 0x1d00ffff, 2083236893)
	want, _ := hex.DecodeString(
		"0100000000000000000000000000000000000000000000000000000000000000" +
			"000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa" +
			"4b1e5e4a29ab5f49ffff001d1dac2b7c")
	if !bytes.Equal(h, want) {
		t.Fatalf("header 失配:\n got %x\nwant %x", h, want)
	}
	gotHash := hex.EncodeToString(Reverse(HeaderHash(h)))
	if gotHash != "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f" {
		t.Fatalf("创世块哈希失配: %s", gotHash)
	}
}

// 金锚②：比特币主网块 #100000 的 merkle root（4 笔交易，经典测试向量）。
func TestMerkleBlock100000(t *testing.T) {
	cb := fromBE(t, "8c14f0db3df150123e6f3dbbf30f8b955a8249b62ac1d1ff16284aefa3d06d87")
	txs := [][]byte{
		fromBE(t, "fff2525b8931402dd09222c50775608f75787bd2b87e56995a7bdd30f79702c4"),
		fromBE(t, "6359f0868171b1d194cbee1af2f16ea598ae8fad666d9b012c8ed2b79a236ec4"),
		fromBE(t, "e9a66845e05d5abc0ad04ec80f774a7e585c6e8db975962d069a522137b80c1d"),
	}
	branch := MerkleBranch(txs)
	root := MerkleRootFromBranch(cb, branch)
	got := hex.EncodeToString(Reverse(root))
	if got != "f3e94742aca4b5ef85488dc37c06c3282295ffec960994b2c0d5ac2a25a95766" {
		t.Fatalf("merkle root 失配: %s", got)
	}
}

// 空块（只有 coinbase）merkle root == coinbase txid。
func TestMerkleEmptyBlock(t *testing.T) {
	cb := fromBE(t, "8c14f0db3df150123e6f3dbbf30f8b955a8249b62ac1d1ff16284aefa3d06d87")
	branch := MerkleBranch(nil)
	if len(branch) != 0 {
		t.Fatalf("空块分支应为空, got %d", len(branch))
	}
	if !bytes.Equal(MerkleRootFromBranch(cb, branch), cb) {
		t.Fatal("空块 root 应等于 coinbase txid")
	}
}

func TestDiffTarget(t *testing.T) {
	if DiffToTarget(1).Cmp(Diff1Target) != 0 {
		t.Fatalf("DiffToTarget(1) != Diff1Target: %x", DiffToTarget(1))
	}
	// diff 越高目标越小
	if DiffToTarget(16).Cmp(DiffToTarget(1)) >= 0 {
		t.Fatal("diff 16 的目标应小于 diff 1")
	}
	// 创世块哈希（远小于 diff1 目标）应满足 diff 1
	gh := fromBE(t, "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f")
	if !HashMeetsTarget(gh, Diff1Target) {
		t.Fatal("创世块哈希应满足 diff1")
	}
	// 创世块实际 share 难度 ≈ 2536（diff1 目标 / 创世块哈希值，手算金锚）
	if d := ShareDiff(gh); d < 2500 || d > 2600 {
		t.Fatalf("创世块 share diff 异常: %v (期望 ~2536)", d)
	}
}

func TestScriptPushNum(t *testing.T) {
	// 必须逐字节等于 Bitcoin Core 的 `CScript() << nHeight`（节点 ContextualCheckBlock
	// 拿它构造 expect 比对 coinbase scriptSig 前缀，差一字节 = bad-cb-height）。
	// ★ 1..16 是单字节操作码 OP_1..OP_16，不是 push——本用例曾把 1 写成 "0101"（push 形式），
	//   把实现的 bug 固化成了期望值，于是测试长期通过却漏掉「任何新链前 16 块挖不到」。
	//   1 和 20 两行是 brisvia 节点 regtest 实测真值（coinbase scriptSig 分别为 5100 / 011400）。
	cases := map[uint64]string{
		0:      "00", // OP_0
		1:      "51", // OP_1  ← 节点实测锚
		2:      "52",
		15:     "5f",
		16:     "60",   // OP_16（最后一个操作码形式）
		17:     "0111", // 17 起才是 push 形式
		20:     "0114", // ← 节点实测锚
		127:    "017f",
		128:    "028000",
		265:    "020901",
		865000: "03e8320d", // 865000=0x0d32e8, LE=e8 32 0d
	}
	for n, want := range cases {
		if got := hex.EncodeToString(scriptPushNum(n)); got != want {
			t.Errorf("scriptPushNum(%d) = %s, want %s", n, got, want)
		}
	}
}

// stratum prevhash 编码与池端解码必须互为逆运算，且解码结果 = 内部序。
func TestPrevHashRoundTrip(t *testing.T) {
	be := "000000000000000000024e9be1c7b56cab6428f9920f957c380b45f664db316f"
	s, err := PrevHashStratum(be)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := PrevHashInternalFromStratum(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(internal, fromBE(t, be)) {
		t.Fatalf("round trip 失配: %x", internal)
	}
	if s == be {
		t.Fatal("stratum 序不应等于 BE 序（字序变换丢失）")
	}
}

// coinbase：构造→填 extranonce→txid 稳定性 + 见证序列化形态。
func TestCoinbase(t *testing.T) {
	payout, _ := hex.DecodeString("76a914000102030405060708090a0b0c0d0e0f1011121388ac") // 假 P2PKH
	wc, _ := hex.DecodeString("6a24aa21a9ed" + "e2f61c3f71d1defd3fa999dfa36953755c690689799962b48bebd836974e8cf9")
	cb, err := BuildCoinbase(102, 5000000000, payout, wc, []byte("/NTMPool/"), 8)
	if err != nil {
		t.Fatal(err)
	}
	en1 := []byte{0, 0, 0, 1}
	en2 := []byte{0, 0, 0, 2}
	raw, err := cb.Serialize(en1, en2)
	if err != nil {
		t.Fatal(err)
	}
	// 结构 sanity：version=2、1 输入、2 输出、locktime=0
	if raw[0] != 2 || raw[4] != 1 {
		t.Fatalf("version/输入数错误: %x", raw[:6])
	}
	if !bytes.Equal(raw[len(raw)-4:], []byte{0, 0, 0, 0}) {
		t.Fatal("locktime 应为 0")
	}
	// extranonce 变 → txid 变；同输入 → txid 稳定
	id1, _ := cb.TxID(en1, en2)
	id2, _ := cb.TxID(en1, []byte{0, 0, 0, 3})
	id3, _ := cb.TxID(en1, en2)
	if bytes.Equal(id1, id2) {
		t.Fatal("不同 extranonce 的 txid 不应相同")
	}
	if !bytes.Equal(id1, id3) {
		t.Fatal("同输入 txid 应稳定")
	}
	// 见证序列化：marker/flag + 34 字节见证段
	wit, err := cb.SerializeWitness(en1, en2)
	if err != nil {
		t.Fatal(err)
	}
	if len(wit) != len(raw)+2+34 {
		t.Fatalf("见证序列化长度异常: %d vs %d", len(wit), len(raw))
	}
	if wit[4] != 0x00 || wit[5] != 0x01 {
		t.Fatal("marker/flag 缺失")
	}
	// 长度错误必须报错
	if _, err := cb.Serialize(en1, []byte{1}); err == nil {
		t.Fatal("extranonce 长度错误应报错")
	}
}

func TestVarInt(t *testing.T) {
	cases := map[uint64]string{1: "01", 252: "fc", 253: "fdfd00", 65535: "fdffff", 65536: "fe00000100"}
	for n, want := range cases {
		if got := hex.EncodeToString(varInt(n)); got != want {
			t.Errorf("varInt(%d)=%s want %s", n, got, want)
		}
	}
}
