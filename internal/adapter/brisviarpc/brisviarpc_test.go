package brisviarpc

import "testing"

// nonce 字段必须给【连接 tag】留出至少 1 字节。
//
// searchLen == nonceLen（tagBytes=0）时 cnjob.materialize 不写 tag，所有连接拿到逐字节
// 相同的 blob；锄头每换 job 都从 nonce 0 起扫，于是 N 台矿机算出完全相同的 hash 序列。
// share 去重是 per-connection 的，重复 share 会被正常接受并计分——池侧一切指标正常，
// 但实际覆盖的 nonce 空间只等于一台矿机，爆块率不随接入算力增长。
//
// 这条断言就是防止有人为了「给单连接更大 nonce 空间」把 searchLen 调回 nonceLen。
func TestNonceLayoutLeavesRoomForConnTag(t *testing.T) {
	if searchLen >= nonceLen {
		t.Fatalf("searchLen=%d nonceLen=%d：tag 字节数为 0，所有连接会拿到相同 blob 并扫相同 nonce",
			searchLen, nonceLen)
	}
	if nonceOffset+nonceLen != 80 {
		t.Fatalf("nonce 字段应落在 80 字节头的末尾，实为 offset=%d len=%d", nonceOffset, nonceLen)
	}
	// tag 1 字节 → 256 个取值，与 family_brisvia 的 SetConnIDSpace(256) 必须一致。
	tagBytes := nonceLen - searchLen
	if space := 1 << (8 * tagBytes); space != 256 {
		t.Fatalf("tag 空间 %d 与 family_brisvia 的 SetConnIDSpace(256) 不一致", space)
	}
}
