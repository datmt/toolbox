package pkgs

import (
	"context"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// Category adapts a Backend to the core.Category sidebar interface.
func Category(be Backend, r runner.Runner, all bool) core.Category {
	return &pkgCategory{be: be, r: r, all: all}
}

type pkgCategory struct {
	be  Backend
	r   runner.Runner
	all bool
}

func (c *pkgCategory) Name() string          { return "Packages" }
func (c *pkgCategory) Scanner() core.Scanner { return c.be.Scanner(c.all) }

func (c *pkgCategory) Info(ctx context.Context, it core.Item) string {
	out, err := c.r.Output(ctx, c.be.InfoCmd(it.ID)...)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(out)
}

func (c *pkgCategory) DryRun(ctx context.Context, items []core.Item) (string, error) {
	out, err := c.r.Output(ctx, c.be.DryRunCmd(ids(items))...)
	return string(out), err
}

func (c *pkgCategory) Remove(items []core.Item) core.Removal {
	cmd := c.be.RemoveCmd(ids(items))
	return core.Removal{Summary: "sudo " + strings.Join(cmd, " "), Sudo: cmd}
}

func ids(items []core.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
