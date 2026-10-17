package sandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

const fpApps = "com.brave.Browser\tx86_64\tstable\t518.3 MB\tsystem\torg.freedesktop.Platform/x86_64/25.08\tBrave\n" +
	"com.usebottles.bottles\tx86_64\tstable\t529.6 MB\tuser\torg.gnome.Platform/x86_64/50\tBottles\n"

const fpRts = "org.freedesktop.Platform\tx86_64\t25.08\t1.2 GB\tsystem\t\tFreedesktop Platform\n" +
	"org.freedesktop.Platform\tx86_64\t24.08\t1.1 GB\tsystem\t\tFreedesktop Platform\n" +
	"org.freedesktop.Platform.GL.default\tx86_64\t25.08\t400 MB\tsystem\t\tMesa\n" +
	"org.freedesktop.Platform.GL.default\tx86_64\t24.08\t390 MB\tsystem\t\tMesa\n" +
	"org.gnome.Platform\tx86_64\t50\t800 MB\tuser\t\tGNOME\n" +
	"org.gnome.Platform\tx86_64\t49\t790 MB\tuser\t\tGNOME\n"

func TestUnusedRuntimes(t *testing.T) {
	un := UnusedRuntimes(ParseFlatpakList(fpApps, true), ParseFlatpakList(fpRts, false))
	var got []string
	for _, r := range un {
		got = append(got, r.ID+"@"+r.Branch)
	}
	want := "org.freedesktop.Platform@24.08 org.freedesktop.Platform.GL.default@24.08 org.gnome.Platform@49"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q", strings.Join(got, " "))
	}
}

func TestFlatpakScanAndRemove(t *testing.T) {
	f := &runner.Fake{Out: map[string]string{
		"flatpak list --app " + flatpakCols:     fpApps,
		"flatpak list --runtime " + flatpakCols: fpRts,
	}}
	c := NewFlatpak(f)
	ch := make(chan core.Item, 32)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	if len(items) != 5 {
		t.Fatalf("items %d", len(items))
	}
	var sys, usr []core.Item
	for _, it := range items {
		if strings.HasPrefix(it.ID, "user ") {
			usr = append(usr, it)
		} else {
			sys = append(sys, it)
		}
	}
	if rm := c.Remove(sys); rm.Sudo == nil || rm.Sudo[2] != "--system" {
		t.Fatalf("sys removal %+v", rm)
	}
	var ran []string
	c.uninstall = func(_ context.Context, argv []string) error { ran = argv; return nil }
	rm := c.Remove(usr)
	if rm.Do == nil {
		t.Fatal("user removal must be Do")
	}
	res := rm.Do(context.Background())
	if len(res.Done) != len(usr) || ran[2] != "--user" {
		t.Fatalf("res %+v ran %v", res, ran)
	}
	mixed := c.Remove(items)
	if r := mixed.Do(context.Background()); len(r.Failed) != len(items) {
		t.Fatalf("mixed must fail all: %+v", r)
	}
}

func TestSnap(t *testing.T) {
	raw := `Name    Version  Rev   Tracking       Publisher  Notes
core22  20240111 1380  latest/stable  canonical  base
firefox 125.0    3836  latest/stable  mozilla    -
firefox 124.0    3779  latest/stable  mozilla    disabled
snapd   2.61     21465 latest/stable  canonical  snapd
`
	f := &runner.Fake{Out: map[string]string{"snap list --all": raw}}
	c := NewSnap(f)
	c.snapDir = t.TempDir()
	ch := make(chan core.Item, 8)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	if len(items) != 2 {
		t.Fatalf("items %+v", items)
	}
	rm := c.Remove(items)
	args := strings.Join(rm.Sudo[4:], " ")
	if args != "firefox 3779 firefox -" {
		t.Fatalf("order: %q", args)
	}
}
