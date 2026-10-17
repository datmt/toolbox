// Package core holds the types shared by scanners, actions and the TUI.
package core

import (
	"context"
	"fmt"
)

// Risk grades how careful the user should be with an item.
type Risk int

const (
	Safe Risk = iota
	Review
	Danger
)

func (r Risk) String() string {
	switch r {
	case Safe:
		return "safe"
	case Review:
		return "review"
	default:
		return "danger"
	}
}

// Item is one thing that can be cleaned.
type Item struct {
	ID       string // unique within a category (package name, path, image id)
	Category string
	Title    string
	Desc     string
	Size     int64 // bytes reclaimed if removed
	Risk     Risk
	Tags     []string // ORPHAN, LEAF, AUR ...
	Date     string
	// Metric, if set, replaces the size column (e.g. "≈0.8 W"). Size then
	// only orders items and is not a byte count.
	Metric string
}

// Scanner is read-only and unprivileged. It streams items as it finds them.
type Scanner interface {
	Name() string
	Scan(ctx context.Context, out chan<- Item) error
}

// Plan is a privileged batch built from selected items.
type Plan struct {
	Items []Item
	// Cmd is the command, without sudo, that performs the batch.
	Cmd []string
}

// HumanSize formats bytes as KiB/MiB/GiB.
func HumanSize(b int64) string {
	const k = 1024
	f := float64(b)
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1f GiB", f/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1f MiB", f/(k*k))
	case b >= k:
		return fmt.Sprintf("%.1f KiB", f/k)
	}
	return fmt.Sprintf("%d B", b)
}

// Result reports what an in-process Removal managed to do.
type Result struct {
	Done   []string         // item IDs removed
	Failed map[string]error // item ID -> reason
}

// Removal is how a batch gets executed. Exactly one of Sudo or Do is set.
type Removal struct {
	Summary string // one line shown on the confirm screen
	// Sudo is a command run through sudo with the terminal attached.
	Sudo []string
	// Do runs in-process, unprivileged (trash moves, cache deletes).
	Do func(ctx context.Context) Result
}

// Category is one sidebar entry: how to scan, preview and remove its items.
// Only Remove's result mutates anything; the rest is read-only.
type Category interface {
	Name() string
	Scanner() Scanner
	// Info is the preview pane text for one item.
	Info(ctx context.Context, it Item) string
	// DryRun says what removing items would do.
	DryRun(ctx context.Context, items []Item) (string, error)
	Remove(items []Item) Removal
}

// PermanentRemover is implemented by categories whose normal removal is
// reversible (trash) but that can also delete for good, which is the only
// way to actually reclaim the space.
type PermanentRemover interface {
	RemovePermanent(items []Item) Removal
	DryRunPermanent(ctx context.Context, items []Item) (string, error)
}
