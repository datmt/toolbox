package journal

import (
	"context"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

func TestScanAndRemove(t *testing.T) {
	f := &runner.Fake{Out: map[string]string{
		"journalctl --disk-usage": "Archived and active journals take up 600.0M in the file system.\n",
	}}
	c := New(f)
	ch := make(chan core.Item, 16)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	// 600M: 1G target skipped; 500/200/100 + two time presets offered.
	if len(items) != 5 || items[0].ID != "size=500M" || items[0].Size != 100<<20 {
		t.Fatalf("items = %+v", items)
	}
	rm := c.Remove([]core.Item{items[0], items[1], {ID: "time=2weeks"}, {ID: "time=1week"}})
	want := "journalctl --vacuum-size=200M --vacuum-time=1week"
	if got := strings.Join(rm.Sudo, " "); got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if _, err := c.DryRun(context.Background(), []core.Item{{ID: "bogus"}}); err == nil {
		t.Error("bogus preset accepted")
	}
}
