package pkgs

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// aptFmt: one installed package per line, tab separated.
const aptFmt = `${Package}\t${Essential}\t${Priority}\t${Installed-Size}\t${Depends}\t${Pre-Depends}\t${Recommends}\t${Provides}\t${db:Status-Abbrev}\t${binary:Summary}\n`

// aptProtectRE: floor for kernel/boot metapackages the dpkg metadata cannot
// flag. Running-kernel image/modules are added by ProtectSet from uname -r.
var aptProtectRE = regexp.MustCompile(`^(linux-image-.*|linux-modules.*|linux-headers-.*|linux-generic.*|linux-firmware|grub.*|shim-signed|systemd|apt|dpkg|sudo|ubuntu-minimal|ubuntu-standard|ubuntu-desktop.*|debian-.*-archive-keyring|network-manager)$`)

type Apt struct {
	r runner.Runner
}

func NewApt(r runner.Runner) *Apt { return &Apt{r: r} }

func (a *Apt) Name() string { return "apt" }

func (a *Apt) Scanner(all bool) core.Scanner { return &aptScanner{a: a, all: all} }

// purge also drops config files, matching pacman -Rns.
func (a *Apt) RemoveCmd(pkgs []string) []string {
	return append([]string{"apt-get", "purge"}, pkgs...)
}

func (a *Apt) DryRunCmd(pkgs []string) []string {
	return append([]string{"apt-get", "-s", "purge"}, pkgs...)
}

func (a *Apt) InfoCmd(pkg string) []string { return []string{"dpkg", "-s", pkg} }

// aptPkg is one parsed dpkg-query row.
type aptPkg struct {
	Name, Priority, Desc string
	Essential            bool
	Size                 int64 // bytes
	Deps                 []string
	Provides             []string
}

// ProtectSet: Essential and Priority required/important packages plus
// everything they depend on, held packages, and the running kernel.
func (a *Apt) protectSet(ctx context.Context, pkgs map[string]*aptPkg) map[string]bool {
	set := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if set[n] {
			return
		}
		set[n] = true
		if p := pkgs[n]; p != nil {
			for _, d := range p.Deps {
				walk(d)
			}
		}
	}
	prov := providers(pkgs)
	var walkRes func(n string)
	walkRes = func(n string) {
		if _, ok := pkgs[n]; ok {
			walk(n)
			return
		}
		for _, p := range prov[n] {
			walk(p)
		}
	}
	for n, p := range pkgs {
		if p.Essential || p.Priority == "required" || p.Priority == "important" {
			walk(n)
			for _, d := range p.Deps {
				walkRes(d)
			}
		}
	}
	if out, err := a.r.Output(ctx, "apt-mark", "showhold"); err == nil {
		for _, n := range strings.Fields(string(out)) {
			set[n] = true
		}
	}
	if out, err := a.r.Output(ctx, "uname", "-r"); err == nil {
		if rel := strings.TrimSpace(string(out)); rel != "" {
			for _, pre := range []string{"linux-image-", "linux-modules-", "linux-modules-extra-", "linux-headers-"} {
				set[pre+rel] = true
			}
		}
	}
	return set
}

func providers(pkgs map[string]*aptPkg) map[string][]string {
	m := map[string][]string{}
	for n, p := range pkgs {
		for _, v := range p.Provides {
			m[v] = append(m[v], n)
		}
	}
	return m
}

type aptScanner struct {
	a   *Apt
	all bool
}

func (s *aptScanner) Name() string { return "Packages" }

func (s *aptScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := s.a.r.Output(ctx, "dpkg-query", "-W", "-f", aptFmt)
	if err != nil {
		return err
	}
	auto := map[string]bool{}
	if o, err := s.a.r.Output(ctx, "apt-mark", "showauto"); err == nil {
		for _, n := range strings.Fields(string(o)) {
			auto[n] = true
		}
	}
	// apt itself knows the full autoremove chain; ask it.
	chain := map[string]bool{}
	if o, err := s.a.r.Output(ctx, "apt-get", "-s", "autoremove"); err == nil {
		chain = ParseAptRemv(string(o))
	}
	pkgs := ParseDpkgQuery(string(raw))
	protect := s.a.protectSet(ctx, pkgs)
	for _, it := range AptItems(pkgs, auto, chain, protect, s.all) {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// ParseAptRemv extracts package names from `apt-get -s` "Remv name [ver]" lines.
func ParseAptRemv(raw string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(raw, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && (f[0] == "Remv" || f[0] == "Purg") {
			m[strings.SplitN(f[1], ":", 2)[0]] = true
		}
	}
	return m
}

// ParseDpkgQuery parses aptFmt output; only fully installed ("ii") rows.
func ParseDpkgQuery(raw string) map[string]*aptPkg {
	pkgs := map[string]*aptPkg{}
	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 10 || !strings.HasPrefix(f[8], "ii") {
			continue
		}
		kib, _ := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		p := &aptPkg{
			Name: f[0], Essential: f[1] == "yes", Priority: f[2],
			Size: kib << 10, Desc: f[9],
			Deps:     depNames(f[4], f[5], f[6]),
			Provides: depNames(f[7]),
		}
		if old := pkgs[p.Name]; old != nil { // multiarch twin
			old.Size += p.Size
			old.Deps = append(old.Deps, p.Deps...)
			old.Essential = old.Essential || p.Essential
			continue
		}
		pkgs[p.Name] = p
	}
	return pkgs
}

// depNames flattens "a (>= 1), b | c:any" to [a b c]. Every alternative
// counts as a dependency: keeps more packages, never fewer.
func depNames(fields ...string) []string {
	var out []string
	for _, fl := range fields {
		for _, part := range strings.FieldsFunc(fl, func(r rune) bool { return r == ',' || r == '|' }) {
			n := strings.Fields(part)
			if len(n) == 0 {
				continue
			}
			out = append(out, strings.SplitN(n[0], ":", 2)[0])
		}
	}
	return out
}

// AptItems builds candidates. A package is unused when no other installed
// package lists it (or something it provides) in Depends/Pre-Depends/Recommends.
func AptItems(pkgs map[string]*aptPkg, auto, chain, protect map[string]bool, all bool) []core.Item {
	prov := providers(pkgs)
	rdeps := map[string]int{}
	for n, p := range pkgs {
		for _, d := range p.Deps {
			if d == n {
				continue
			}
			if _, ok := pkgs[d]; ok {
				rdeps[d]++
			}
			for _, pr := range prov[d] {
				if pr != n {
					rdeps[pr]++
				}
			}
		}
	}
	var items []core.Item
	for n, p := range pkgs {
		if protect[n] || aptProtectRE.MatchString(n) {
			continue
		}
		unused := rdeps[n] == 0
		var tags []string
		cand := all
		switch {
		case auto[n] && (unused || chain[n]):
			tags, cand = append(tags, "ORPHAN"), true
		case !auto[n] && unused:
			tags, cand = append(tags, "LEAF"), true
		}
		if !cand {
			continue
		}
		risk := core.Review
		if len(tags) > 0 && tags[0] == "ORPHAN" {
			risk = core.Safe
		}
		if !unused && !chain[n] {
			risk = core.Danger
		}
		items = append(items, core.Item{
			ID: n, Category: "Packages", Title: n, Desc: p.Desc,
			Size: p.Size, Risk: risk, Tags: tags,
		})
	}
	sortItems(items)
	return items
}

func sortItems(items []core.Item) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}
