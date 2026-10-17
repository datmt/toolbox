//go:build live

package services

import (
	"context"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// Read-only: samples the real cgroup tree for a few seconds.
func TestLiveScan(t *testing.T) {
	c := New(runner.System{})
	ch := make(chan core.Item, 512)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	n := 0
	for it := range ch {
		if n < 8 {
			t.Logf("%-40s %-10s %v %s", it.Title, it.Metric, it.Tags, it.Desc)
		}
		n++
	}
	t.Logf("%d active units", n)
}
