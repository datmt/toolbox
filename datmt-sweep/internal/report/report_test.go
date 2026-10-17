package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
)

type fakeScanner struct {
	items []core.Item
	err   error
}

func (f fakeScanner) Name() string { return "fake" }
func (f fakeScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	for _, it := range f.items {
		out <- it
	}
	return f.err
}

type fakeCat struct {
	name string
	sc   fakeScanner
}

func (f fakeCat) Name() string                                        { return f.name }
func (f fakeCat) Scanner() core.Scanner                               { return f.sc }
func (f fakeCat) Info(context.Context, core.Item) string              { return "" }
func (f fakeCat) DryRun(context.Context, []core.Item) (string, error) { return "", nil }
func (f fakeCat) Remove([]core.Item) core.Removal                     { return core.Removal{} }

func TestCollect(t *testing.T) {
	cats := []core.Category{
		fakeCat{"a", fakeScanner{items: []core.Item{
			{ID: "small", Size: 10},
			{ID: "big", Size: 1000, Risk: core.Review},
			{ID: "watts", Size: 5, Metric: "0.8 W"},
		}}},
		fakeCat{"b", fakeScanner{err: errors.New("boom")}},
	}
	r := Collect(context.Background(), cats, nil)
	if len(r.Categories) != 2 || r.Categories[0].Name != "a" {
		t.Fatalf("order: %+v", r.Categories)
	}
	a := r.Categories[0]
	if a.Items[0].ID != "big" || a.Items[0].Risk != "review" {
		t.Errorf("sort/risk: %+v", a.Items)
	}
	if a.TotalBytes != 1010 {
		t.Errorf("total = %d, want 1010 (metric items excluded)", a.TotalBytes)
	}
	if r.Categories[1].Error != "boom" || r.Categories[1].Items == nil {
		t.Errorf("error cat: %+v", r.Categories[1])
	}
	var buf bytes.Buffer
	if err := Write(&buf, r); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
}
