//go:build live

package journal

import (
	"context"
	"os/exec"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

func TestLiveScan(t *testing.T) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("no journalctl")
	}
	ch := make(chan core.Item, 4096)
	if err := New(runner.System{}).Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	for it := range ch {
		t.Logf("%-12s %10s  %s", it.ID, core.HumanSize(it.Size), it.Desc)
	}
}
