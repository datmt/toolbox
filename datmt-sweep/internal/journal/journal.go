// Package journal offers systemd-journal vacuum presets as a category.
package journal

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

type preset struct {
	id    string // flag form, e.g. "size=500M"
	title string
	bytes int64 // target size for size presets, 0 for time presets
}

var presets = []preset{
	{"size=1G", "Keep at most 1 GiB", 1 << 30},
	{"size=500M", "Keep at most 500 MiB", 500 << 20},
	{"size=200M", "Keep at most 200 MiB", 200 << 20},
	{"size=100M", "Keep at most 100 MiB", 100 << 20},
	{"time=2weeks", "Keep last 2 weeks", 0},
	{"time=1week", "Keep last week", 0},
}

var usageRE = regexp.MustCompile(`take up ([0-9.]+[KMGTP]?)`)

// ParseUsage reads `journalctl --disk-usage` output.
func ParseUsage(s string) int64 {
	m := usageRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	return core.ParseSize(m[1], false)
}

type Category struct{ r runner.Runner }

func New(r runner.Runner) *Category { return &Category{r: r} }

func (c *Category) Name() string          { return "Journal" }
func (c *Category) Scanner() core.Scanner { return c }

func (c *Category) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := c.r.Output(ctx, "journalctl", "--disk-usage")
	if err != nil {
		return err
	}
	usage := ParseUsage(string(raw))
	for _, p := range presets {
		it := core.Item{ID: p.id, Category: "Journal", Title: p.title, Risk: core.Review}
		if p.bytes > 0 {
			if usage <= p.bytes {
				continue // already under target: nothing to offer
			}
			it.Size = usage - p.bytes
			it.Desc = fmt.Sprintf("journal is %s; vacuum frees up to %s", core.HumanSize(usage), core.HumanSize(it.Size))
		} else {
			it.Desc = fmt.Sprintf("journal is %s; age-based, amount freed unknown", core.HumanSize(usage))
		}
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Category) Info(_ context.Context, it core.Item) string {
	return it.Title + "\n\n" + it.Desc +
		"\n\nOnly archived journal files are removed; the active file is kept.\nLogs older than the cut are gone for good."
}

func (c *Category) args(items []core.Item) []string {
	argv := []string{"journalctl"}
	// Several presets selected: one flag per kind is enough, take the strictest.
	var size, tm string
	var sizeB int64 = -1
	for _, it := range items {
		for _, p := range presets {
			if p.id != it.ID {
				continue
			}
			if p.bytes > 0 && (sizeB < 0 || p.bytes < sizeB) {
				sizeB, size = p.bytes, strings.TrimPrefix(p.id, "size=")
			}
			if p.bytes == 0 && (tm == "" || p.id == "time=1week") {
				tm = strings.TrimPrefix(p.id, "time=")
			}
		}
	}
	if size != "" {
		argv = append(argv, "--vacuum-size="+size)
	}
	if tm != "" {
		argv = append(argv, "--vacuum-time="+tm)
	}
	return argv
}

// DryRun: journalctl cannot preview a vacuum, so show the command and bound.
func (c *Category) DryRun(_ context.Context, items []core.Item) (string, error) {
	argv := c.args(items)
	if len(argv) == 1 {
		return "", fmt.Errorf("no valid preset selected")
	}
	var max int64
	for _, it := range items {
		if it.Size > max {
			max = it.Size
		}
	}
	return fmt.Sprintf("sudo %s\n\nfrees up to %s (journalctl has no dry run)\n", strings.Join(argv, " "), core.HumanSize(max)), nil
}

func (c *Category) Remove(items []core.Item) core.Removal {
	argv := c.args(items)
	return core.Removal{Summary: "sudo " + strings.Join(argv, " "), Sudo: argv}
}
