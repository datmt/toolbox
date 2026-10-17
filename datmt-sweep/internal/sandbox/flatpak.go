// Package sandbox cleans sandboxed app stores: flatpak and snap.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// flatpak list columns, tab separated.
const flatpakCols = "--columns=application,arch,branch,size,installation,runtime,name"

// FlatpakRef is one installed app or runtime.
type FlatpakRef struct {
	ID, Arch, Branch string
	Size             int64
	Install          string // system | user | other installation name
	Runtime          string // id/arch/branch the app runs on (apps only)
	Name             string
	App              bool
}

func (f FlatpakRef) ref() string { return f.ID + "/" + f.Arch + "/" + f.Branch }

// ParseFlatpakList parses `flatpak list` output with flatpakCols.
func ParseFlatpakList(raw string, app bool) []FlatpakRef {
	var out []FlatpakRef
	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 7 || strings.TrimSpace(f[0]) == "" {
			continue
		}
		out = append(out, FlatpakRef{
			ID: f[0], Arch: f[1], Branch: f[2],
			Size: core.ParseSize(f[3], true), Install: f[4],
			Runtime: f[5], Name: f[6], App: app,
		})
	}
	return out
}

// UnusedRuntimes returns runtimes no app uses (conservative: see inFamily). An extension (Locale, GL, ...)
// is used when its id is under a used runtime's or an installed app's id and
// its branch matches the parent runtime's.
func UnusedRuntimes(apps, runtimes []FlatpakRef) []FlatpakRef {
	type rt struct{ id, branch string }
	var used []rt
	for _, a := range apps {
		p := strings.Split(a.Runtime, "/")
		if len(p) == 3 {
			used = append(used, rt{p[0], p[2]})
		}
	}
	isUsed := func(r FlatpakRef) bool {
		for _, u := range used {
			if r.ID == u.id && r.Branch == u.branch {
				return true
			}
			if strings.HasPrefix(r.ID, u.id+".") && strings.HasPrefix(r.Branch, u.branch) {
				return true
			}
		}
		for _, a := range apps {
			if strings.HasPrefix(r.ID, a.ID+".") {
				return true
			}
		}
		return false
	}
	// Only runtimes belonging to a family some app uses (same id, or an
	// extension under it) are candidates; other extensions (KStyle, Wine.mono,
	// plugins) attach to apps in ways the ref alone does not reveal.
	inFamily := func(r FlatpakRef) bool {
		for _, u := range used {
			if r.ID == u.id || strings.HasPrefix(r.ID, u.id+".") {
				return true
			}
		}
		return false
	}
	var out []FlatpakRef
	for _, r := range runtimes {
		if inFamily(r) && !isUsed(r) {
			out = append(out, r)
		}
	}
	return out
}

type Flatpak struct {
	r runner.Runner
	// uninstall runs unprivileged for user installations; swapped in tests.
	uninstall func(ctx context.Context, argv []string) error
}

func NewFlatpak(r runner.Runner) *Flatpak {
	return &Flatpak{r: r, uninstall: func(ctx context.Context, argv []string) error {
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}}
}

func (c *Flatpak) Name() string          { return "Flatpak" }
func (c *Flatpak) Scanner() core.Scanner { return c }

func (c *Flatpak) Scan(ctx context.Context, out chan<- core.Item) error {
	ar, err := c.r.Output(ctx, "flatpak", "list", "--app", flatpakCols)
	if err != nil {
		return err
	}
	rr, err := c.r.Output(ctx, "flatpak", "list", "--runtime", flatpakCols)
	if err != nil {
		return err
	}
	apps := ParseFlatpakList(string(ar), true)
	var items []core.Item
	for _, a := range apps {
		items = append(items, core.Item{
			ID: a.Install + " " + a.ref(), Category: "Flatpak", Title: a.Name,
			Desc: a.ID + " (" + a.Install + ")", Size: a.Size, Risk: core.Review,
			Tags: []string{"APP", strings.ToUpper(a.Install)},
		})
	}
	for _, r := range UnusedRuntimes(apps, ParseFlatpakList(string(rr), false)) {
		items = append(items, core.Item{
			ID: r.Install + " " + r.ref(), Category: "Flatpak", Title: r.ID + " " + r.Branch,
			Desc: r.ref() + " (" + r.Install + ")", Size: r.Size, Risk: core.Safe,
			Tags: []string{"UNUSED", strings.ToUpper(r.Install)},
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	for _, it := range items {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Flatpak) Info(ctx context.Context, it core.Item) string {
	inst, ref, _ := strings.Cut(it.ID, " ")
	out, err := c.r.Output(ctx, "flatpak", "info", installFlag(inst), ref)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(out)
}

func installFlag(inst string) string {
	if inst == "user" {
		return "--user"
	}
	if inst == "system" {
		return "--system"
	}
	return "--installation=" + inst
}

// split groups refs by installation flag.
func split(items []core.Item) map[string][]string {
	g := map[string][]string{}
	for _, it := range items {
		inst, ref, _ := strings.Cut(it.ID, " ")
		g[inst] = append(g[inst], ref)
	}
	return g
}

func (c *Flatpak) cmd(inst string, refs []string) []string {
	return append([]string{"flatpak", "uninstall", installFlag(inst), "--noninteractive", "-y"}, refs...)
}

// DryRun lists the commands; flatpak uninstall has no preview mode.
func (c *Flatpak) DryRun(_ context.Context, items []core.Item) (string, error) {
	var b strings.Builder
	var total int64
	for _, it := range items {
		total += it.Size
	}
	g := split(items)
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pre := ""
		if k != "user" {
			pre = "sudo "
		}
		fmt.Fprintf(&b, "%s%s\n", pre, strings.Join(c.cmd(k, g[k]), " "))
	}
	fmt.Fprintf(&b, "\nfrees about %s; related extensions go with their app (flatpak has no dry run)\n", core.HumanSize(total))
	return b.String(), nil
}

// Remove: system/other installations need root -> one Sudo command; user
// installation runs in-process. Mixed selections are refused so sudo never
// touches root's own (empty) user installation.
func (c *Flatpak) Remove(items []core.Item) core.Removal {
	g := split(items)
	if len(g) == 1 {
		for inst, refs := range g {
			cmd := c.cmd(inst, refs)
			if inst == "user" {
				return core.Removal{Summary: strings.Join(cmd, " "), Do: func(ctx context.Context) core.Result {
					res := core.Result{Failed: map[string]error{}}
					if err := c.uninstall(ctx, cmd); err != nil {
						for _, it := range items {
							res.Failed[it.ID] = err
						}
						return res
					}
					for _, it := range items {
						res.Done = append(res.Done, it.ID)
					}
					return res
				}}
			}
			return core.Removal{Summary: "sudo " + strings.Join(cmd, " "), Sudo: cmd}
		}
	}
	return core.Removal{Summary: "mixed user/system flatpak selection", Do: func(context.Context) core.Result {
		res := core.Result{Failed: map[string]error{}}
		for _, it := range items {
			res.Failed[it.ID] = fmt.Errorf("select either user or system flatpaks per batch")
		}
		return res
	}}
}
