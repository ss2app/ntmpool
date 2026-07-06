package stratum

import (
	"math/big"
	"sync"

	"github.com/scashcc/ntmpool/internal/btcwork"
)

// Job 是一个可推送给矿工的 bitcoin 系挖矿任务（stratum V1 notify 的全部字段）。
// 由 JobManager 从 BlockTemplate 构造；stratum 层只读它组 notify + 用它校验 share。
type Job struct {
	ID            string
	Height        uint64
	PrevHashBE    string // BE hex（组块/日志用）
	Coinbase      *btcwork.Coinbase
	MerkleBranch  [][]byte // 内部序
	RawTxs        [][]byte // 块内其余交易（见证序列化），组块用
	Version       uint32
	Bits          uint32
	NTime         uint32
	NetworkTarget *big.Int // GBT target（命中即爆块）
	NetDiff       float64  // 网络难度（PPLNS 窗口用）
	RewardSat     int64    // coinbase 总额（聪），爆块入账用
	CleanJobs     bool
}

// JobRegistry 持有当前与最近的 job，供 share 查找（一步 grace：当前或上一 job）。
type JobRegistry struct {
	mu      sync.RWMutex
	byID    map[string]*Job
	order   []string // 插入顺序，用于裁剪
	current string
	keep    int // 保留最近 N 个 job
}

func NewJobRegistry() *JobRegistry {
	return &JobRegistry{byID: map[string]*Job{}, keep: 4}
}

// Put 登记新 job 为 current，裁剪超出 keep 的旧 job。
func (r *JobRegistry) Put(j *Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[j.ID] = j
	r.order = append(r.order, j.ID)
	r.current = j.ID
	for len(r.order) > r.keep {
		old := r.order[0]
		r.order = r.order[1:]
		delete(r.byID, old)
	}
}

// Get 按 ID 取 job（找不到 = stale）。
func (r *JobRegistry) Get(id string) (*Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.byID[id]
	return j, ok
}

// Current 当前 job。
func (r *JobRegistry) Current() (*Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.byID[r.current]
	return j, ok
}
