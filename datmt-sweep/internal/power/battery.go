// Package power samples system power draw and per-cgroup activity.
package power

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Battery is one reading of total platform draw.
type Battery struct {
	Watts       float64
	Discharging bool
	OK          bool // false: no battery, or no usable power reading
}

func readInt(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// ReadBattery reads BAT* under supplyDir (/sys/class/power_supply). Uses
// power_now (uW), else current_now (uA) * voltage_now (uV).
func ReadBattery(supplyDir string) Battery {
	dirs, _ := filepath.Glob(filepath.Join(supplyDir, "BAT*"))
	var out Battery
	for _, d := range dirs {
		st, _ := os.ReadFile(filepath.Join(d, "status"))
		var w float64
		if p, ok := readInt(filepath.Join(d, "power_now")); ok && p > 0 {
			w = float64(p) / 1e6
		} else if c, ok := readInt(filepath.Join(d, "current_now")); ok && c > 0 {
			if v, ok := readInt(filepath.Join(d, "voltage_now")); ok {
				w = float64(c) / 1e6 * float64(v) / 1e6
			}
		}
		if w == 0 {
			continue
		}
		out.Watts += w
		out.OK = true
		if strings.TrimSpace(string(st)) == "Discharging" {
			out.Discharging = true
		}
	}
	return out
}
