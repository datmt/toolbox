package power

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func wr(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadSupply(t *testing.T) {
	d := t.TempDir()
	wr(t, d, "BAT0/status", "Discharging\n")
	wr(t, d, "BAT0/capacity", "50\n")
	wr(t, d, "BAT0/power_now", "10000000\n")
	wr(t, d, "BAT0/energy_now", "40000000\n")
	wr(t, d, "BAT0/energy_full", "80000000\n")
	wr(t, d, "BAT0/energy_full_design", "100000000\n")
	wr(t, d, "AC/type", "Mains\n")
	wr(t, d, "AC/online", "0\n")
	s := ReadSupply(d)
	if !s.OK || s.Watts != 10 || s.Percent != 50 || s.AC {
		t.Fatalf("%+v", s)
	}
	if rem, ok := s.Remaining(); !ok || rem != 4*time.Hour {
		t.Errorf("remaining = %v %v", rem, ok)
	}
	if h := s.Health(); h != 80 {
		t.Errorf("health = %v", h)
	}
}

func TestChargeBasedBatteryAndAC(t *testing.T) {
	d := t.TempDir()
	wr(t, d, "BAT0/status", "Charging\n")
	wr(t, d, "BAT0/current_now", "2000000\n")
	wr(t, d, "BAT0/voltage_now", "10000000\n")
	wr(t, d, "BAT0/charge_now", "1000000\n")
	wr(t, d, "BAT0/charge_full", "4000000\n")
	wr(t, d, "ADP1/type", "Mains\n")
	wr(t, d, "ADP1/online", "1\n")
	s := ReadSupply(d)
	if s.Watts != 20 || s.NowWh != 10 || s.FullWh != 40 || !s.AC {
		t.Fatalf("%+v", s)
	}
	if rem, ok := s.Remaining(); !ok || rem != 90*time.Minute {
		t.Errorf("time to full = %v %v", rem, ok)
	}
}

func TestMonitorAttributesRAPLAndSkipsFirstSample(t *testing.T) {
	root := t.TempDir()
	sup, proc, cap := filepath.Join(root, "ps"), filepath.Join(root, "proc"), filepath.Join(root, "cap")
	os.MkdirAll(sup, 0o755)
	stat := func(pid, comm string, ticks int) string {
		return pid + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 " + strconv.Itoa(ticks) + " 0 0 0 20 0 1 0 0 0 0"
	}
	setProc := func(aTicks, bTicks int, energy string, cpu string) {
		wr(t, proc, "100/stat", stat("100", "heavy", aTicks))
		wr(t, proc, "100/cmdline", "heavy\x00")
		wr(t, proc, "200/stat", stat("200", "light", bTicks))
		wr(t, proc, "200/cmdline", "light\x00")
		wr(t, proc, "stat", cpu)
		wr(t, cap, "intel-rapl:0/energy_uj", energy)
		wr(t, cap, "intel-rapl:0/max_energy_range_uj", "1000000000")
		wr(t, cap, "intel-rapl:0:0/energy_uj", "999") // subdomain ignored
	}
	clock := time.Unix(1000, 0)
	m := &Monitor{Roots: Roots{sup, proc, cap}, TopN: 5, now: func() time.Time { return clock }}

	setProc(0, 0, "0\n", "cpu 0 0 0 0 0 0 0 0\n")
	if r := m.Sample(); r.Valid || len(r.Top) != 0 {
		t.Fatalf("first sample = %+v", r)
	}
	clock = clock.Add(2 * time.Second)
	// heavy: 150 ticks = 1.5 s cpu in 2 s; light: 50 ticks. 10 J over 2 s = 5 W.
	setProc(150, 50, "10000000\n", "cpu 100 0 100 600 0 0 0 0\n")
	r := m.Sample()
	if !r.Valid || !r.PkgOK || r.Budget != "rapl" {
		t.Fatalf("%+v", r)
	}
	if r.PkgWatts < 4.99 || r.PkgWatts > 5.01 {
		t.Errorf("pkg watts = %v", r.PkgWatts)
	}
	if len(r.Top) != 2 || r.Top[0].Name != "heavy" {
		t.Fatalf("top = %+v", r.Top)
	}
	if w := r.Top[0].Watts; w < 3.74 || w > 3.76 {
		t.Errorf("heavy watts = %v, want 3.75", w)
	}
	if r.CPUPct < 24.9 || r.CPUPct > 25.1 {
		t.Errorf("cpu%% = %v", r.CPUPct)
	}
}

func TestRAPLWrap(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(0, 0)
	m := &Monitor{Roots: Roots{filepath.Join(root, "s"), filepath.Join(root, "p"), filepath.Join(root, "c")}, now: func() time.Time { return clock }}
	wr(t, root, "p/stat", "cpu 0 0 0 0\n")
	wr(t, root, "c/intel-rapl:0/max_energy_range_uj", "1000\n")
	wr(t, root, "c/intel-rapl:0/energy_uj", "900\n")
	m.Sample()
	clock = clock.Add(time.Second)
	wr(t, root, "c/intel-rapl:0/energy_uj", "100\n") // wrapped: 200 uJ elapsed
	r := m.Sample()
	if !r.PkgOK || r.PkgWatts < 0.0001999 || r.PkgWatts > 0.0002001 {
		t.Errorf("wrap: %+v", r)
	}
}
