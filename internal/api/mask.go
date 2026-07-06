// 地址脱敏（docs/01 R14.6、docs/02 §7）：
// 列表/榜单一律显示「前6…后4」，并附池侧密钥 HMAC 短哈希做稳定匿名 ID。
// 地址是链上公开集合，无密钥的纯 hash 可被字典反查——必须 HMAC（密钥不出池）。
package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
)

// Masker 池侧密钥脱敏器。同一密钥下同一地址的 ID 稳定（前端可跨页面聚合同一矿工）。
type Masker struct {
	secret []byte
}

// NewMasker secret 为空时随机生成（本进程内稳定，重启后 ID 变化——生产应配置固定密钥）。
func NewMasker(secret []byte) *Masker {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("[api] 生成随机脱敏密钥失败: %v", err)
		}
		log.Printf("[api] ⚠ 未配置 maskSecret，使用随机脱敏密钥（重启后匿名 ID 会变化）")
	}
	return &Masker{secret: secret}
}

// ID 稳定匿名 ID：HMAC-SHA256(secret, addr) 前 8 hex。
func (m *Masker) ID(addr string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(addr))
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// Display 展示形式：前6…后4；短地址整体打码。
func (m *Masker) Display(addr string) string {
	if len(addr) >= 12 {
		return addr[:6] + "…" + addr[len(addr)-4:]
	}
	if len(addr) > 2 {
		return addr[:2] + "…"
	}
	return "…"
}
