package coininstance

import (
	"fmt"
	"strconv"
)

func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
