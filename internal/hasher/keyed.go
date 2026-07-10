package hasher

import "fmt"

// KeyedHasher 是带 key 的 PoW 算法（RandomX 家族：key = seed_hash 对应的 epoch 种子）。
// 与 Hasher 铁律相同：与节点/锄头同源、自带金锚、SelfTest 不过池拒绝启动。
//
// 实现须自行管理 key→VM/cache 的切换与缓存（RandomX 初始化一个 cache 要几百 ms，
// 绝不能每条 share 重建；惯例保留最近 2 个 epoch 的 VM，适配 seed 轮换窗口）。
type KeyedHasher interface {
	// Name 算法名，如 "rx/0"、"rx/dragonx"。
	Name() string
	// HashKeyed 用 key（RandomX seed）对 input 做共识哈希。
	HashKeyed(key, input []byte) ([]byte, error)
	// SelfTest 跑内置金锚 test vector。
	SelfTest() error
}

// TwoStageKeyedHasher 双段 PoW 算法（如 rx/dragonx：内层 RandomX 是矿工上报的
// result（badpow 逐字节比对它），外层 sha256d(140B||0x20||rx_hash) 才是难度与
// 块判定用的 PoW 值）。cnjob 探测到本接口即走双段编排；单段算法零影响。
//
// 契约：HashKeyed 必须返回 pow（与单段调用方口径一致——pow 反转即块 hash）。
type TwoStageKeyedHasher interface {
	KeyedHasher
	// HashKeyedTwoStage 返回 (result=矿工可见结果哈希, pow=最终 PoW 哈希)。
	HashKeyedTwoStage(key, input []byte) (result, pow []byte, err error)
}

var keyedRegistry = map[string]KeyedHasher{}

// RegisterKeyed 注册一个带 key 算法实现（在各实现包的 init 中调用）。
func RegisterKeyed(h KeyedHasher) {
	mu.Lock()
	defer mu.Unlock()
	keyedRegistry[h.Name()] = h
}

// GetKeyed 按算法名取带 key 哈希器。
func GetKeyed(name string) (KeyedHasher, error) {
	mu.RLock()
	defer mu.RUnlock()
	h, ok := keyedRegistry[name]
	if !ok {
		return nil, fmt.Errorf("keyed hasher %q 未注册", name)
	}
	return h, nil
}

// selfTestByName 在两个注册表中找该算法并跑金锚（启动门禁共用）。
func selfTestByName(name string) error {
	mu.RLock()
	h, plain := registry[name]
	kh, keyed := keyedRegistry[name]
	mu.RUnlock()
	if !plain && !keyed {
		return fmt.Errorf("hasher %q 未注册", name)
	}
	if plain {
		if err := h.SelfTest(); err != nil {
			return fmt.Errorf("hasher %s 金锚自检失败（拒绝启动）: %w", name, err)
		}
	}
	if keyed {
		if err := kh.SelfTest(); err != nil {
			return fmt.Errorf("keyed hasher %s 金锚自检失败（拒绝启动）: %w", name, err)
		}
	}
	return nil
}
