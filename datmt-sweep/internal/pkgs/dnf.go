package pkgs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// rpmFmt: one installed package per line; [..] expands multi-valued tags.
const rpmFmt = `%{NAME}\t%{SIZE}\t%{INSTALLTIME}\t%{SUMMARY}\t[%{REQUIRENAME},]\t[%{PROVIDENAME},]\n`

// dnfProtectRE: floor for what protected.d and the dep graph might miss.
var dnfProtectRE = regexp.MustCompile(`^(kernel.*|linux-firmware.*|grub2.*|shim.*|systemd.*|dnf.*|rpm.*|sudo|glibc.*|filesystem|fedora-release.*|redhat-release.*|NetworkManager.*|dracut.*|bash)$`)

type Dnf struct {
	r            runner.Runner
	protectedDir string
}

func NewDnf(r runner.Runner) *Dnf {
	return &Dnf{r: r, protectedDir: "/etc/dnf/protected.d"}
}

func (d *Dnf) Name() string { return "dnf" }

func (d *Dnf) Scanner(all bool) core.Scanner { return &dnfScanner{d: d, all: all} }

func (d *Dnf) RemoveCmd(pkgs []string) []string {
	return append([]string{"dnf", "remove"}, pkgs...)
}

// dnf remove --assumeno prints the transaction then exits non-zero; swallow
// the exit status so the preview is not shown as an error.
func (d *Dnf) DryRunCmd(pkgs []string) []string {
	return append([]string{"sh", "-c", `dnf remove --assumeno "$@" 2>&1 || true`, "sh"}, pkgs...)
}

func (d *Dnf) InfoCmd(pkg string) []string { return []string{"rpm", "-qi", pkg} }

// protectSet: /etc/dnf/protected.d/*.conf (one name per line), installonly
// packages of the running kernel.
func (d *Dnf) protectSet(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(d.protectedDir, "*.conf"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				set[l] = true
			}
		}
	}
	if out, err := d.r.Output(ctx, "rpm", "-qf", "/boot/vmlinuz-"+d.kernelRelease(ctx), "--qf", "%{NAME}\n"); err == nil {
		for _, n := range strings.Fields(string(out)) {
			set[n] = true
		}
	}
	return set
}

func (d *Dnf) kernelRelease(ctx context.Context) string {
	out, _ := d.r.Output(ctx, "uname", "-r")
	return strings.TrimSpace(string(out))
}

type dnfScanner struct {
	d   *Dnf
	all bool
}

func (s *dnfScanner) Name() string { return "Packages" }

func (s *dnfScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := s.d.r.Output(ctx, "rpm", "-qa", "--qf", rpmFmt)
	if err != nil {
		return err
	}
	userInst := map[string]bool{}
	haveUser := false
	if o, err := s.d.r.Output(ctx, "dnf", "repoquery", "--userinstalled", "--qf", "%{name}\n"); err == nil {
		haveUser = true
		for _, n := range strings.Fields(string(o)) {
			userInst[n] = true
		}
	}
	unneeded := map[string]bool{}
	if o, err := s.d.r.Output(ctx, "dnf", "repoquery", "--unneeded", "--qf", "%{name}\n"); err == nil {
		for _, n := range strings.Fields(string(o)) {
			unneeded[n] = true
		}
	}
	pkgs := ParseRpmQa(string(raw))
	for _, it := range DnfItems(pkgs, userInst, haveUser, unneeded, s.d.protectSet(ctx), s.all) {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type rpmPkg struct {
	Name, Desc string
	Size       int64
	Installed  time.Time
	Requires   []string
	Provides   []string
}

func ParseRpmQa(raw string) map[string]*rpmPkg {
	pkgs := map[string]*rpmPkg{}
	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 6 || f[0] == "gpg-pubkey" {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		ts, _ := strconv.ParseInt(f[2], 10, 64)
		p := &rpmPkg{
			Name: f[0], Size: size, Desc: f[3],
			Installed: time.Unix(ts, 0),
			Requires:  splitCSV(f[4]), Provides: splitCSV(f[5]),
		}
		if old := pkgs[p.Name]; old != nil { // multilib / installonly twin
			old.Size += p.Size
			old.Requires = append(old.Requires, p.Requires...)
			old.Provides = append(old.Provides, p.Provides...)
			continue
		}
		pkgs[p.Name] = p
	}
	return pkgs
}

func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// DnfItems: a package is unused when no other installed package requires a
// capability it provides. ORPHAN = dnf's own --unneeded set; LEAF = user
// installed (or unknown reason) and unused. rpmlib() requires are ignored.
func DnfItems(pkgs map[string]*rpmPkg, userInst map[string]bool, haveUser bool, unneeded, protect map[string]bool, all bool) []core.Item {
	provBy := map[string][]string{}
	for n, p := range pkgs {
		provBy[n] = append(provBy[n], n)
		for _, c := range p.Provides {
			provBy[c] = append(provBy[c], n)
		}
	}
	rdeps := map[string]int{}
	for n, p := range pkgs {
		seen := map[string]bool{}
		for _, c := range p.Requires {
			if strings.HasPrefix(c, "rpmlib(") {
				continue
			}
			for _, prov := range provBy[c] {
				if prov != n && !seen[prov] {
					seen[prov] = true
					rdeps[prov]++
				}
			}
		}
	}
	var items []core.Item
	for n, p := range pkgs {
		if protect[n] || dnfProtectRE.MatchString(n) {
			continue
		}
		used := rdeps[n] > 0
		var tags []string
		cand := all
		switch {
		case unneeded[n]:
			tags, cand = append(tags, "ORPHAN"), true
		case !used && (userInst[n] || !haveUser):
			tags, cand = append(tags, "LEAF"), true
		}
		if !cand {
			continue
		}
		risk := core.Review
		if len(tags) > 0 && tags[0] == "ORPHAN" {
			risk = core.Safe
		}
		if used && !unneeded[n] {
			risk = core.Danger
		}
		items = append(items, core.Item{
			ID: n, Category: "Packages", Title: n, Desc: p.Desc,
			Size: p.Size, Risk: risk, Tags: tags,
			Date: p.Installed.Format("2 Jan 2006"),
		})
	}
	sortItems(items)
	return items
}
