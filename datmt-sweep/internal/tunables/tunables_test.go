package tunables

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

func w(t *testing.T, p, s string) {
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindAndApply(t *testing.T) {
	sys, proc := t.TempDir(), t.TempDir()
	w(t, filepath.Join(sys, "module/pcie_aspm/parameters/policy"), "[default] performance powersave powersupersave\n")
	w(t, filepath.Join(sys, "bus/usb/devices/1-1/power/control"), "on\n")
	w(t, filepath.Join(sys, "bus/usb/devices/1-1/product"), "Mouse\n")
	w(t, filepath.Join(sys, "bus/usb/devices/usb1/power/control"), "on\n")
	w(t, filepath.Join(sys, "module/snd_hda_intel/parameters/power_save"), "1\n")
	w(t, filepath.Join(proc, "sys/kernel/nmi_watchdog"), "1\n")
	os.MkdirAll(filepath.Join(sys, "class/net/wlan0/wireless"), 0o755)
	r := &runner.Fake{Out: map[string]string{"iw dev wlan0 get power_save": "Power save: off\n"}}
	c := New(r)
	c.sysfs, c.proc = sys, proc

	ch := make(chan core.Item, 16)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var ids []string
	var items []core.Item
	for it := range ch {
		ids = append(ids, it.ID)
		items = append(items, it)
	}
	got := strings.Join(ids, ",")
	if got != "aspm,usb:1-1,wifi:wlan0,nmi" { // root hub + already-on audio excluded
		t.Fatalf("ids = %s", got)
	}
	rm := c.Remove(items)
	script := rm.Sudo[2]
	if !strings.Contains(script, "'iw' 'dev' 'wlan0' 'set' 'power_save' 'on'") || strings.Count(script, "&&") != 3 {
		t.Errorf("script = %s", script)
	}
	if _, err := c.DryRun(context.Background(), []core.Item{{ID: "bogus"}}); err == nil {
		t.Error("bogus accepted")
	}
}
