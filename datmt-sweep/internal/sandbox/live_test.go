//go:build live

package sandbox

import (
	"context"
	"os/exec"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

func TestLiveFlatpak(t *testing.T) {
	if _, err := exec.LookPath("flatpak"); err != nil {
		t.Skip("no flatpak")
	}
	ch := make(chan core.Item, 512)
	if err := NewFlatpak(runner.System{}).Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	for it := range ch {
		t.Logf("%-7s %-10s %-9s %s", it.Risk, core.HumanSize(it.Size), it.Tags, it.Desc)
	}
}
