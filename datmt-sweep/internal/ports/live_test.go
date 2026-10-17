//go:build live

package ports

import (
	"context"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
)

// Read-only: reads /proc/net and /proc/PID/fd.
func TestLiveScan(t *testing.T) {
	ch := make(chan core.Item, 4096)
	if err := New().Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	n := 0
	for it := range ch {
		if n < 8 {
			t.Logf("%-40s %v", it.Title, it.Tags)
		}
		n++
	}
	t.Logf("%d sockets", n)
}
