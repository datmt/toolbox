//go:build live

package containers

import (
	"os/exec"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// Read-only: scans the real docker daemon, removes nothing.
func TestLiveScan(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker")
	}
	d := NewDocker(runner.System{})
	for _, c := range d.Categories() {
		items := scan(t, c)
		var total int64
		for _, it := range items {
			total += it.Size
		}
		t.Logf("%-10s %3d items %10s", c.Name(), len(items), core.HumanSize(total))
	}
}
