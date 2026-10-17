//go:build live

package pkgs

import (
	"context"
	"os/exec"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// Runs against the real pacman DB; skipped where pacman is absent.
func TestLiveScan(t *testing.T) {
	if _, err := exec.LookPath("pacman"); err != nil {
		t.Skip("no pacman")
	}
	out := make(chan core.Item, 4096)
	err := NewPacman(runner.System{}).Scanner(false).Scan(context.Background(), out)
	close(out)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for it := range out {
		n++
		if n <= 5 {
			t.Logf("%-30s %10s %v", it.ID, core.HumanSize(it.Size), it.Tags)
		}
	}
	t.Logf("%d candidates", n)
}
