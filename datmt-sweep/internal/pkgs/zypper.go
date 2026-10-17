package pkgs

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// zypperProtectRE: floor on top of dnfProtectRE (which DnfItems applies) for
// openSUSE bits: package stack, release packages, patterns, bootloader.
var zypperProtectRE = regexp.MustCompile(`^(zypper.*|libzypp.*|openSUSE-.*|sles-release.*|SLE.*|patterns-.*|branding-.*|grub2.*|kernel-.*|snapper.*|btrfsprogs|aaa_base.*|udev|polkit|PackageKit)$`)

// Zypper reuses the rpm graph from the dnf backend: zypper has no
// repoquery, so the dependency data comes from rpm, "unneeded" from
// `zypper packages --unneeded`, and user-installed from /var/lib/zypp/AutoInstalled.
type Zypper struct {
	r             runner.Runner
	autoInstalled string
}

func NewZypper(r runner.Runner) *Zypper {
	return &Zypper{r: r, autoInstalled: "/var/lib/zypp/AutoInstalled"}
}

func (z *Zypper) Name() string { return "zypper" }

func (z *Zypper) Scanner(all bool) core.Scanner { return &zypperScanner{z: z, all: all} }

func (z *Zypper) RemoveCmd(pkgs []string) []string {
	return append([]string{"zypper", "remove", "--clean-deps"}, pkgs...)
}

// zypper --dry-run may need root for the lock; show errors in the preview
// instead of failing the pane.
func (z *Zypper) DryRunCmd(pkgs []string) []string {
	return append([]string{"sh", "-c", `zypper --non-interactive remove --clean-deps --dry-run "$@" 2>&1 || true`, "sh"}, pkgs...)
}

func (z *Zypper) InfoCmd(pkg string) []string { return []string{"rpm", "-qi", pkg} }

// ParseZypperPackages reads `zypper packages` table rows
// ("i | @System | name | version | arch") and returns the names.
func ParseZypperPackages(raw string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "|")
		if len(f) < 5 {
			continue
		}
		st, name := strings.TrimSpace(f[0]), strings.TrimSpace(f[2])
		if name == "" || name == "Name" || st == "S" || strings.HasPrefix(st, "-") {
			continue
		}
		set[name] = true
	}
	return set
}

// ParseAutoInstalled reads /var/lib/zypp/AutoInstalled (one name per line).
func ParseAutoInstalled(raw string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			set[l] = true
		}
	}
	return set
}

type zypperScanner struct {
	z   *Zypper
	all bool
}

func (s *zypperScanner) Name() string { return "Packages" }

func (s *zypperScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	raw, err := s.z.r.Output(ctx, "rpm", "-qa", "--qf", rpmFmt)
	if err != nil {
		return err
	}
	pkgs := ParseRpmQa(string(raw))
	unneeded := map[string]bool{}
	if o, err := s.z.r.Output(ctx, "zypper", "--non-interactive", "packages", "--unneeded"); err == nil {
		unneeded = ParseZypperPackages(string(o))
	}
	userInst, haveUser := map[string]bool{}, false
	if b, err := os.ReadFile(s.z.autoInstalled); err == nil {
		haveUser = true
		auto := ParseAutoInstalled(string(b))
		for n := range pkgs {
			if !auto[n] {
				userInst[n] = true
			}
		}
	}
	protect := map[string]bool{}
	for n := range pkgs {
		if zypperProtectRE.MatchString(n) {
			protect[n] = true
		}
	}
	if o, err := s.z.r.Output(ctx, "uname", "-r"); err == nil {
		rel := strings.TrimSpace(string(o))
		if q, err := s.z.r.Output(ctx, "rpm", "-qf", "/boot/vmlinuz-"+rel, "--qf", "%{NAME}\n"); err == nil {
			for _, n := range strings.Fields(string(q)) {
				protect[n] = true
			}
		}
	}
	for _, it := range DnfItems(pkgs, userInst, haveUser, unneeded, protect, s.all) {
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
