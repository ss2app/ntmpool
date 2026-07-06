// Package minersettings 矿工设置（docs/01 R5、docs/02 §3 密码参数）。
//
// 语义：
//   - 锄头密码字段逗号分隔：`d=8192`（固定难度，连接级）、`mp=21`（起付额，持久）、
//     `pl=` 兼容 yiimp 系（= mp）；不含 `=` 的首个 token 视为「设置密码」。
//   - 首个带设置密码的连接把密码绑定到该地址；之后改持久设置需同一密码
//     （stratum / 面板 API 都凭它）。未绑定密码前设置可随意改（低价值，先用先得）。
//   - 起付额下限 = 池默认（防设 0 刷打款 tx），上限可配（防手滑设 1e9 永不打款；
//     面板可见 + 管理后台可重置）。
package minersettings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PasswordParams 从锄头密码字段解析出的参数。
type PasswordParams struct {
	Password  string  // 设置密码（首个不含 = 的 token；"x"/"" 占位符忽略）
	FixedDiff float64 // d=；0 = 未设置
	MinPayout float64 // mp= / pl=；0 = 未设置
}

// ParsePassword 解析锄头密码字段。未知 key=value 忽略（向后兼容）。
func ParsePassword(s string) PasswordParams {
	var p PasswordParams
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		k, v, found := strings.Cut(tok, "=")
		if !found {
			// 首个非占位裸 token = 设置密码
			if p.Password == "" && tok != "x" && tok != "X" {
				p.Password = tok
			}
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "d":
			p.FixedDiff, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case "mp", "pl":
			p.MinPayout, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		}
	}
	return p
}

// Record 一个地址的持久设置（对应 miner_settings 表）。
type Record struct {
	PasswordHash string    `json:"passwordHash,omitempty"` // HMAC-SHA256(存储盐, 密码)
	MinPayout    float64   `json:"minPayout,omitempty"`    // 0 = 未设置（用池默认）
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Store 全池矿工设置（coin → addr → Record），JSON 落盘。
type Store struct {
	mu   sync.Mutex
	path string // 空 = 纯内存
	salt []byte // 密码 hash 盐（进程配置，非 per-record；防彩虹表够用）
	data map[string]map[string]*Record

	MaxMinPayout float64 // mp= 上限（0 = 不限）
}

type storeFile struct {
	Data map[string]map[string]*Record `json:"data"`
}

func New(path string, salt []byte) (*Store, error) {
	s := &Store{path: path, salt: salt, data: map[string]map[string]*Record{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("矿工设置文件损坏 %s: %w", path, err)
	}
	if f.Data != nil {
		s.data = f.Data
	}
	return s, nil
}

func (s *Store) hash(pwd string) string {
	h := hmac.New(sha256.New, s.salt)
	h.Write([]byte(pwd))
	return hex.EncodeToString(h.Sum(nil))
}

// ApplyPassword stratum authorize 时调用：按 R5 语义应用 mp=（并按需绑定密码）。
// 返回说明性 note（空 = 无事发生或成功静默），err 只在被拒时返回——
// 调用方**不因此断开矿工**（授权照常成功），只记日志。
func (s *Store) ApplyPassword(coin, addr string, p PasswordParams, poolDefaultMin float64) (note string, err error) {
	if p.MinPayout <= 0 && p.Password == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	per := s.data[coin]
	if per == nil {
		per = map[string]*Record{}
		s.data[coin] = per
	}
	rec := per[addr]
	if rec == nil {
		rec = &Record{}
		per[addr] = rec
	}

	// 密码校验/绑定（先于任何修改）
	if rec.PasswordHash != "" {
		if p.Password == "" || s.hash(p.Password) != rec.PasswordHash {
			return "", fmt.Errorf("设置密码不符，忽略本次设置变更")
		}
	} else if p.Password != "" {
		rec.PasswordHash = s.hash(p.Password)
		rec.UpdatedAt = time.Now()
		note = "已绑定设置密码"
	}

	if p.MinPayout > 0 {
		mp := p.MinPayout
		if mp < poolDefaultMin {
			mp = poolDefaultMin // 下限 = 池默认
		}
		if s.MaxMinPayout > 0 && mp > s.MaxMinPayout {
			return note, fmt.Errorf("mp=%v 超上限 %v，忽略", p.MinPayout, s.MaxMinPayout)
		}
		if rec.MinPayout != mp {
			rec.MinPayout = mp
			rec.UpdatedAt = time.Now()
			if note != "" {
				note += "；"
			}
			note += fmt.Sprintf("起付额=%v", mp)
		}
	}
	return note, s.saveLocked()
}

// SetMinPayout 面板/管理后台改起付额。管理后台传 bypassPassword=true 可跳过密码（重置用）。
func (s *Store) SetMinPayout(coin, addr, password string, mp float64, bypassPassword bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	per := s.data[coin]
	if per == nil {
		per = map[string]*Record{}
		s.data[coin] = per
	}
	rec := per[addr]
	if rec == nil {
		rec = &Record{}
		per[addr] = rec
	}
	if !bypassPassword && rec.PasswordHash != "" {
		if password == "" || s.hash(password) != rec.PasswordHash {
			return fmt.Errorf("设置密码不符")
		}
	}
	if s.MaxMinPayout > 0 && mp > s.MaxMinPayout {
		return fmt.Errorf("mp=%v 超上限 %v", mp, s.MaxMinPayout)
	}
	if mp < 0 {
		mp = 0
	}
	rec.MinPayout = mp
	rec.UpdatedAt = time.Now()
	return s.saveLocked()
}

// MinPayouts 该币全部地址级起付额覆盖（打款引擎 PayableBalances perAddr 参数）。
func (s *Store) MinPayouts(coin string) map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]float64{}
	for addr, rec := range s.data[coin] {
		if rec.MinPayout > 0 {
			out[addr] = rec.MinPayout
		}
	}
	return out
}

// Get 单地址设置（面板显示；不含密码 hash 的语义由调用方决定输出）。
func (s *Store) Get(coin, addr string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[coin][addr]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// Coins 有设置记录的币列表（管理后台遍历用）。
func (s *Store) Coins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for c := range s.data {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(storeFile{Data: s.data}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
