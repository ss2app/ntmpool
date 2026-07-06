package payout

import "sync"

// MemBatchStore 内存 BatchStore（M1/测试；生产用 Postgres 实现做真恢复）。
type MemBatchStore struct {
	mu      sync.Mutex
	seq     int64
	batches map[int64]*Batch
}

func NewMemBatchStore() *MemBatchStore {
	return &MemBatchStore{batches: map[int64]*Batch{}}
}

func (s *MemBatchStore) NextBatchID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
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

func (s *MemBatchStore) Load(id int64) (*Batch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	return b, ok
}

func (s *MemBatchStore) Unfinished() []*Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Batch
	for _, b := range s.batches {
		switch b.Status {
		case "confirmed", "failed":
			// 完结
		default:
			out = append(out, b)
		}
	}
	return out
}
