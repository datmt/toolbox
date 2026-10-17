package pkgs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/runner"
)

// name, size, installtime, summary, requires, provides
var rpmFixture = strings.Join([]string{
	row("glibc", "1000", "1700000000", "libc", "rpmlib(X),", "libc.so.6()(64bit),"),
	row("orphan-lib", "2048", "1700000000", "unused dep", "libc.so.6()(64bit),", "liborphan.so.1()(64bit),"),
	row("leaf-app", "512", "1700000000", "user app", "libneeded.so.1()(64bit),libc.so.6()(64bit),", ""),
	row("needed-lib", "1024", "1700000000", "used", "libc.so.6()(64bit),", "libneeded.so.1()(64bit),"),
	row("auto-used", "10", "1700000000", "dep in use", "", "auto-cap,"),
	row("user-app2", "10", "1700000000", "needs auto", "auto-cap,", ""),
	row("gpg-pubkey", "0", "1700000000", "key", "", ""),
	"",
}, "\n")

func TestParseRpmQa(t *testing.T) {
	p := ParseRpmQa(rpmFixture)
	if _, ok := p["gpg-pubkey"]; ok {
		t.Fatal("gpg-pubkey parsed")
	}
	if p["orphan-lib"].Size != 2048 || len(p["leaf-app"].Requires) != 2 {
		t.Fatalf("bad parse %+v", p["leaf-app"])
	}
}

func TestDnfItems(t *testing.T) {
	pkgs := ParseRpmQa(rpmFixture)
	user := map[string]bool{"leaf-app": true, "user-app2": true}
	unneeded := map[string]bool{"orphan-lib": true}
	items := DnfItems(pkgs, user, true, unneeded, map[string]bool{"user-app2": true}, false)
	got := map[string]string{}
	for _, it := range items {
		got[it.ID] = it.Tags[0]
	}
	if len(got) != 2 || got["orphan-lib"] != "ORPHAN" || got["leaf-app"] != "LEAF" {
		t.Fatalf("got %v", got)
	}
	all := DnfItems(pkgs, user, true, unneeded, nil, true)
	for _, it := range all {
		if it.ID == "needed-lib" && it.Risk.String() != "danger" {
			t.Errorf("needed-lib risk %v", it.Risk)
		}
		if it.ID == "glibc" {
			t.Error("glibc offered")
		}
	}
}

func TestDnfProtectSet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dnf.conf"), []byte("# c\ndnf\nyum\n"), 0o644)
	f := &runner.Fake{Out: map[string]string{
		"uname -r": "6.8.5-300.fc40.x86_64\n",
		"rpm -qf /boot/vmlinuz-6.8.5-300.fc40.x86_64 --qf %{NAME}\n": "kernel-core\n",
	}}
	d := NewDnf(f)
	d.protectedDir = dir
	set := d.protectSet(context.Background())
	for _, n := range []string{"dnf", "yum", "kernel-core"} {
		if !set[n] {
			t.Errorf("%s missing: %v", n, set)
		}
	}
}

func TestDnfCmds(t *testing.T) {
	d := NewDnf(nil)
	if got := strings.Join(d.RemoveCmd([]string{"x"}), " "); got != "dnf remove x" {
		t.Error(got)
	}
	if c := d.DryRunCmd([]string{"x"}); c[len(c)-1] != "x" || c[0] != "sh" {
		t.Error(c)
	}
}
