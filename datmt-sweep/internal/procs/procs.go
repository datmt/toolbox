// Package procs ranks non-service processes by recent CPU/wakeup activity
// and offers to terminate them.
package procs

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/power"
)

type Category struct {
	proc, cgroup, supply string
	window               time.Duration
	minUsec              int64
	uid                  uint32
	kill                 func(pid int, sig syscall.Signal) error
	self                 int
}

func New() *Category {
	return &Category{proc: "/proc", cgroup: "/sys/fs/cgroup", supply: "/sys/class/power_supply",
		window: 3 * time.Second, minUsec: 5000, uid: uint32(os.Getuid()), self: os.Getpid(),
		kill: func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }}
}

func (c *Category) Name() string          { return "Processes" }
func (c *Category) Scanner() core.Scanner { return c }

func (c *Category) Scan(ctx context.Context, out chan<- core.Item) error {
	rootBefore := power.RootCPU(c.cgroup)
	ds, err := power.SampleProcs(ctx, c.proc, c.window)
	if err != nil {
		return err
	}
	rootDelta := power.RootCPU(c.cgroup) - rootBefore
	bat := power.ReadBattery(c.supply)

	var rows []power.ProcDelta
	for _, d := range ds {
		// services are covered by the Services category
		if d.Kernel || d.PID == c.self || d.PID == 1 || strings.Contains(d.Cgroup, ".service") {
			continue
		}
		if d.Score() >= c.minUsec {
			rows = append(rows, d)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score() > rows[j].Score() })

	secs := c.window.Seconds()
	for _, d := range rows {
		it := core.Item{
			ID:       strconv.Itoa(d.PID),
			Category: "Processes",
			Title:    fmt.Sprintf("%s (%d)", d.Comm, d.PID),
			Size:     d.Score(),
			Risk:     core.Review,
		}
		own := d.UID == c.uid
		if own {
			it.Tags = []string{"OWN"}
		} else {
			it.Tags = []string{"UID" + strconv.Itoa(int(d.UID))}
		}
		cpuPct := float64(d.DCPU) / (secs * 1e6) * 100
		it.Metric = fmt.Sprintf("%.1f%% cpu", cpuPct)
		if bat.OK && bat.Discharging && rootDelta > 0 {
			it.Metric = fmt.Sprintf("≈%.2f W", bat.Watts*float64(d.DCPU)/float64(rootDelta))
		}
		it.Desc = fmt.Sprintf("%s · %.1f%% cpu · %.0f wakeups/s · %s", d.Cmdline, cpuPct, float64(d.DCtx)/secs, d.Cgroup)
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Category) Info(_ context.Context, it core.Item) string {
	s := it.Title + "\n\n" + it.Desc + "\n\nSIGTERM is sent; unsaved work in the program is lost."
	if !c.owned(it) {
		s += "\nNot your process: needs sudo."
	}
	return s
}

func (c *Category) owned(it core.Item) bool { return len(it.Tags) > 0 && it.Tags[0] == "OWN" }

func (c *Category) split(items []core.Item) (own, other []int) {
	for _, it := range items {
		pid, err := strconv.Atoi(it.ID)
		if err != nil || pid <= 1 {
			continue
		}
		if c.owned(it) {
			own = append(own, pid)
		} else {
			other = append(other, pid)
		}
	}
	return
}

func pidStrs(p []int) []string {
	s := make([]string, len(p))
	for i, v := range p {
		s[i] = strconv.Itoa(v)
	}
	return s
}

// DryRun lists what gets signalled; kill has no real preview.
func (c *Category) DryRun(_ context.Context, items []core.Item) (string, error) {
	own, other := c.split(items)
	if len(own)+len(other) == 0 {
		return "", fmt.Errorf("nothing selected")
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "  SIGTERM %-24s %s\n", it.Title, it.Metric)
	}
	if len(other) > 0 {
		fmt.Fprintf(&b, "\nsudo kill -TERM %s\n", strings.Join(pidStrs(other), " "))
		if len(own) > 0 {
			b.WriteString("mixed own+other selection: only your own processes run now.\n")
		}
	}
	return b.String(), nil
}

func (c *Category) Remove(items []core.Item) core.Removal {
	own, other := c.split(items)
	if len(own) == 0 && len(other) > 0 {
		return core.Removal{
			Summary: "sudo kill -TERM " + strings.Join(pidStrs(other), " "),
			Sudo:    append([]string{"kill", "-TERM"}, pidStrs(other)...),
		}
	}
	return core.Removal{
		Summary: "SIGTERM " + strings.Join(pidStrs(own), " "),
		Do: func(context.Context) core.Result {
			res := core.Result{Failed: map[string]error{}}
			for _, pid := range own {
				id := strconv.Itoa(pid)
				if err := c.kill(pid, syscall.SIGTERM); err != nil {
					res.Failed[id] = err
				} else {
					res.Done = append(res.Done, id)
				}
			}
			for _, pid := range other {
				res.Failed[strconv.Itoa(pid)] = fmt.Errorf("mixed selection: select other users' processes separately (needs sudo)")
			}
			return res
		},
	}
}
