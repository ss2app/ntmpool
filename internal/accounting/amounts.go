package accounting

import "fmt"

// 金额工具：十进制字符串 ↔ 整数最小单位（聪）。内部计算绝不过浮点（金额铁律）。
// MemLedger 与 PGLedger 共用同一份换算，保证两实现分毫不差。
// ⚠ int64 上限：decimals=12 时单值 ≈ 9.2M 币；工厂币种量级足够，超限币种需换 big.Int。

func amountUnit(decimals int) int64 {
	u := int64(1)
	for i := 0; i < decimals; i++ {
		u *= 10
	}
	return u
}

func formatAmount(sat int64, decimals int) string {
	u := amountUnit(decimals)
	neg := ""
	if sat < 0 {
		neg = "-"
		sat = -sat
	}
	return fmt.Sprintf("%s%d.%0*d", neg, sat/u, decimals, sat%u)
}

// FormatAmount / ParseAmount 导出口（midjob 直付分账等外部包与账本同一套换算）。
func FormatAmount(sat int64, decimals int) string          { return formatAmount(sat, decimals) }
func ParseAmount(s string, decimals int) (int64, error)    { return parseAmount(s, decimals) }

// parseAmount 解析十进制字符串到聪。超出 decimals 的小数位截断（与入库精度一致）。
func parseAmount(s string, decimals int) (int64, error) {
	var whole, frac int64
	var fracDigits int
	neg := false
	i := 0
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i = 1
	}
	seenDot := false
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			seenDot = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("非法金额 %q", s)
		}
		if seenDot {
			if fracDigits < decimals {
				frac = frac*10 + int64(c-'0')
				fracDigits++
			}
		} else {
			whole = whole*10 + int64(c-'0')
		}
	}
	for fracDigits < decimals {
		frac *= 10
		fracDigits++
	}
	v := whole*amountUnit(decimals) + frac
	if neg {
		v = -v
	}
	return v, nil
}
