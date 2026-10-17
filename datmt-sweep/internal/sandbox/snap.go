package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// snapSkip: snapd itself and base snaps other snaps are built on.
var snapSkip = regexp.MustCompile(`^(snapd|core|core[0-9]+|bare)$`)

// SnapRev is one row of `snap list --all`.
type SnapRev struct {
	Name, Version, Rev, Notes string
	Disabled                  bool
}

// ParseSnapList parses `snap list --all` (Name Version Rev Tracking Publisher Notes).
func ParseSnapList(raw string) []SnapRev {
	var out []SnapRev
	for i, l := range strings.Split(raw, "\n") {
		f := strings.Fields(l)
		if i == 0 || len(f) < 4 { // header
			continue
		}
		s := SnapRev{Name: f[0], Version: f[1], Rev: f[2]}
		s.Notes = f[len(f)-1]
		s.Disabled = strings.Contains(s.Notes, "disabled")
		out = append(out, s)
	}
	return out
}

type Snap struct {
	r       runner.Runner
	snapDir string
}

func NewSnap(r runner.Runner) *Snap { return &Snap{r: r, snapDir: "/var/lib/snapd/snaps"} }

func (c *Snap) Name() string          { return "Snap" }
func (c *Snap) Scanner() core.Scanner { return c }

func (c *Snap) size(s SnapRev) int64 {
	if st, err := os.Stat(filepath.Join(c.snapDir, s.Name+"_"+s.Rev+".snap")); err == nil {
		return st.Size()
	}
	return 0
}

// Scan offers disabled (old) revisions as Safe and enabled snaps as Review.
func (c *Snap) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := c.r.Output(ctx, "snap", "list", "--all")
	if err != nil {
		return err
	}
	var items []core.Item
	for _, s := range ParseSnapList(string(raw)) {
		if snapSkip.MatchString(s.Name) {
			continue
		}
		it := core.Item{Category: "Snap", Title: s.Name, Size: c.size(s)}
		if s.Disabled {
			it.ID, it.Risk = s.Name+" "+s.Rev, core.Safe
			it.Desc = fmt.Sprintf("old revision %s (%s), superseded", s.Rev, s.Version)
			it.Tags = []string{"OLD-REV"}
		} else {
			it.ID, it.Risk = s.Name+" -", core.Review
			it.Desc = fmt.Sprintf("%s rev %s; removal also drops all its revisions", s.Version, s.Rev)
			it.Tags = []string{"APP"}
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	for _, it := range items {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Snap) Info(ctx context.Context, it core.Item) string {
	name, _, _ := strings.Cut(it.ID, " ")
	out, err := c.r.Output(ctx, "snap", "info", name)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(out)
}

const snapScript = `set -e
while [ $# -gt 0 ]; do
  if [ "$2" = "-" ]; then snap remove "$1"; else snap remove "$1" --revision "$2"; fi
  shift 2
done`

// cmd builds one sudo command: revisions first, then whole snaps, so a
// selected app does not make its revision removals fail.
func (c *Snap) cmd(items []core.Item) []string {
	var revs, apps []string
	for _, it := range items {
		name, rev, _ := strings.Cut(it.ID, " ")
		if rev == "-" {
			apps = append(apps, name, rev)
		} else {
			revs = append(revs, name, rev)
		}
	}
	return append([]string{"sh", "-c", snapScript, "sh"}, append(revs, apps...)...)
}

func (c *Snap) DryRun(_ context.Context, items []core.Item) (string, error) {
	var b strings.Builder
	var total int64
	for _, it := range items {
		name, rev, _ := strings.Cut(it.ID, " ")
		total += it.Size
		if rev == "-" {
			fmt.Fprintf(&b, "sudo snap remove %s\n", name)
		} else {
			fmt.Fprintf(&b, "sudo snap remove %s --revision %s\n", name, rev)
		}
	}
	fmt.Fprintf(&b, "\nfrees about %s (snap has no dry run)\n", core.HumanSize(total))
	return b.String(), nil
}

func (c *Snap) Remove(items []core.Item) core.Removal {
	return core.Removal{Summary: fmt.Sprintf("sudo snap remove x%d", len(items)), Sudo: c.cmd(items)}
}
