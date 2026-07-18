package payout

import (
	"testing"

	"github.com/scashcc/ntmpool/internal/core"
)

func TestMemBatchStoreMarkVoided(t *testing.T) {
	s := NewMemBatchStore()
	b := &Batch{
		ID: 7, Kind: "payout", Status: core.PaymentConfirmed,
		Outputs: map[string]string{"addrA": "1.50000000", "addrB": "2.25000000"},
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkVoided(b.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load(b.ID)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got.Status != core.PaymentVoided {
		t.Fatalf("status=%s", got.Status)
	}
	unf, err := s.Unfinished()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range unf {
		if item.ID == b.ID {
			t.Fatal("voided 批次不应出现在 Unfinished")
		}
	}
	if err := s.MarkVoided(999); err == nil {
		t.Fatal("不存在批次 MarkVoided 应报错")
	}
}
