package power

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Supply is one reading of the battery and mains state.
type Supply struct {
	OK          bool // a battery with a usable reading exists
	Status      string
	Percent     int
	Watts       float64 // platform draw (or charge rate)
	Discharging bool
	AC          bool
	NowWh       float64
	FullWh      float64
	DesignWh    float64
	Cycles      int
}

// Remaining estimates time to empty (discharging) or full (charging).
// ok=false when the rate is unknown or the battery is neither.
func (s Supply) Remaining() (d time.Duration, ok bool) {
	if !s.OK || s.Watts <= 0 || s.FullWh <= 0 {
		return 0, false
	}
	var wh float64
	switch s.Status {
	case "Discharging":
		wh = s.NowWh
	case "Charging":
		wh = s.FullWh - s.NowWh
	default:
		return 0, false
	}
	return time.Duration(wh / s.Watts * float64(time.Hour)), true
}

// Health is full capacity as a percentage of design capacity (0 = unknown).
func (s Supply) Health() float64 {
	if s.DesignWh <= 0 || s.FullWh <= 0 {
		return 0
	}
	return s.FullWh / s.DesignWh * 100
}

// ReadSupply reads BAT* and mains adapters under dir (/sys/class/power_supply).
func ReadSupply(dir string) Supply {
	var s Supply
	b := ReadBattery(dir)
	s.Watts, s.Discharging, s.OK = b.Watts, b.Discharging, b.OK
	bats, _ := filepath.Glob(filepath.Join(dir, "BAT*"))
	var pctSum, pctN int
	for _, d := range bats {
		if st := readStr(filepath.Join(d, "status")); st != "" && (s.Status == "" || st != "Unknown") {
			if s.Status == "" || st == "Discharging" || st == "Charging" {
				s.Status = st
			}
		}
		if c, ok := readInt(filepath.Join(d, "capacity")); ok {
			pctSum += int(c)
			pctN++
		}
		v, _ := readInt(filepath.Join(d, "voltage_now"))
		if v == 0 {
			v, _ = readInt(filepath.Join(d, "voltage_min_design"))
		}
		s.NowWh += wh(d, "energy_now", "charge_now", v)
		s.FullWh += wh(d, "energy_full", "charge_full", v)
		s.DesignWh += wh(d, "energy_full_design", "charge_full_design", v)
		if c, ok := readInt(filepath.Join(d, "cycle_count")); ok && c > 0 {
			s.Cycles += int(c)
		}
	}
	if pctN > 0 {
		s.Percent = pctSum / pctN
	}
	ac, _ := filepath.Glob(filepath.Join(dir, "A*"))
	more, _ := filepath.Glob(filepath.Join(dir, "*AC*"))
	for _, d := range append(ac, more...) {
		if t := readStr(filepath.Join(d, "type")); t == "Mains" || t == "" {
			if o, ok := readInt(filepath.Join(d, "online")); ok && o == 1 {
				s.AC = true
			}
		}
	}
	if !s.AC && s.Status == "Charging" {
		s.AC = true
	}
	return s
}

// wh reads a capacity as watt-hours: energy_* is uWh, charge_* is uAh*uV.
func wh(dir, energy, charge string, voltageUV int64) float64 {
	if e, ok := readInt(filepath.Join(dir, energy)); ok {
		return float64(e) / 1e6
	}
	if c, ok := readInt(filepath.Join(dir, charge)); ok && voltageUV > 0 {
		return float64(c) / 1e6 * float64(voltageUV) / 1e6
	}
	return 0
}

func readStr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Consumer is all processes sharing one command name.
type Consumer struct {
	Name    string
	Procs   int
	CPUPct  float64 // of one core, summed
	Wakeups float64 // context switches per second
	Watts   float64 // estimated; 0 when no budget is known
}

// Reading is one monitor sample.
type Reading struct {
	At       time.Time
	Supply   Supply
	PkgWatts float64 // RAPL package power
	PkgOK    bool    // false: no RAPL, or unreadable without root
	CPUPct   float64 // whole machine, 0-100
	Top      []Consumer
	// Budget is where Consumer.Watts comes from: "rapl", "battery" or "".
	Budget string
	Valid  bool // false on the first sample: no baseline for deltas yet
}

// Roots are the filesystem locations the monitor reads (swappable in tests).
type Roots struct {
	Supply   string // /sys/class/power_supply
	Proc     string // /proc
	Powercap string // /sys/class/powercap
}

// DefaultRoots is the real system.
var DefaultRoots = Roots{"/sys/class/power_supply", "/proc", "/sys/class/powercap"}

// Monitor produces readings; each Sample diffs against the previous one.
// Not safe for concurrent use.
type Monitor struct {
	Roots Roots
	TopN  int
	now   func() time.Time

	prevT      time.Time
	prevProcs  map[int]Proc
	prevEnergy map[string]uint64
	prevBusy   uint64
	prevTotal  uint64
}

