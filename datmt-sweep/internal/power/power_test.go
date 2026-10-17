package power

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBattery(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "BAT0/current_now"), "1000000\n")
	write(t, filepath.Join(d, "BAT0/voltage_now"), "12000000\n")
	write(t, filepath.Join(d, "BAT0/status"), "Discharging\n")
	b := ReadBattery(d)
	if !b.OK || !b.Discharging || b.Watts < 11.99 || b.Watts > 12.01 {
		t.Fatalf("%+v", b)
	}
	if ReadBattery(t.TempDir()).OK {
		t.Error("no battery reported OK")
	}
}

func TestSnapshotDiff(t *testing.T) {
	root := t.TempDir()
	u := filepath.Join(root, "system.slice/foo.service")
	write(t, filepath.Join(u, "cpu.stat"), "usage_usec 100\nuser_usec 60\n")
	write(t, filepath.Join(u, "io.stat"), "8:0 rbytes=10 wbytes=5 rios=1\n")
	write(t, filepath.Join(u, "cgroup.procs"), "")
	write(t, filepath.Join(root, "system.slice/other.scope/cpu.stat"), "usage_usec 9\n")
	a := SnapshotUnits(root, ".service")
	if len(a) != 1 || a["system.slice/foo.service"].CPUUsec != 100 || a["system.slice/foo.service"].IOBytes != 15 {
		t.Fatalf("%+v", a)
	}
	write(t, filepath.Join(u, "cpu.stat"), "usage_usec 350\n")
	d := Diff(a, SnapshotUnits(root, ".service"))["system.slice/foo.service"]
	if d.CPUUsec != 250 || d.Score() != 250 {
		t.Fatalf("%+v", d)
	}
}
