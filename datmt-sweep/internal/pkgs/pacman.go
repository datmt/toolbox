package pkgs

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// protectRE: fallback floor for packages never offered even when they look
// unused. The real protect set is derived from pacman by ProtectSet; this
// only covers what that cannot see. Anything the base group depends on is
// already excluded because "Required By" is non-empty for it.
var protectRE = regexp.MustCompile(`^(base|base-devel|linux|linux-[a-z]+|linux-[a-z]+-headers|linux-firmware.*|intel-ucode|amd-ucode|grub|systemd-boot|efibootmgr|refind|pacman|sudo|mkinitcpio.*|filesystem|glibc|nvidia.*|mesa|networkmanager)$`)

type Pacman struct {
	r           runner.Runner
	modulesGlob string // kernel module dirs; each has a pkgbase file
}

func NewPacman(r runner.Runner) *Pacman {
	return &Pacman{r: r, modulesGlob: "/usr/lib/modules/*/pkgbase"}
}

// ProtectSet derives what must never be offered from the system itself:
// the base/base-devel groups, HoldPkg and IgnorePkg from pacman.conf, and
// the packages owning installed kernels (/usr/lib/modules/*/pkgbase).
func (p *Pacman) ProtectSet(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	add := func(out []byte, err error) {
		if err != nil {
			return
		}
		for _, n := range strings.Fields(string(out)) {
			set[n] = true
		}
	}
	// base is a metapackage on current Arch, a group on old installs: ask
	// for both, one name at a time since pacman fails on a missing one.
	for _, g := range []string{"base", "base-devel"} {
		add(p.r.Output(ctx, "pacman", "-Qgq", g))
		if out, err := p.r.Output(ctx, "pacman", "-Qi", g); err == nil {
			set[g] = true
			f := parseBlock(string(out))
			for _, d := range strings.Fields(f["Depends On"]) {
				if n := strings.FieldsFunc(d, func(r rune) bool { return r == '>' || r == '<' || r == '=' }); len(n) > 0 {
					set[n[0]] = true
				}
			}
		}
	}
	add(p.r.Output(ctx, "pacman-conf", "HoldPkg"))
	add(p.r.Output(ctx, "pacman-conf", "IgnorePkg"))
	if files, _ := filepath.Glob(p.modulesGlob); len(files) > 0 {
		add(p.r.Output(ctx, append([]string{"pacman", "-Qoq"}, files...)...))
	}
	return set
}

func (p *Pacman) Name() string { return "pacman" }

func (p *Pacman) Scanner(all bool) core.Scanner { return &pacmanScanner{p: p, all: all} }

func (p *Pacman) RemoveCmd(pkgs []string) []string {
	return append([]string{"pacman", "-Rns"}, pkgs...)
}

// -n cannot combine with -p, so the preview uses -Rs: same target set.
func (p *Pacman) DryRunCmd(pkgs []string) []string {
	return append([]string{"pacman", "-Rsp", "--print-format", "%n %v"}, pkgs...)
}

func (p *Pacman) InfoCmd(pkg string) []string { return []string{"pacman", "-Qi", pkg} }

type pacmanScanner struct {
	p   *Pacman
	all bool
}

func (s *pacmanScanner) Name() string { return "Packages" }

func (s *pacmanScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := s.p.r.Output(ctx, "pacman", "-Qi")
	if err != nil {
		return err
	}
	foreign := map[string]bool{}
	if f, err := s.p.r.Output(ctx, "pacman", "-Qmq"); err == nil {
		for _, n := range strings.Fields(string(f)) {
			foreign[n] = true
		}
	}
	protect := s.p.ProtectSet(ctx)
	for _, it := range ParsePacmanQi(string(raw), foreign, protect, s.all) {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// ParsePacmanQi turns `pacman -Qi` output into candidate items. Packages in
// protect (or matching the fallback protectRE) are skipped.
func ParsePacmanQi(raw string, foreign, protect map[string]bool, all bool) []core.Item {
	var items []core.Item
	for _, block := range strings.Split(raw, "\n\n") {
		f := parseBlock(block)
		name := f["Name"]
		if name == "" || protect[name] || protectRE.MatchString(name) {
			continue
		}
		reqNone := f["Required By"] == "None"
		optNone := f["Optional For"] == "None"
		var tags []string
		cand := all
		if reqNone && optNone {
			switch {
			case strings.Contains(f["Install Reason"], "dependency"):
				tags = append(tags, "ORPHAN")
				cand = true
			case strings.Contains(f["Install Reason"], "Explicitly"):
				tags = append(tags, "LEAF")
				cand = true
			}
		}
		if !cand {
			continue
		}
		risk := core.Review
		if len(tags) > 0 && tags[0] == "ORPHAN" {
			risk = core.Safe
		}
		if !reqNone {
			risk = core.Danger
		}
		if foreign[name] {
			tags = append(tags, "AUR")
		}
		items = append(items, core.Item{
			ID: name, Category: "Packages", Title: name,
			Desc: f["Description"], Size: parseSize(f["Installed Size"]),
			Risk: risk, Tags: tags, Date: shortDate(f["Install Date"]),
		})
	}
	return items
}

func parseBlock(block string) map[string]string {
	f := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue // continuation lines of multi-line fields
		}
		f[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return f
}

func parseSize(s string) int64 {
	fs := strings.Fields(s)
	if len(fs) != 2 {
		return 0
	}
	v, err := strconv.ParseFloat(fs[0], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}[fs[1]]
	return int64(v * mult)
}

// "Tue 14 Oct 2026 09:31:00 AM UTC" -> "14 Oct 2026"
func shortDate(s string) string {
	fs := strings.Fields(s)
	if len(fs) >= 4 {
		return strings.Join(fs[1:4], " ")
	}
	return s
}
