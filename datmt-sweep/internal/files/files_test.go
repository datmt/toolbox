package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
)

func TestGuard(t *testing.T) {
	g := NewGuard("/home/u")
	ok := []string{"/home/u/Downloads/x.iso", "/home/u/.cache/foo", "/home/u/.config/app"}
	bad := []string{"/home/u", "/home/u/.config", "/home/u/.cache", "/home/u/.ssh/id_rsa", "/home/u/.gnupg", "/etc/passwd", "/home/uu/x", "rel/path", "/home/u/../etc/passwd"}
	for _, p := range ok {
		if err := g.Check(p); err != nil {
			t.Errorf("%s should pass: %v", p, err)
		}
	}
	for _, p := range bad {
		if err := g.Check(p); err == nil {
			t.Errorf("%s should be refused", p)
		}
	}
}

func TestTrashRoundTrip(t *testing.T) {
	d := t.TempDir()
	trash := filepath.Join(d, "Trash")
	for i := 0; i < 2; i++ { // second one collides on name
		p := filepath.Join(d, "src", "big.bin")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		if err := Trash(trash, p); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("source still there")
		}
	}
	for _, n := range []string{"big.bin", "big.bin.1"} {
		if _, err := os.Stat(filepath.Join(trash, "files", n)); err != nil {
			t.Errorf("missing %s: %v", n, err)
		}
		b, err := os.ReadFile(filepath.Join(trash, "info", n+".trashinfo"))
		if err != nil || !strings.Contains(string(b), "Path="+d+"/src/big.bin") {
			t.Errorf("bad info for %s: %q %v", n, b, err)
		}
	}
}

func scanAll(t *testing.T, sc core.Scanner) []core.Item {
	t.Helper()
	ch := make(chan core.Item, 100)
	if err := sc.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var out []core.Item
	for it := range ch {
		out = append(out, it)
	}
	return out
}

func TestBigFilesScanAndRemove(t *testing.T) {
	home := t.TempDir()
	w := func(rel string, n int) string {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, n), 0o644)
		return p
	}
	big := w("Videos/a.mkv", 2000)
	w("Videos/small.txt", 10)
	w(".cache/huge", 5000)
	w("proj/.git/objects/pack", 5000)
	w(".ssh/key", 5000)

	trash := filepath.Join(home, ".local/share/Trash")
	cat := BigFiles(home, 1000, NewGuard(home), trash)
	items := scanAll(t, cat.Scanner())
	if len(items) != 1 || items[0].ID != big {
		t.Fatalf("items = %+v", items)
	}
	res := cat.Remove(items).Do(context.Background())
	if len(res.Done) != 1 || len(res.Failed) != 0 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(big); !os.IsNotExist(err) {
		t.Error("file not moved")
	}
}

func TestRemoveRefusesOutsideGuard(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	victim := filepath.Join(other, "keep")
	os.WriteFile(victim, []byte("x"), 0o644)
	cat := BigFiles(home, 0, NewGuard(home), filepath.Join(home, "T"))
	res := cat.Remove([]core.Item{{ID: victim}}).Do(context.Background())
	if len(res.Failed) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("victim was touched")
	}
}

func TestCachesAndTrashBin(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".cache")
	os.MkdirAll(filepath.Join(cache, "mozilla"), 0o755)
	os.WriteFile(filepath.Join(cache, "mozilla", "f"), make([]byte, 300), 0o644)
	cc := Caches(cache, NewGuard(home))
	items := scanAll(t, cc.Scanner())
	if len(items) != 1 || items[0].Size != 300 {
		t.Fatalf("items = %+v", items)
	}
	if r := cc.Remove(items).Do(context.Background()); len(r.Done) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(cache, "mozilla")); !os.IsNotExist(err) {
		t.Error("cache not removed")
	}

	trash := filepath.Join(home, "Trash")
	src := filepath.Join(home, "doc.txt")
	os.WriteFile(src, make([]byte, 50), 0o644)
	if err := Trash(trash, src); err != nil {
		t.Fatal(err)
	}
	tc := TrashBin(trash)
	items = scanAll(t, tc.Scanner())
	if len(items) != 1 || items[0].Size != 50 || items[0].Date == "" {
		t.Fatalf("items = %+v", items)
	}
	if r := tc.Remove([]core.Item{{ID: "../etc"}}).Do(context.Background()); len(r.Failed) != 1 {
		t.Fatal("traversal id accepted")
	}
	if r := tc.Remove(items).Do(context.Background()); len(r.Done) != 1 {
		t.Fatalf("%+v", r)
	}
	if left := scanAll(t, tc.Scanner()); len(left) != 0 {
		t.Fatalf("left = %+v", left)
	}
}

func TestBigFilesPermanentDelete(t *testing.T) {
	home := t.TempDir()
	trash := filepath.Join(home, "T")
	big := filepath.Join(home, "big.bin")
	os.WriteFile(big, make([]byte, 2000), 0o644)
	dir := filepath.Join(home, "adir")
	os.Mkdir(dir, 0o755)
	outside := filepath.Join(t.TempDir(), "keep")
	os.WriteFile(outside, []byte("x"), 0o644)

	cat := BigFiles(home, 1000, NewGuard(home), trash)
	pr, ok := cat.(core.PermanentRemover)
	if !ok {
		t.Fatal("BigFiles is not a PermanentRemover")
	}
	if _, ok := Caches(home, NewGuard(home)).(core.PermanentRemover); ok {
		t.Error("Caches must not offer a second, identical delete")
	}
	out, err := pr.DryRunPermanent(context.Background(), []core.Item{{ID: big, Size: 2000, Desc: big}})
	if err != nil || !strings.Contains(out, "PERMANENT") {
		t.Errorf("dry run = %q, %v", out, err)
	}
	res := pr.RemovePermanent([]core.Item{{ID: big}, {ID: dir}, {ID: outside}}).Do(context.Background())
	if len(res.Done) != 1 || res.Done[0] != big || len(res.Failed) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Lstat(big); !os.IsNotExist(err) {
		t.Error("big file still there")
	}
	if _, err := os.Stat(trash); err == nil {
		t.Error("permanent delete must not touch trash")
	}
	for _, p := range []string{dir, outside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was touched", p)
		}
	}
}
