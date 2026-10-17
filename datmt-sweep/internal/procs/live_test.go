//go:build live

package procs

import (
	"context"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
	"github.com/datmt/datmt-sweep/internal/tunables"
)

// Read-only: samples /proc for a few seconds and reads sysfs.
func TestLive(t *testing.T) {
	for _, c := range []core.Category{New(), tunables.New(runner.System{})} {
		ch := make(chan core.Item, 512)
		if err := c.Scanner().Scan(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
		close(ch)
		n := 0
		for it := range ch {
			if n < 6 {
				t.Logf("%s: %-30s %-14s %v", c.Name(), it.Title, it.Metric, it.Tags)
			}
			n++
		}
		t.Logf("%s: %d items", c.Name(), n)
	}
}
