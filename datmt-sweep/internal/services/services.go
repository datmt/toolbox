// Package services ranks running systemd services by recent activity
// (CPU time + wakeups) and offers to stop them.
package services

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/power"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// protected units are never bulk-selected (Risk Danger).
var protected = regexp.MustCompile(`^(dbus(-broker)?|systemd-.*|sshd?|NetworkManager|wpa_supplicant|iwd|polkit|udisks2|upower|display-manager|gdm|sddm|lightdm|greetd|seatd|getty@.*|serial-getty@.*|user@.*|user-runtime-dir@.*|accounts-daemon|rtkit-daemon|bluetooth|login|elogind|org\.gnome\.Shell@.*|plasma-.*|gnome-session.*)\.service$`)

type Category struct {
	r       runner.Runner
	cgroup  string        // cgroup v2 mount
	supply  string        // power_supply dir
	window  time.Duration // sampling window
	minUsec int64         // ignore units with less CPU+wakeup score
}

func New(r runner.Runner) *Category {
	return &Category{r: r, cgroup: "/sys/fs/cgroup", supply: "/sys/class/power_supply",
		window: 3 * time.Second, minUsec: 1000}
}

func (c *Category) Name() string          { return "Services" }
func (c *Category) Scanner() core.Scanner { return c }

// parseID splits "system:foo.service" / "user:foo.service".
func parseID(id string) (user bool, unit string) {
	scope, unit, _ := strings.Cut(id, ":")
	return scope == "user", unit
}

func (c *Category) Scan(ctx context.Context, out chan<- core.Item) error {
	rootBefore := power.RootCPU(c.cgroup)
	deltas, err := power.Sample(ctx, c.cgroup, ".service", c.window)
	if err != nil {
		return err
	}
	rootDelta := power.RootCPU(c.cgroup) - rootBefore
	bat := power.ReadBattery(c.supply)

	type row struct {
		path string
		d    power.Delta
	}
	var rows []row
	for p, d := range deltas {
		if d.Score() >= c.minUsec {
			rows = append(rows, row{p, d})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].d.Score() > rows[j].d.Score() })

	secs := c.window.Seconds()
	for _, r := range rows {
		unit := r.path[strings.LastIndex(r.path, "/")+1:]
		user := strings.Contains(r.path, "/user@")
		scope := "system"
		if user {
			scope = "user"
		}
		it := core.Item{
			ID:       scope + ":" + unit,
			Category: "Services",
			Title:    unit,
			Size:     r.d.Score(),
			Risk:     core.Review,
			Tags:     []string{strings.ToUpper(scope)},
		}
		if protected.MatchString(unit) {
			it.Risk = core.Danger
			it.Tags = append(it.Tags, "PROTECTED")
		}
		cpuPct := float64(r.d.CPUUsec) / (secs * 1e6) * 100
		wakes := float64(r.d.Ctx) / secs
		it.Metric = fmt.Sprintf("%.1f%% cpu", cpuPct)
		if bat.OK && bat.Discharging && rootDelta > 0 {
			w := bat.Watts * float64(r.d.CPUUsec) / float64(rootDelta)
			it.Metric = fmt.Sprintf("≈%.2f W", w)
		}
		it.Desc = fmt.Sprintf("%s · %.1f%% cpu · %.0f wakeups/s · %s/s io · %d procs",
			r.path, cpuPct, wakes, core.HumanSize(int64(float64(r.d.IOBytes)/secs)), r.d.Procs)
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Category) Info(ctx context.Context, it core.Item) string {
	user, unit := parseID(it.ID)
	args := []string{"systemctl"}
	if user {
		args = append(args, "--user")
	}
	args = append(args, "show", "-p", "Description", "-p", "MainPID", "-p", "ActiveEnterTimestamp", "-p", "UnitFileState", unit)
	b, _ := c.r.Output(ctx, args...)
	s := it.Title + "\n\n" + it.Desc + "\n\n" + strings.TrimSpace(string(b))
	switch {
	case c.watts(): // battery-based estimate
		s += "\n\nWatts are an estimate: battery draw split by this unit's share of CPU time.\nIdle hardware and GPU draw are not attributed."
	default:
		s += "\n\nNo battery discharge data (on AC or desktop): ranked by CPU/wakeups only."
	}
	if it.Risk == core.Danger {
		s += "\n\nPROTECTED: system depends on this unit; not bulk-selected."
	}
	return s
}

func (c *Category) watts() bool {
	b := power.ReadBattery(c.supply)
	return b.OK && b.Discharging
}

func (c *Category) split(items []core.Item) (sys, usr []string) {
	for _, it := range items {
		user, u := parseID(it.ID)
		if user {
			usr = append(usr, u)
		} else {
			sys = append(sys, u)
		}
	}
	return
}

// DryRun: systemctl has no dry run for stop, so show command + reverse deps.
func (c *Category) DryRun(ctx context.Context, items []core.Item) (string, error) {
	sys, usr := c.split(items)
	if len(sys)+len(usr) == 0 {
		return "", fmt.Errorf("nothing selected")
	}
	var b strings.Builder
	show := func(user bool, units []string) {
		for _, u := range units {
			args := []string{"systemctl"}
			if user {
				args = append(args, "--user")
			}
			args = append(args, "list-dependencies", "--reverse", "--plain", "--no-pager", u)
			out, _ := c.r.Output(ctx, args...)
			var deps []string
			for _, l := range strings.Split(string(out), "\n")[1:] {
				if l = strings.TrimSpace(l); l != "" {
					deps = append(deps, l)
				}
			}
			fmt.Fprintf(&b, "  %s", u)
			if len(deps) > 0 {
				fmt.Fprintf(&b, "  (needed by: %s)", strings.Join(deps, ", "))
			}
			b.WriteString("\n")
		}
	}
	if len(sys) > 0 {
		fmt.Fprintf(&b, "sudo systemctl stop %s\n", strings.Join(sys, " "))
		show(false, sys)
	}
	if len(usr) > 0 {
		fmt.Fprintf(&b, "systemctl --user stop %s\n", strings.Join(usr, " "))
		show(true, usr)
	}
	b.WriteString("\nstop only: units are not disabled and may restart on socket/dbus activation or next boot.\n")
	if len(sys) > 0 && len(usr) > 0 {
		b.WriteString("mixed system+user selection: only user units run now; select system units separately.\n")
	}
	return b.String(), nil
}

func (c *Category) Remove(items []core.Item) core.Removal {
	sys, usr := c.split(items)
	if len(usr) == 0 {
		return core.Removal{
			Summary: "sudo systemctl stop " + strings.Join(sys, " "),
			Sudo:    append([]string{"systemctl", "stop"}, sys...),
		}
	}
	return core.Removal{
		Summary: "systemctl --user stop " + strings.Join(usr, " "),
		Do: func(ctx context.Context) core.Result {
			res := core.Result{Failed: map[string]error{}}
			for _, u := range usr {
				id := "user:" + u
				if _, err := c.r.Output(ctx, "systemctl", "--user", "stop", u); err != nil {
					res.Failed[id] = err
				} else {
					res.Done = append(res.Done, id)
				}
			}
			for _, u := range sys {
				res.Failed["system:"+u] = fmt.Errorf("mixed selection: run system units separately (needs sudo)")
			}
			return res
		},
	}
}

// Protected reports whether a unit name is one the system depends on.
func Protected(unit string) bool { return protected.MatchString(unit) }
