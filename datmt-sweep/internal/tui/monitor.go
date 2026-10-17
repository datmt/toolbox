package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/datmt/datmt-sweep/internal/power"
)

const (
	monInterval = 2 * time.Second
	monHistLen  = 120 // samples kept: 4 minutes at 2 s
)

type monMsg struct{ r power.Reading }

// monSample takes one reading now (the baseline).
func (m *Model) monSample() tea.Cmd {
	mon := m.Monitor
	return func() tea.Msg { return monMsg{mon.Sample()} }
}

// monNext waits one interval, then samples. Only one is ever in flight, so
// the Monitor needs no locking.
func (m *Model) monNext() tea.Cmd {
	mon := m.Monitor
	return tea.Tick(monInterval, func(time.Time) tea.Msg { return monMsg{mon.Sample()} })
}

// watts is the best whole-machine figure in a reading: battery draw when
// discharging, else RAPL package power, else 0.
func watts(r power.Reading) float64 {
	if r.Supply.OK && r.Supply.Discharging {
		return r.Supply.Watts
	}
	if r.PkgOK {
		return r.PkgWatts
	}
	if r.Supply.OK {
		return r.Supply.Watts // charging rate; labelled as such in the view
	}
	return 0
}

func (m *Model) monShort() string {
	if !m.monLast.Valid {
		return "…"
	}
	if w := watts(m.monLast); w > 0 {
		return fmt.Sprintf("%.1fW", w)
	}
	return fmt.Sprintf("%.0f%%cpu", m.monLast.CPUPct)
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// spark draws vals scaled to [0,max(vals)] in at most width cells.
func spark(vals []float64, width int) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var hi float64
	for _, v := range vals {
		hi = max(hi, v)
	}
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if hi > 0 {
			i = int(v / hi * float64(len(sparkRunes)-1))
		}
		b.WriteRune(sparkRunes[min(max(i, 0), len(sparkRunes)-1)])
	}
	return b.String()
}

func (m *Model) monitorView() string {
	bodyH := max(1, m.h-3)
	mainW := m.w - sideW - 1
	side := m.sidebar(bodyH)
	body := m.monitorBody(mainW)
	var b strings.Builder
	b.WriteString(headSt.Render("datmt-sweep · Monitor") + "\n")
	for i := 0; i < bodyH; i++ {
		l := ""
		if i < len(body) {
			l = trunc(body[i], mainW)
		}
		b.WriteString(pad(side[i], sideW) + dimSt.Render("│") + l + "\n")
	}
	foot := fmt.Sprintf("sampling every %s", monInterval)
	if m.monPause {
		foot = "paused"
	}
	b.WriteString(foot + "\n")
	b.WriteString(dimSt.Render("tab category · m monitor · p pause · q quit"))
	return b.String()
}

func (m *Model) monitorBody(w int) []string {
	r := m.monLast
	if !r.Valid {
		return []string{"", " sampling..."}
	}
	var o []string
	add := func(f string, a ...any) { o = append(o, fmt.Sprintf(f, a...)) }
	s := r.Supply

	o = append(o, "")
	switch {
	case s.OK && s.Discharging:
		add(" Battery   %d%%  discharging at %.1f W", s.Percent, s.Watts)
		if d, ok := s.Remaining(); ok {
			add("           about %s left", power.FormatDuration(d))
		}
	case s.OK && s.Status == "Charging":
		add(" Battery   %d%%  charging at %.1f W", s.Percent, s.Watts)
		if d, ok := s.Remaining(); ok {
			add("           about %s to full", power.FormatDuration(d))
		}
	case s.Status != "":
		add(" Battery   %d%%  %s", s.Percent, strings.ToLower(s.Status))
	default:
		add(" Battery   none (desktop)")
	}
	if s.AC {
		add(" Mains     plugged in")
	}
	if h := s.Health(); h > 0 {
		cyc := ""
		if s.Cycles > 0 {
			cyc = fmt.Sprintf(" · %d cycles", s.Cycles)
		}
		add(" Health    %.0f%% of design capacity%s", h, cyc)
	}
	if r.PkgOK {
		add(" CPU pkg   %.1f W (RAPL)", r.PkgWatts)
	}
	add(" CPU       %.0f%% busy", r.CPUPct)

	o = append(o, "")
	hw := min(w-2, monHistLen)
	label := "draw"
	if !(s.OK && s.Discharging) && r.PkgOK {
		label = "pkg power"
	}
	if len(m.monHist) > 0 && hw > 8 {
		var hi float64
		for _, v := range m.monHist {
			hi = max(hi, v)
		}
		add(" %s, last %d s, peak %.1f W", label, len(m.monHist)*int(monInterval.Seconds()), hi)
		add(" %s", spark(m.monHist, hw))
	}

	o = append(o, "")
	switch r.Budget {
	case "rapl":
		add(" Top consumers · watts = CPU package power split by CPU share (estimate)")
	case "battery":
		add(" Top consumers · watts = battery draw split by CPU share (estimate, includes idle hardware)")
	default:
		add(" Top consumers · no power source readable (needs battery discharge or root for RAPL); CPU only")
	}
	add(" %-24s %5s %8s %9s %8s", "COMMAND", "PROCS", "CPU", "WAKE/s", "WATTS")
	for _, c := range r.Top {
		wt := "-"
		if c.Watts > 0 {
			wt = fmt.Sprintf("≈%.2f", c.Watts)
		}
		add(" %-24s %5d %7.1f%% %9.0f %8s", trunc(c.Name, 24), c.Procs, c.CPUPct, c.Wakeups, wt)
	}
	return o
}