func NewMonitor() *Monitor {
	return &Monitor{Roots: DefaultRoots, TopN: 20, now: time.Now}
}

// Sample reads everything once.
func (m *Monitor) Sample() Reading {
	now := m.now()
	r := Reading{At: now, Supply: ReadSupply(m.Roots.Supply)}
	procs := SnapshotProcs(m.Roots.Proc)
	energy, maxRange := readRAPL(m.Roots.Powercap)
	busy, total := readCPUStat(m.Roots.Proc)

	dt := now.Sub(m.prevT).Seconds()
	if m.prevProcs != nil && dt > 0 {
		r.Valid = true
		if total > m.prevTotal {
			r.CPUPct = float64(busy-m.prevBusy) / float64(total-m.prevTotal) * 100
		}
		var uj float64
		for k, e := range energy {
			p, ok := m.prevEnergy[k]
			if !ok {
				continue
			}
			if e >= p {
				uj += float64(e - p)
			} else if mr := maxRange[k]; mr > 0 { // counter wrapped
				uj += float64(mr - p + e)
			}
			r.PkgOK = true
		}
		if r.PkgOK {
			r.PkgWatts = uj / 1e6 / dt
		}

		switch {
		case r.PkgOK && r.PkgWatts > 0:
			r.Budget = "rapl"
		case r.Supply.OK && r.Supply.Discharging:
			r.Budget = "battery"
		}
		r.Top = m.consumers(procs, dt, r)
	}
	m.prevT, m.prevProcs, m.prevEnergy, m.prevBusy, m.prevTotal = now, procs, energy, busy, total
	return r
}

func (m *Monitor) consumers(cur map[int]Proc, dt float64, r Reading) []Consumer {
	agg := map[string]*Consumer{}
	var totalCPU float64
	for pid, p := range cur {
		prev, ok := m.prevProcs[pid]
		if !ok || prev.Comm != p.Comm {
			continue // appeared mid-window: no baseline
		}
		dcpu, dctx := max(p.CPUUsec-prev.CPUUsec, 0), max(p.Ctx-prev.Ctx, 0)
		totalCPU += float64(dcpu)
		c := agg[p.Comm]
		if c == nil {
			c = &Consumer{Name: p.Comm}
			agg[p.Comm] = c
		}
		c.Procs++
		c.CPUPct += float64(dcpu) / (dt * 1e6) * 100
		c.Wakeups += float64(dctx) / dt
	}
	budget := 0.0
	switch r.Budget {
	case "rapl":
		budget = r.PkgWatts
	case "battery":
		budget = r.Supply.Watts
	}
	out := make([]Consumer, 0, len(agg))
	for _, c := range agg {
		if totalCPU > 0 && budget > 0 {
			c.Watts = budget * (c.CPUPct / 100 * dt * 1e6) / totalCPU
		}
		out = append(out, *c)
	}
	score := func(c Consumer) float64 { return c.CPUPct + c.Wakeups*wakeupCostUsec/1e4 }
	sort.Slice(out, func(i, j int) bool {
		if a, b := score(out[i]), score(out[j]); a != b {
			return a > b
		}
		return out[i].Name < out[j].Name
	})
	if m.TopN > 0 && len(out) > m.TopN {
		out = out[:m.TopN]
	}
	return out
}

// readRAPL sums nothing: it returns energy_uj per top-level package domain
// (intel-rapl:N, amd uses the same layout). Unreadable domains are omitted,
// which is the normal case without root on recent kernels.
func readRAPL(dir string) (energy, maxRange map[string]uint64) {
	energy, maxRange = map[string]uint64{}, map[string]uint64{}
	doms, _ := filepath.Glob(filepath.Join(dir, "*:*"))
	for _, d := range doms {
		base := filepath.Base(d)
		if strings.Count(base, ":") != 1 { // skip subdomains (core, uncore, dram)
			continue
		}
		e, err := os.ReadFile(filepath.Join(d, "energy_uj"))
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(e)), 10, 64)
		if err != nil {
			continue
		}
		energy[base] = v
		if mr, err := strconv.ParseUint(readStr(filepath.Join(d, "max_energy_range_uj")), 10, 64); err == nil {
			maxRange[base] = mr
		}
	}
	return
}

// readCPUStat returns busy and total jiffies from the aggregate cpu line.
func readCPUStat(procRoot string) (busy, total uint64) {
	b, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return 0, 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	var idle uint64
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		if i >= 8 { // guest columns are already counted in user/nice
			break
		}
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	return total - idle, total
}

// FormatDuration renders 2h05m style.
func FormatDuration(d time.Duration) string {
	h := int(d.Hours())
	return fmt.Sprintf("%dh%02dm", h, int(d.Minutes())%60)
}
