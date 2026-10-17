package pkgs

import (
	"context"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/runner"
)

func row(f ...string) string { return strings.Join(f, "\t") }

// name, essential, priority, size KiB, depends, predepends, recommends, provides, status, summary
var dpkgFixture = strings.Join([]string{
	row("libc6", "yes", "required", "13000", "libgcc-s1", "", "", "", "ii ", "C library"),
	row("libgcc-s1", "", "optional", "100", "libc6 (>= 2.3)", "", "", "", "ii ", "gcc runtime"),
	row("bash", "yes", "required", "1500", "libc6", "", "", "", "ii ", "shell"),
	row("orphan-lib", "", "optional", "2048", "libc6", "", "", "", "ii ", "unused dep"),
	row("chain-lib", "", "optional", "10", "libc6", "", "", "", "ii ", "only orphan-app needs it"),
	row("orphan-app", "", "optional", "20", "chain-lib | other", "", "", "", "ii ", "auto app"),
	row("leaf-app", "", "optional", "512", "needed-lib", "", "", "", "ii ", "manual unused"),
	row("needed-lib", "", "optional", "1024", "libc6", "", "", "mail-thing", "ii ", "used"),
	row("mailer", "", "optional", "5", "mail-thing", "", "", "", "ii ", "needs provided virtual"),
	row("removed", "", "optional", "5", "", "", "", "", "rc ", "config only"),
	"",
}, "\n")

func TestParseDpkgQuery(t *testing.T) {
	p := ParseDpkgQuery(dpkgFixture)
	if _, ok := p["removed"]; ok {
		t.Fatal("rc package parsed")
	}
	if p["orphan-lib"].Size != 2048<<10 || !p["libc6"].Essential {
		t.Fatalf("bad parse: %+v", p["orphan-lib"])
	}
	if d := p["orphan-app"].Deps; len(d) != 2 || d[0] != "chain-lib" || d[1] != "other" {
		t.Fatalf("deps = %v", d)
	}
}

func TestAptItems(t *testing.T) {
	pkgs := ParseDpkgQuery(dpkgFixture)
	auto := map[string]bool{"orphan-lib": true, "chain-lib": true, "orphan-app": true, "needed-lib": true, "libgcc-s1": true}
	chain := ParseAptRemv("Remv orphan-lib [1]\nRemv orphan-app [1]\nRemv chain-lib:amd64 [1]\n")
	a := NewApt(&runner.Fake{})
	protect := a.protectSet(context.Background(), pkgs)
	if !protect["libc6"] || !protect["libgcc-s1"] || protect["orphan-lib"] {
		t.Fatalf("protect = %v", protect)
	}
	got := map[string]string{}
	for _, it := range AptItems(pkgs, auto, chain, protect, false) {
		got[it.ID] = it.Tags[0]
	}
	want := map[string]string{"orphan-lib": "ORPHAN", "orphan-app": "ORPHAN", "chain-lib": "ORPHAN", "leaf-app": "LEAF", "mailer": "LEAF"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q want %q (%v)", k, got[k], v, got)
		}
	}
}

func TestAptProtectKernelHold(t *testing.T) {
	f := &runner.Fake{Out: map[string]string{
		"apt-mark showhold": "held-pkg\n",
		"uname -r":          "6.8.0-1\n",
	}}
	set := NewApt(f).protectSet(context.Background(), map[string]*aptPkg{})
	for _, n := range []string{"held-pkg", "linux-image-6.8.0-1", "linux-modules-6.8.0-1"} {
		if !set[n] {
			t.Errorf("%s missing: %v", n, set)
		}
	}
}

func TestAptCmds(t *testing.T) {
	a := NewApt(nil)
	if got := strings.Join(a.RemoveCmd([]string{"x"}), " "); got != "apt-get purge x" {
		t.Error(got)
	}
	if got := strings.Join(a.DryRunCmd([]string{"x"}), " "); got != "apt-get -s purge x" {
		t.Error(got)
	}
}
