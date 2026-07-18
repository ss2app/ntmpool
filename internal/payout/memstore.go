package payout

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/scashcc/ntmpool/internal/core"
)

// MemBatchStore 内存 BatchStore（M1/测试；生产用 PGBatchStore 做真恢复）。
type MemBatchStore struct {
	mu      sync.Mutex
	seq     int64
	batches map[int64]*Batch
}

func NewMemBatchStore() *MemBatchStore {
	return &MemBatchStore{batches: map[int64]*Batch{}}
}

func (s *MemBatchStore) NextBatchID() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq, nil
}

func (s *MemBatchStore) Save(b *Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 存副本，避免调用方后续改动串味
	cp := *b
	cp.Outputs = map[string]string{}
	for k, v := range b.Outputs {
		cp.Outputs[k] = v
	}
	s.batches[b.ID] = &cp
	return nil
}

func (s *MemBatchStore) Load(id int64) (*Batch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	return b, ok, nil
}

func (s *MemBatchStore) FindByTxID(txid string) (*Batch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		if (b.TxID != "" && strings.EqualFold(b.TxID, txid)) ||
			(b.PlannedTxID != "" && strings.EqualFold(b.PlannedTxID, txid)) {
			return b, true, nil
		}
	}
	return nil, false, nil
}

func (s *MemBatchStore) Unfinished() ([]*Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Batch
	for _, b := range s.batches {
		switch b.Status {
		case core.PaymentConfirmed, core.PaymentFailed, core.PaymentVoided:
			// 完结
		default:
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *MemBatchStore) All() ([]*Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Batch, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID }) // 新→旧
	return out, nil
}

func (s *MemBatchStore) SentUnconfirmed() ([]*Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Batch
	for _, b := range s.batches {
		if b.Kind != "payout" || b.TxID == "" {
			continue
		}
		if b.Status == core.PaymentSent || b.Status == core.PaymentConfirming {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID }) // 旧→新
	return out, nil
}

func (s *MemBatchStore) MarkConfirmations(batchID, confirmations int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.batches[batchID]; ok {
		b.Confirmations = confirmations
	}
	return nil
}

func (s *MemBatchStore) MarkVoided(batchID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[batchID]
	if !ok {
		return fmt.Errorf("批次 %d 不存在", batchID)
	}
	b.Status = core.PaymentVoided
	return nil
}
