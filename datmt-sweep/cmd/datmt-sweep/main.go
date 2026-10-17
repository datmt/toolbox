// datmt-sweep: interactive Linux cleaning tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/datmt/datmt-sweep/internal/clean"
	"github.com/datmt/datmt-sweep/internal/containers"
	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/files"
	"github.com/datmt/datmt-sweep/internal/journal"
	"github.com/datmt/datmt-sweep/internal/pkgs"
	"github.com/datmt/datmt-sweep/internal/ports"
	"github.com/datmt/datmt-sweep/internal/power"
	"github.com/datmt/datmt-sweep/internal/procs"
	"github.com/datmt/datmt-sweep/internal/report"
	"github.com/datmt/datmt-sweep/internal/runner"
	"github.com/datmt/datmt-sweep/internal/safety"
	"github.com/datmt/datmt-sweep/internal/sandbox"
	"github.com/datmt/datmt-sweep/internal/services"
	"github.com/datmt/datmt-sweep/internal/tui"
	"github.com/datmt/datmt-sweep/internal/tunables"
)

// version is set at release time via -ldflags "-X main.version=...".
var version = "dev"

func filterCats(cats []core.Category, sub string) []core.Category {
	sub = strings.ToLower(sub)
	var out []core.Category
	for _, c := range cats {
		if strings.Contains(strings.ToLower(c.Name()), sub) {
			out = append(out, c)
		}
	}
	return out
}

func main() {
	all := flag.Bool("all", false, "offer every installed package, not just unused ones")
	minMiB := flag.Int64("min-size", 100, "big-file threshold in MiB")
	asJSON := flag.Bool("json", false, "scan everything, print JSON to stdout, exit (read-only)")
	only := flag.String("category", "", "with --json: only categories whose name contains this (case-insensitive)")
	doClean := flag.Bool("clean", false, "safe clean: remove everything graded safe, confirming per category")
	dryRun := flag.Bool("dry-run", false, "with --clean: show what would be removed, change nothing")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("datmt-sweep", version)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "datmt-sweep:", err)
		os.Exit(1)
	}
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		cacheDir = filepath.Join(home, ".cache")
	}
	guard := files.NewGuard(home)
	trash := files.TrashDir()

	r := runner.System{}
	var cats []core.Category
	if be := pkgs.Detect(r); be != nil {
		cats = append(cats, pkgs.Category(be, r, *all))
	}
	if _, err := exec.LookPath("docker"); err == nil {
		cats = append(cats, containers.NewDocker(r).Categories()...)
	}
	if _, err := exec.LookPath("flatpak"); err == nil {
		cats = append(cats, sandbox.NewFlatpak(r))
	}
	if _, err := exec.LookPath("snap"); err == nil {
		cats = append(cats, sandbox.NewSnap(r))
	}
	if _, err := exec.LookPath("journalctl"); err == nil {
		cats = append(cats, journal.New(r))
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		cats = append(cats, services.New(r))
	}
	cats = append(cats, procs.New(), ports.New(), tunables.New(r))
	cats = append(cats,
		files.BigFiles(home, *minMiB<<20, guard, trash),
		files.Caches(cacheDir, guard),
		files.TrashBin(trash),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ignore, err := safety.LoadIgnore(safety.DefaultIgnorePath(home))
	if err != nil {
		fmt.Fprintln(os.Stderr, "datmt-sweep: ignore list:", err)
		os.Exit(1)
	}

	if *asJSON {
		if *only != "" {
			cats = filterCats(cats, *only)
		}
		if err := report.Write(os.Stdout, report.Collect(ctx, cats, ignore)); err != nil {
			fmt.Fprintln(os.Stderr, "datmt-sweep:", err)
			os.Exit(1)
		}
		return
	}

	if *doClean {
		fi, err := os.Stdin.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(os.Stderr, "datmt-sweep: --clean needs an interactive terminal")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "scanning...")
		groups, scanErrs := clean.Plan(ctx, cats, ignore)
		for name, err := range scanErrs {
			fmt.Fprintf(os.Stderr, "datmt-sweep: scan %s: %v\n", name, err)
		}
		if len(groups) == 0 {
			fmt.Println("nothing safe to clean")
			return
		}
		sum := clean.Run(ctx, groups, clean.Options{
			In: os.Stdin, Out: os.Stdout, DryRun: *dryRun,
			Audit: safety.NewAudit(safety.DefaultAuditPath(home)),
		})
		if !*dryRun {
			fmt.Printf("\ndone: removed %d item(s), freed %s\n", sum.Removed, core.HumanSize(sum.Freed))
		}
		if len(sum.Failures) > 0 {
			for _, f := range sum.Failures {
				fmt.Fprintln(os.Stderr, "FAILED", f)
			}
			os.Exit(1)
		}
		return
	}

	m := tui.New(ctx, cats)
	m.Ignore = ignore
	m.Monitor = power.NewMonitor()
	m.Audit = safety.NewAudit(safety.DefaultAuditPath(home))
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "datmt-sweep:", err)
		os.Exit(1)
	}
}
