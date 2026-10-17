// Package clean implements the non-TUI "safe clean" mode: gather every item
// graded core.Safe, show what each category would do, and remove only what
// the user confirms, category by category.
package clean

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/safety"
)

// Group is the safe items of one category.
type Group struct {
	Cat   core.Category
	Items []core.Item
	Total int64
}

// Plan scans all categories concurrently and keeps items that are Safe, not
// on the ignore list (nil ok) and have a real byte size (Metric items such as
// watts are not reclaimable space). Empty groups are dropped; order follows cats.
func Plan(ctx context.Context, cats []core.Category, ign *safety.Ignore) ([]Group, map[string]error) {
	groups := make([]Group, len(cats))
	errs := make([]error, len(cats))
	var wg sync.WaitGroup
	for i, c := range cats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan core.Item)
			done := make(chan error, 1)
			go func() {
				done <- c.Scanner().Scan(ctx, ch)
				close(ch)
			}()
			g := Group{Cat: c}
			for it := range ch {
				if it.Risk != core.Safe || it.Metric != "" || (ign != nil && ign.Match(c.Name(), it.ID)) {
					continue
				}
				g.Items = append(g.Items, it)
				g.Total += it.Size
			}
			errs[i] = <-done
			sort.SliceStable(g.Items, func(a, b int) bool { return g.Items[a].Size > g.Items[b].Size })
			groups[i] = g
		}()
	}
	wg.Wait()
	var out []Group
	scanErrs := map[string]error{}
	for i, g := range groups {
		if errs[i] != nil {
			scanErrs[g.Cat.Name()] = errs[i]
		}
		if len(g.Items) > 0 {
			out = append(out, g)
		}
	}
	return out, scanErrs
}

// Options controls Run.
type Options struct {
	In     io.Reader
	Out    io.Writer
	DryRun bool // print the plan, change nothing
	Audit  *safety.Audit
	// Sudo runs a privileged command with the terminal attached. Nil = real sudo.
	Sudo func(ctx context.Context, argv []string) error
}

// Summary is what Run did.
type Summary struct {
	Removed  int
	Freed    int64
	Skipped  []string // categories the user declined or whose dry run failed
	Failures []string
}

func realSudo(ctx context.Context, argv []string) error {
	c := exec.CommandContext(ctx, "sudo", argv...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// Run shows each group's dry run and asks before removing it. Answers:
// y = this category, n = skip it, a = this and every remaining one, q = stop.
func Run(ctx context.Context, groups []Group, o Options) Summary {
	var sum Summary
	if o.Sudo == nil {
		o.Sudo = realSudo
	}
	in := bufio.NewReader(o.In)
	var grand int64
	for _, g := range groups {
		grand += g.Total
	}
	fmt.Fprintf(o.Out, "Safe clean: %d categories, %s reclaimable\n", len(groups), core.HumanSize(grand))
	all := false
	for gi, g := range groups {
		name := g.Cat.Name()
		fmt.Fprintf(o.Out, "\n== %s: %d item(s), %s ==\n", name, len(g.Items), core.HumanSize(g.Total))
		dry, err := g.Cat.DryRun(ctx, g.Items)
		if err != nil {
			fmt.Fprintf(o.Out, "dry run failed, skipping: %v\n", err)
			sum.Skipped = append(sum.Skipped, name)
			continue
		}
		fmt.Fprintln(o.Out, strings.TrimRight(dry, "\n"))
		if o.DryRun {
			continue
		}
		if !all {
			switch ask(in, o.Out, fmt.Sprintf("Clean %s? [y]es / [n]o / [a]ll remaining / [q]uit: ", name)) {
			case "q":
				for _, r := range groups[gi:] {
					sum.Skipped = append(sum.Skipped, r.Cat.Name())
				}
				return sum
			case "a":
				all = true
			case "y":
			default:
				sum.Skipped = append(sum.Skipped, name)
				continue
			}
		}
		rm := g.Cat.Remove(g.Items)
		ids := make([]string, len(g.Items))
		for i, it := range g.Items {
			ids[i] = it.ID
		}
		audit(o.Audit, safety.Record{Event: "confirm", Category: name, Summary: rm.Summary + " (safe clean)", Items: ids})
		res := safety.Record{Event: "result", Category: name, Summary: rm.Summary + " (safe clean)", Items: ids}
		var done []string
		var failed map[string]error
		switch {
		case rm.Sudo != nil:
			if err := o.Sudo(ctx, rm.Sudo); err != nil {
				res.Error = err.Error()
				sum.Failures = append(sum.Failures, name+": "+err.Error())
			} else {
				done = ids // sudo batch is all-or-nothing
			}
		case rm.Do != nil:
			r := rm.Do(ctx)
			done, failed = r.Done, r.Failed
		}
		res.Done = done
		ok := map[string]bool{}
		for _, id := range done {
			ok[id] = true
		}
		for id, err := range failed {
			res.Failed = append(res.Failed, id+": "+err.Error())
			sum.Failures = append(sum.Failures, name+" "+id+": "+err.Error())
		}
		sort.Strings(res.Failed)
		audit(o.Audit, res)
		var freed int64
		for _, it := range g.Items {
			if ok[it.ID] {
				freed += it.Size
			}
		}
		sum.Removed += len(done)
		sum.Freed += freed
		fmt.Fprintf(o.Out, "%s: removed %d/%d, freed %s\n", name, len(done), len(g.Items), core.HumanSize(freed))
	}
	return sum
}

func audit(a *safety.Audit, r safety.Record) { _ = a.Log(r) }

// ask reads one answer; EOF counts as "q" so a closed stdin never removes.
func ask(in *bufio.Reader, out io.Writer, prompt string) string {
	for {
		fmt.Fprint(out, prompt)
		line, err := in.ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(line))
		switch a {
		case "y", "yes":
			return "y"
		case "n", "no":
			return "n"
		case "a", "all":
			return "a"
		case "q", "quit":
			return "q"
		}
		if err != nil {
			return "q"
		}
	}
}
