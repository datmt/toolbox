// Package tunables offers kernel/device power-saving settings that are
// currently at a power-hungry value.
package tunables

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// tunable is one setting. cmd is the fixed argv applied via sudo.
type tunable struct {
	id, title, cur, want, why string
	cmd                       []string
}

type Category struct {
	r     runner.Runner
	sysfs string // "/sys"
	proc  string // "/proc"
}

func New(r runner.Runner) *Category {
	return &Category{r: r, sysfs: "/sys", proc: "/proc"}
}

func (c *Category) Name() string          { return "Tunables" }
func (c *Category) Scanner() core.Scanner { return c }

func readTrim(p string) (string, bool) {
	b, err := os.ReadFile(p)
	return strings.TrimSpace(string(b)), err == nil
}

func sh(path, val string) []string {
	return []string{"sh", "-c", fmt.Sprintf("echo %s > %s", val, path)}
}

func (c *Category) find(ctx context.Context) []tunable {
	var ts []tunable

	// PCIe ASPM: policy file looks like "[default] performance powersave powersupersave".
	if p := filepath.Join(c.sysfs, "module/pcie_aspm/parameters/policy"); true {
		if s, ok := readTrim(p); ok {
			cur := ""
			if i, j := strings.IndexByte(s, '['), strings.IndexByte(s, ']'); i >= 0 && j > i {
				cur = s[i+1 : j]
			}
			if cur != "" && cur != "powersave" && cur != "powersupersave" && strings.Contains(s, "powersave") {
				ts = append(ts, tunable{"aspm", "PCIe ASPM policy", cur, "powersave",
					"link power management for PCIe devices; may add latency", sh(p, "powersave")})
			}
		}
	}

	// USB autosuspend per device.
	devs, _ := filepath.Glob(filepath.Join(c.sysfs, "bus/usb/devices/*/power/control"))
	for _, ctl := range devs {
		if v, _ := readTrim(ctl); v != "on" {
			continue
		}
		dir := filepath.Dir(filepath.Dir(ctl))
		// skip root hubs (usbN): autosuspend there is handled by the kernel
		if strings.HasPrefix(filepath.Base(dir), "usb") {
			continue
		}
		name, _ := readTrim(filepath.Join(dir, "product"))
		if name == "" {
			name = filepath.Base(dir)
		}
		ts = append(ts, tunable{"usb:" + filepath.Base(dir), "USB autosuspend: " + name, "on", "auto",
			"device never suspends; input devices may lag on wake", sh(ctl, "auto")})
	}

	// Wi-Fi power save via iw.
	nets, _ := filepath.Glob(filepath.Join(c.sysfs, "class/net/*/wireless"))
	for _, w := range nets {
		ifc := filepath.Base(filepath.Dir(w))
		out, err := c.r.Output(ctx, "iw", "dev", ifc, "get", "power_save")
		if err == nil && strings.Contains(string(out), "Power save: off") {
			ts = append(ts, tunable{"wifi:" + ifc, "Wi-Fi power save: " + ifc, "off", "on",
				"radio sleeps between beacons; slightly higher latency", []string{"iw", "dev", ifc, "set", "power_save", "on"}})
		}
	}

	// Audio codec idle power-down.
	if p := filepath.Join(c.sysfs, "module/snd_hda_intel/parameters/power_save"); true {
		if v, ok := readTrim(p); ok && v == "0" {
			ts = append(ts, tunable{"audio", "HDA audio power_save", "0", "1",
				"codec powers down when idle; may click on resume", sh(p, "1")})
		}
	}

	// SATA link power management.
	hosts, _ := filepath.Glob(filepath.Join(c.sysfs, "class/scsi_host/host*/link_power_management_policy"))
	for _, p := range hosts {
		if v, _ := readTrim(p); v == "max_performance" {
			ts = append(ts, tunable{"sata:" + filepath.Base(filepath.Dir(p)), "SATA link power: " + filepath.Base(filepath.Dir(p)),
				v, "med_power_with_dipm", "drive link idles to a low-power state", sh(p, "med_power_with_dipm")})
		}
	}

	// NMI watchdog: periodic wakeups on every CPU.
	if p := filepath.Join(c.proc, "sys/kernel/nmi_watchdog"); true {
		if v, ok := readTrim(p); ok && v == "1" {
			ts = append(ts, tunable{"nmi", "NMI watchdog", "1", "0",
				"removes a periodic per-CPU wakeup; loses hard-lockup detection", sh(p, "0")})
		}
	}
	return ts
}

func (c *Category) Scan(ctx context.Context, out chan<- core.Item) error {
	for _, t := range c.find(ctx) {
		it := core.Item{
			ID: t.id, Category: "Tunables", Title: t.title, Risk: core.Review,
			Metric: t.cur + " → " + t.want, Tags: []string{"RUNTIME"},
			Desc: fmt.Sprintf("%s\nnow %q, set %q\n%s\n\nRuntime only: reverts on reboot.", t.title, t.cur, t.want, t.why),
		}
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Category) Info(_ context.Context, it core.Item) string { return it.Desc }

// argv for the selected items, rescanned so only current, known settings run.
func (c *Category) script(ctx context.Context, items []core.Item) (string, []tunable) {
	want := map[string]bool{}
	for _, it := range items {
		want[it.ID] = true
	}
	var sel []tunable
	var parts []string
	for _, t := range c.find(ctx) {
		if want[t.id] {
			sel = append(sel, t)
			parts = append(parts, shellJoin(t.cmd))
		}
	}
	return strings.Join(parts, " && "), sel
}

func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func (c *Category) DryRun(ctx context.Context, items []core.Item) (string, error) {
	_, sel := c.script(ctx, items)
	if len(sel) == 0 {
		return "", fmt.Errorf("selected tunables no longer apply")
	}
	var b strings.Builder
	for _, t := range sel {
		fmt.Fprintf(&b, "sudo %s\n    %s: %s -> %s (reverts on reboot)\n", strings.Join(t.cmd, " "), t.title, t.cur, t.want)
	}
	return b.String(), nil
}

func (c *Category) Remove(items []core.Item) core.Removal {
	script, _ := c.script(context.Background(), items)
	return core.Removal{
		Summary: fmt.Sprintf("sudo sh -c (%d tunables)", len(items)),
		Sudo:    []string{"sh", "-c", script},
	}
}
