package power

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Stat is cumulative activity of one cgroup at a point in time.
type Stat struct {
	CPUUsec int64 // cpu.stat usage_usec
	Ctx     int64 // voluntary+nonvoluntary context switches of member pids
	IOBytes int64 // io.stat rbytes+wbytes
	Procs   int
}

// Snapshot maps cgroup path relative to root (e.g. "system.slice/foo.service").
type Snapshot map[string]Stat

// Delta is activity over a sampling window.
type Delta struct {
	CPUUsec int64
	Ctx     int64
	IOBytes int64
	Procs   int
}

// Score is a unitless activity proxy in CPU-microsecond equivalents: each
// context switch (wakeup) is charged wakeupCost. Not measured power.
const wakeupCostUsec = 50

func (d Delta) Score() int64 { return d.CPUUsec + d.Ctx*wakeupCostUsec }

// SnapshotUnits walks the cgroup v2 tree under root and records every
// cgroup whose name ends in suffix (".service"). Unreadable cgroups are
// skipped silently; this is read-only and unprivileged.
func SnapshotUnits(root, suffix string) Snapshot {
	snap := Snapshot{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), suffix) || strings.HasPrefix(d.Name(), "user@") {
			return nil // user@UID.service wraps the whole user manager: descend
		}
		rel, _ := filepath.Rel(root, p)
		snap[rel] = readCgroup(p)
		return fs.SkipDir // nested cgroups are rolled into the unit by cpu.stat
	})
	return snap
}

func readCgroup(dir string) Stat {
	var s Stat
	if f, err := os.Open(filepath.Join(dir, "cpu.stat")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == "usage_usec" {
				s.CPUUsec, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		f.Close()
	}
	if f, err := os.Open(filepath.Join(dir, "io.stat")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			for _, tok := range strings.Fields(sc.Text()) {
				k, v, ok := strings.Cut(tok, "=")
				if ok && (k == "rbytes" || k == "wbytes") {
					n, _ := strconv.ParseInt(v, 10, 64)
					s.IOBytes += n
				}
			}
		}
		f.Close()
	}
	// pids of this cgroup and any child cgroups
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(p, "cgroup.procs"))
		if err != nil {
			return nil
		}
		for _, pid := range strings.Fields(string(b)) {
			s.Procs++
			s.Ctx += procCtx(pid, "/proc")
		}
		return nil
	})
	return s
}

func procCtx(pid, procRoot string) int64 {
	f, err := os.Open(filepath.Join(procRoot, pid, "status"))
	if err != nil {
		return 0
	}
	defer f.Close()
	var n int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.HasSuffix(k, "ctxt_switches") {
			x, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			n += x
		}
	}
	return n
}

// Diff returns per-cgroup deltas. Cgroups absent from before (started
// during the window) count from zero; cgroups gone by after are dropped.
func Diff(before, after Snapshot) map[string]Delta {
	out := map[string]Delta{}
	for k, a := range after {
		b := before[k]
		d := Delta{a.CPUUsec - b.CPUUsec, a.Ctx - b.Ctx, a.IOBytes - b.IOBytes, a.Procs}
		if d.CPUUsec < 0 || d.Ctx < 0 { // restarted mid-window
			d.CPUUsec, d.Ctx, d.IOBytes = max(d.CPUUsec, 0), max(d.Ctx, 0), max(d.IOBytes, 0)
		}
		out[k] = d
	}
	return out
}

// Sample snapshots, waits window, snapshots again.
func Sample(ctx context.Context, root, suffix string, window time.Duration) (map[string]Delta, error) {
	a := SnapshotUnits(root, suffix)
	select {
	case <-time.After(window):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return Diff(a, SnapshotUnits(root, suffix)), nil
}

// RootCPU returns total CPU usec used by everything (root cgroup cpu.stat).
func RootCPU(root string) int64 { return readCgroupCPU(root) }

func readCgroupCPU(dir string) int64 {
	f, err := os.Open(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == "usage_usec" {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}
