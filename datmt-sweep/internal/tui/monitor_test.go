package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/datmt/datmt-sweep/internal/power"
)

func TestMonitorTab(t *testing.T) {
	m := New(context.Background(), nil)
	m.Monitor = power.NewMonitor()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.tabs() != 1 {
		t.Fatalf("tabs = %d", m.tabs())
	}
	// "m" jumps to the monitor; list keys must not panic there.
	for _, k := range []string{"m", "j", " ", "a", "enter", "D", "i", "s", "/"} {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
	m.mode = modeList
	if !m.onMonitor() {
		t.Fatal("not on monitor")
	}
	r := power.Reading{
		Valid: true, At: time.Now(), Budget: "battery", CPUPct: 12,
		Supply: power.Supply{OK: true, Discharging: true, Status: "Discharging", Percent: 49, Watts: 9.1,
			NowWh: 39.6, FullWh: 81, DesignWh: 85, Cycles: 113},
		Top: []power.Consumer{{Name: "firefox", Procs: 9, CPUPct: 31.5, Wakeups: 420, Watts: 2.4}},
	}
	for i := 0; i < 3; i++ {
		m.Update(monMsg{r})
	}
	v := m.View()
	for _, want := range []string{"Monitor", "discharging at 9.1 W", "firefox", "≈2.40", "left", "of design capacity"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if !m.monPause {
		t.Error("p did not pause")
	}
	// tab wraps back (single tab here) without panicking
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
}

func TestSpark(t *testing.T) {
	if got := spark([]float64{0, 5, 10}, 10); got != "▁▄█" {
		t.Errorf("spark = %q", got)
	}
	if got := spark([]float64{1, 2, 3, 4}, 2); len([]rune(got)) != 2 {
		t.Errorf("width not honoured: %q", got)
	}
}
