package pkgs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datmt/datmt-sweep/internal/runner"
)

const qiFixture = `Name            : orphan-lib
Description     : An unused dep
Installed Size  : 2.00 MiB
Install Date    : Tue 14 Oct 2026 09:31:00 AM UTC
Install Reason  : Installed as a dependency for another package
Required By     : None
Optional For    : None

Name            : leaf-app
Description     : Explicit, unused
Installed Size  : 512.00 KiB
Install Date    : Mon 13 Oct 2026 10:00:00 AM UTC
Install Reason  : Explicitly installed
Required By     : None
Optional For    : None

Name            : needed-lib
Description     : Used
Installed Size  : 1.00 GiB
Install Reason  : Installed as a dependency for another package
Required By     : leaf-app
Optional For    : None

Name            : opt-dep
Description     : Optional dep of something
Installed Size  : 1.00 MiB
Install Reason  : Installed as a dependency for another package
Required By     : None
Optional For    : leaf-app

Name            : linux
Description     : kernel
Installed Size  : 100.00 MiB
Install Reason  : Explicitly installed
Required By     : None
Optional For    : None
`

func TestParsePacmanQi(t *testing.T) {
	items := ParsePacmanQi(qiFixture, map[string]bool{"leaf-app": true}, nil, false)
	got := map[string]string{}
	for _, it := range items {
		got[it.ID] = it.Tags[0]
	}
	if len(got) != 2 || got["orphan-lib"] != "ORPHAN" || got["leaf-app"] != "LEAF" {
		t.Fatalf("unexpected candidates: %v", got)
	}
	for _, it := range items {
		if it.ID == "orphan-lib" && it.Size != 2<<20 {
			t.Errorf("size = %d", it.Size)
		}
		if it.ID == "leaf-app" && (len(it.Tags) != 2 || it.Tags[1] != "AUR") {
			t.Errorf("tags = %v", it.Tags)
		}
	}
}

func TestParsePacmanQiAll(t *testing.T) {
	items := ParsePacmanQi(qiFixture, nil, nil, true)
	// everything except protected "linux"
	if len(items) != 4 {
		t.Fatalf("got %d items", len(items))
	}
}

func TestParsePacmanQiDerivedProtect(t *testing.T) {
	items := ParsePacmanQi(qiFixture, nil, map[string]bool{"leaf-app": true}, true)
	for _, it := range items {
		if it.ID == "leaf-app" {
			t.Fatal("derived-protected package offered")
		}
	}
}

func TestProtectSet(t *testing.T) {
	dir := t.TempDir()
	kdir := filepath.Join(dir, "6.1.0-custom")
	os.MkdirAll(kdir, 0o755)
	kfile := filepath.Join(kdir, "pkgbase")
	os.WriteFile(kfile, []byte("linux-custom\n"), 0o644)

	f := &runner.Fake{Out: map[string]string{
		"pacman -Qgq base-devel": "autoconf\n",
		"pacman -Qi base":        "Name : base\nDepends On      : bash  coreutils>=9\n",
		"pacman-conf HoldPkg":    "pacman\nglibc\n",
		"pacman-conf IgnorePkg":  "foo\n",
		"pacman -Qoq " + kfile:   "linux-custom\n",
	}}
	p := NewPacman(f)
	p.modulesGlob = filepath.Join(dir, "*", "pkgbase")
	got := p.ProtectSet(context.Background())
	for _, n := range []string{"base", "bash", "coreutils", "autoconf", "pacman", "foo", "linux-custom"} {
		if !got[n] {
			t.Errorf("%s missing from protect set %v", n, got)
		}
	}
}
