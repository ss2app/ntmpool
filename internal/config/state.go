// 热状态落盘（docs/02 §7：每次热变更落盘 config.state.json，重启以最后热状态为准）。
//
// 只存热参数子集（payout/ports/币开关），不存密钥与静态项——
// adminToken/maskSecret 等经 ${ENV} 注入的密钥绝不能以展开后的明文落盘。
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// CoinHotState 一个币的热参数快照。
type CoinHotState struct {
	Payout          PayoutConfig `json:"payout"`
	Ports           []PortConfig `json:"ports"`
	NewConnsEnabled bool         `json:"newConnectionsEnabled"`
}

// HotState 全部币的热状态。
type HotState struct {
	Coins map[string]CoinHotState `json:"coins"`
}

// SaveState 原子落盘热状态。
func SaveState(path string, st HotState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadAndApplyState 读热状态并覆盖到 c（只覆盖 c 里存在的币；state 里多出的币忽略）。
// 文件不存在 = 无事发生。损坏文件报错（宁可启动失败也不静默用错参数）。
func LoadAndApplyState(path string, c *Config) (applied bool, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var st HotState
	if err := json.Unmarshal(b, &st); err != nil {
		return false, fmt.Errorf("热状态文件损坏 %s: %w", path, err)
	}
	for i := range c.Coins {
		hs, ok := st.Coins[c.Coins[i].ID]
		if !ok {
			continue
		}
		c.Coins[i].Payout = hs.Payout
		c.Coins[i].Ports = hs.Ports
		c.Coins[i].NewConnsEnabled = hs.NewConnsEnabled
		applied = true
	}
	if applied {
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			return false, fmt.Errorf("热状态覆盖后配置非法: %w", err)
		}
	}
	return applied, nil
}
