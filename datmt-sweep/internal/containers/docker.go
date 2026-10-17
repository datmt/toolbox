// Package containers offers unused docker objects as cleanup categories.
package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// df mirrors `docker system df -v --format '{{json .}}'`. Sizes are strings.
type df struct {
	Images []struct {
		ID, Repository, Tag, Size, CreatedSince, Containers string
	}
	Containers []struct {
		ID, Names, Image, State, Status, Size, CreatedAt string
	}
	Volumes []struct {
		Name, Size, Links, Labels string
	}
}

// Docker snapshots usage once and serves the three categories from it.
type Docker struct {
	r    runner.Runner
	bin  string
	once sync.Once
	df   df
	err  error
}

func NewDocker(r runner.Runner) *Docker { return &Docker{r: r, bin: "docker"} }

func (d *Docker) load(ctx context.Context) (*df, error) {
	d.once.Do(func() {
		out, err := d.r.Output(ctx, d.bin, "system", "df", "-v", "--format", "{{json .}}")
		if err != nil {
			d.err = fmt.Errorf("docker not usable (daemon down, or user not in docker group?): %w", err)
			return
		}
		d.err = json.Unmarshal(out, &d.df)
	})
	return &d.df, d.err
}

// Categories returns Containers (stopped), Images (unused), Volumes (unused).
func (d *Docker) Categories() []core.Category {
	return []core.Category{
		&dockerCat{d: d, name: "Containers", kind: kContainer},
		&dockerCat{d: d, name: "Images", kind: kImage},
		&dockerCat{d: d, name: "Volumes", kind: kVolume},
	}
}

type kind int

const (
	kContainer kind = iota
	kImage
	kVolume
)

type dockerCat struct {
	d    *Docker
	name string
	kind kind
}

func (c *dockerCat) Name() string          { return c.name }
func (c *dockerCat) Scanner() core.Scanner { return c }
func (c *dockerCat) Scan(ctx context.Context, out chan<- core.Item) error {
	df, err := c.d.load(ctx)
	if err != nil {
		return err
	}
	for _, it := range c.items(df) {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *dockerCat) items(df *df) []core.Item {
	var items []core.Item
	switch c.kind {
	case kContainer:
		for _, x := range df.Containers {
			if x.State == "running" || x.State == "restarting" || x.State == "paused" {
				continue
			}
			items = append(items, core.Item{
				ID: x.ID, Category: c.name, Title: x.Names, Size: core.ParseSize(x.Size, true),
				Desc: fmt.Sprintf("%s · %s · %s", x.Image, x.Status, x.CreatedAt), Risk: core.Review,
				Tags: []string{strings.ToUpper(x.State)}, Date: dateOf(x.CreatedAt),
			})
		}
	case kImage:
		for _, x := range df.Images {
			if x.Containers != "0" {
				continue
			}
			it := core.Item{
				ID: x.Repository + ":" + x.Tag, Title: x.Repository + ":" + x.Tag, Category: c.name,
				Size: core.ParseSize(x.Size, true), Desc: x.ID + " · " + x.CreatedSince, Risk: core.Safe,
			}
			if x.Repository == "<none>" {
				// untagged: only the id can name it
				it.ID, it.Title = strings.TrimPrefix(x.ID, "sha256:"), "<dangling> "+shortID(x.ID)
				it.Tags = []string{"DANGLING"}
			}
			items = append(items, it)
		}
	case kVolume:
		for _, x := range df.Volumes {
			if x.Links != "0" {
				continue
			}
			desc := "no container references this volume; data is lost on removal"
			if p := label(x.Labels, "com.docker.compose.project"); p != "" {
				desc += " · compose project " + p
			}
			items = append(items, core.Item{
				ID: x.Name, Category: c.name, Title: x.Name, Size: core.ParseSize(x.Size, true),
				Desc: desc, Risk: core.Danger, Tags: []string{"UNUSED", "DATA"},
			})
		}
	}
	return items
}

func (c *dockerCat) Info(_ context.Context, it core.Item) string {
	return fmt.Sprintf("%s\n\n%s\n\nSize: %s\nRisk: %s\n", it.Title, it.Desc, core.HumanSize(it.Size), it.Risk)
}

func (c *dockerCat) argv(id string) []string {
	switch c.kind {
	case kContainer:
		return []string{c.d.bin, "rm", id}
	case kImage:
		return []string{c.d.bin, "rmi", id}
	}
	return []string{c.d.bin, "volume", "rm", id}
}

// DryRun: docker has no dry run for rm/rmi/volume rm, so list the exact
// commands. Docker itself refuses in-use objects (no -f is ever passed).
func (c *dockerCat) DryRun(_ context.Context, items []core.Item) (string, error) {
	var b strings.Builder
	var total int64
	for _, it := range items {
		fmt.Fprintf(&b, "%-60s %10s\n", strings.Join(c.argv(it.ID), " "), core.HumanSize(it.Size))
		total += it.Size
	}
	fmt.Fprintf(&b, "\nup to %s (shared layers may free less)\n", core.HumanSize(total))
	if c.kind == kVolume {
		b.WriteString("\nVOLUME DATA IS NOT RECOVERABLE.\n")
	}
	return b.String(), nil
}

func (c *dockerCat) Remove(items []core.Item) core.Removal {
	items = append([]core.Item(nil), items...)
	return core.Removal{
		Summary: fmt.Sprintf("docker: remove %d %s (never forced)", len(items), strings.ToLower(c.name)),
		Do: func(ctx context.Context) core.Result {
			res := core.Result{Failed: map[string]error{}}
			for _, it := range items {
				if _, err := c.d.r.Output(ctx, c.argv(it.ID)...); err != nil {
					res.Failed[it.ID] = err
					continue
				}
				res.Done = append(res.Done, it.ID)
			}
			return res
		},
	}
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func dateOf(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return ""
}

func label(labels, key string) string {
	for _, kv := range strings.Split(labels, ",") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}
