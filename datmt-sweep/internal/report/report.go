// Package report runs scanners to completion and renders the result as JSON,
// for scripts and the --json flag. Read-only: nothing here mutates the system.
package report

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/safety"
)

// Item is the JSON shape of core.Item.
type Item struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Desc    string   `json:"desc,omitempty"`
	Size    int64    `json:"size_bytes"`
	Metric  string   `json:"metric,omitempty"`
	Risk    string   `json:"risk"`
	Tags    []string `json:"tags,omitempty"`
	Date    string   `json:"date,omitempty"`
	Ignored bool     `json:"ignored,omitempty"`
}

// Category is one scanned sidebar entry.
type Category struct {
	Name       string `json:"name"`
	Items      []Item `json:"items"`
	TotalBytes int64  `json:"total_bytes"`
	Error      string `json:"error,omitempty"`
}

// Report is the whole scan.
type Report struct {
	Generated  time.Time  `json:"generated"`
	Categories []Category `json:"categories"`
}

// Collect scans every category concurrently and returns them in input order.
// Items matching ign (nil ok) are kept but flagged and left out of the
// total. Items are sorted by size, descending (Metric-only items too: Size
// still orders them).
func Collect(ctx context.Context, cats []core.Category, ign *safety.Ignore) Report {
	out := make([]Category, len(cats))
	var wg sync.WaitGroup
	for i, c := range cats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = scan(ctx, c, ign)
		}()
	}
	wg.Wait()
	return Report{Generated: time.Now().UTC(), Categories: out}
}

func scan(ctx context.Context, c core.Category, ign *safety.Ignore) Category {
	res := Category{Name: c.Name(), Items: []Item{}}
	ch := make(chan core.Item)
	errc := make(chan error, 1)
	go func() {
		errc <- c.Scanner().Scan(ctx, ch)
		close(ch)
	}()
	for it := range ch {
		ri := Item{
			ID: it.ID, Title: it.Title, Desc: it.Desc, Size: it.Size,
			Metric: it.Metric, Risk: it.Risk.String(), Tags: it.Tags, Date: it.Date,
			Ignored: ign != nil && ign.Match(c.Name(), it.ID),
		}
		if !ri.Ignored && it.Metric == "" {
			res.TotalBytes += it.Size
		}
		res.Items = append(res.Items, ri)
	}
	if err := <-errc; err != nil {
		res.Error = err.Error()
	}
	sort.SliceStable(res.Items, func(a, b int) bool { return res.Items[a].Size > res.Items[b].Size })
	return res
}

// Write renders r as indented JSON.
func Write(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
