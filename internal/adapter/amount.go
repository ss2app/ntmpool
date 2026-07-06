package adapter

import (
	"fmt"
	"math/big"
	"strings"
)

// AtomicToDecimal 原子单位 → 十进制币字符串（纯整数运算，金额铁律：绝不过浮点）。
func AtomicToDecimal(atomic uint64, decimals int) string {
	if decimals <= 0 {
		return fmt.Sprintf("%d", atomic)
	}
	unit := big.NewInt(1)
	for i := 0; i < decimals; i++ {
		unit.Mul(unit, big.NewInt(10))
	}
	v := new(big.Int).SetUint64(atomic)
	q, r := new(big.Int).QuoRem(v, unit, new(big.Int))
	return fmt.Sprintf("%s.%0*s", q.String(), decimals, r.String())
}

// DecimalToAtomic 十进制币字符串 → 原子单位（纯字符串/整数运算）。
// 小数位超过 decimals 报错（绝不静默截断金额）。
func DecimalToAtomic(s string, decimals int) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("非法金额 %q", s)
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(fracPart) > decimals {
		// 只允许多余位全为 0（如 "1.230000000000" 对 8 位小数）
		extra := fracPart[decimals:]
		if strings.Trim(extra, "0") != "" {
			return 0, fmt.Errorf("金额 %q 小数位超过 %d 位", s, decimals)
		}
		fracPart = fracPart[:decimals]
	}
	fracPart += strings.Repeat("0", decimals-len(fracPart))
	digits := intPart + fracPart
	if strings.Trim(digits, "0123456789") != "" {
		return 0, fmt.Errorf("非法金额 %q", s)
	}
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok || !v.IsUint64() {
		return 0, fmt.Errorf("金额 %q 超出范围", s)
	}
	return v.Uint64(), nil
}
