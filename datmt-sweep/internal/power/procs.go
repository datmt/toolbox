package power

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const usecPerTick = 10000 // CLK_TCK is 100 on Linux

// Proc is cumulative activity of one process.
type Proc struct {
	PID     int
	Comm    string
	Cmdline string
	UID     uint32
	Cgroup  string // path from /proc/PID/cgroup, e.g. /system.slice/foo.service
	CPUUsec int64
	Ctx     int64
	Kernel  bool
}

// ProcDelta is a process's activity over a sampling window.
type ProcDelta struct {
	Proc
	DCPU, DCtx int64
}

func (d ProcDelta) Score() int64 { return d.DCPU + d.DCtx*wakeupCostUsec }

// SnapshotProcs reads every visible process under procRoot (/proc).
func SnapshotProcs(procRoot string) map[int]Proc {
	out := map[int]Proc{}
	ents, _ := os.ReadDir(procRoot)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		s := string(raw)
		lp, rp := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if lp < 0 || rp < lp {
			continue
		}
		f := strings.Fields(s[rp+1:])
		if len(f) < 13 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		ut, _ := strconv.ParseInt(f[11], 10, 64)
		st, _ := strconv.ParseInt(f[12], 10, 64)
		p := Proc{PID: pid, Comm: s[lp+1 : rp], CPUUsec: (ut + st) * usecPerTick, Kernel: pid == 2 || ppid == 2}
		if fi, err := os.Stat(dir); err == nil {
			if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
				p.UID = sys.Uid
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			p.Cmdline = strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
		}
		if p.Cmdline == "" {
			p.Kernel = true
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
			if _, v, ok := strings.Cut(strings.TrimSpace(string(b)), "::"); ok {
				p.Cgroup = v
			}
		}
		p.Ctx = procCtx(e.Name(), procRoot)
		out[pid] = p
	}
	return out
}

// SampleProcs diffs two snapshots window apart. A pid reused by a different
// comm counts from zero.
func SampleProcs(ctx context.Context, procRoot string, window time.Duration) ([]ProcDelta, error) {
	a := SnapshotProcs(procRoot)
	select {
	case <-time.After(window):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var out []ProcDelta
	for pid, p := range SnapshotProcs(procRoot) {
		d := ProcDelta{Proc: p, DCPU: p.CPUUsec, DCtx: p.Ctx}
		if b, ok := a[pid]; ok && b.Comm == p.Comm {
			d.DCPU, d.DCtx = max(p.CPUUsec-b.CPUUsec, 0), max(p.Ctx-b.Ctx, 0)
		} else {
			d.DCPU, d.DCtx = 0, 0 // appeared mid-window: no baseline
		}
		out = append(out, d)
	}
	return out, nil
}
