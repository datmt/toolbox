package clean

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
)

type scanner struct{ items []core.Item }

func (s scanner) Name() string { return "x" }
func (s scanner) Scan(_ context.Context, out chan<- core.Item) error {
	for _, it := range s.items {
		out <- it
	}
	return nil
}

type cat struct {
	name    string
	items   []core.Item
	sudo    []string
	removed *[]string
	dryErr  error
}

func (c cat) Name() string                           { return c.name }
func (c cat) Scanner() core.Scanner                  { return scanner{c.items} }
func (c cat) Info(context.Context, core.Item) string { return "" }
func (c cat) DryRun(_ context.Context, it []core.Item) (string, error) {
	return "would remove " + c.name, c.dryErr
}
func (c cat) Remove(items []core.Item) core.Removal {
	if c.sudo != nil {
		return core.Removal{Summary: "sudo " + c.name, Sudo: c.sudo}
	}
	return core.Removal{Summary: "rm " + c.name, Do: func(context.Context) core.Result {
		for _, it := range items {
			*c.removed = append(*c.removed, it.ID)
		}
		ids := []string{}
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		return core.Result{Done: ids}
	}}
}

func TestPlanKeepsOnlySafe(t *testing.T) {
	c := cat{name: "A", items: []core.Item{
		{ID: "s", Size: 5, Risk: core.Safe},
		{ID: "r", Size: 9, Risk: core.Review},
		{ID: "d", Size: 9, Risk: core.Danger},
		{ID: "w", Size: 9, Risk: core.Safe, Metric: "1 W"},
	}}
	empty := cat{name: "B", items: []core.Item{{ID: "r", Risk: core.Review}}}
	g, _ := Plan(context.Background(), []core.Category{c, empty}, nil)
	if len(g) != 1 || len(g[0].Items) != 1 || g[0].Items[0].ID != "s" || g[0].Total != 5 {
		t.Fatalf("groups = %+v", g)
	}
}

func run(t *testing.T, answers string, dry bool, cats ...cat) (Summary, string, []string, [][]string) {
	t.Helper()
	removed := &[]string{}
	var sudos [][]string
	var groups []Group
	for _, c := range cats {
		c.removed = removed
		groups = append(groups, Group{Cat: c, Items: c.items, Total: 1})
	}
	var out bytes.Buffer
	s := Run(context.Background(), groups, Options{
		In: strings.NewReader(answers), Out: &out, DryRun: dry,
		Sudo: func(_ context.Context, argv []string) error { sudos = append(sudos, argv); return nil },
	})
	return s, out.String(), *removed, sudos
}

func TestRunConfirmsPerCategory(t *testing.T) {
	a := cat{name: "A", items: []core.Item{{ID: "a1", Size: 1}}}
	b := cat{name: "B", items: []core.Item{{ID: "b1", Size: 1}}, sudo: []string{"pacman", "-Rns", "b1"}}
	s, _, removed, sudos := run(t, "n\ny\n", false, a, b)
	if len(removed) != 0 || len(sudos) != 1 || sudos[0][0] != "pacman" {
		t.Fatalf("removed=%v sudos=%v", removed, sudos)
	}
	if len(s.Skipped) != 1 || s.Skipped[0] != "A" || s.Removed != 1 {
		t.Errorf("summary = %+v", s)
	}
}

func TestRunAllAndQuit(t *testing.T) {
	a := cat{name: "A", items: []core.Item{{ID: "a1", Size: 1}}}
	b := cat{name: "B", items: []core.Item{{ID: "b1", Size: 1}}}
	if _, _, removed, _ := run(t, "a\n", false, a, b); len(removed) != 2 {
		t.Errorf("all: removed = %v", removed)
	}
	s, _, removed, _ := run(t, "q\n", false, a, b)
	if len(removed) != 0 || len(s.Skipped) != 2 {
		t.Errorf("quit: removed=%v summary=%+v", removed, s)
	}
}

func TestEOFAndDryRunRemoveNothing(t *testing.T) {
	a := cat{name: "A", items: []core.Item{{ID: "a1", Size: 1}}}
	if _, _, removed, _ := run(t, "", false, a); len(removed) != 0 {
		t.Error("EOF removed something")
	}
	_, out, removed, _ := run(t, "y\n", true, a)
	if len(removed) != 0 || !strings.Contains(out, "would remove A") {
		t.Errorf("dry run: removed=%v out=%q", removed, out)
	}
}

func TestFailedDryRunSkips(t *testing.T) {
	a := cat{name: "A", items: []core.Item{{ID: "a1"}}, dryErr: errors.New("nope")}
	s, _, removed, _ := run(t, "y\n", false, a)
	if len(removed) != 0 || len(s.Skipped) != 1 {
		t.Errorf("removed=%v summary=%+v", removed, s)
	}
}
