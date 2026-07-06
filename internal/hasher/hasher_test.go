package hasher

import "testing"

func TestSha256dSelfTest(t *testing.T) {
	h, err := Get("sha256d")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestSelfTestAllUnknown(t *testing.T) {
	if err := SelfTestAll([]string{"no-such-algo"}); err == nil {
		t.Fatal("未注册算法应当报错")
	}
}
