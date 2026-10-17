package core

import (
	"strconv"
	"strings"
)

// ParseSize parses "788MB", "3.721kB", "1.8G", "2B". decimal=true uses
// 1000-based units (docker); false uses 1024 (journalctl). Unknown -> 0.
func ParseSize(s string, decimal bool) int64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	base := 1024.0
	if decimal {
		base = 1000.0
	}
	unit := strings.ToUpper(strings.TrimSpace(s[i:]))
	unit = strings.TrimSuffix(strings.TrimSuffix(unit, "IB"), "B")
	pow := -1
	if unit != "" {
		pow = strings.Index("KMGTP", unit[:1])
	}
	mult := 1.0
	for j := 0; j <= pow; j++ {
		mult *= base
	}
	return int64(n * mult)
}
