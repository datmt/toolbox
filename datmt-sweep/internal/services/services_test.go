package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

func TestScanRankAndRemove(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, cpu string) {
		d := filepath.Join(root, rel)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "cpu.stat"), []byte("usage_usec "+cpu+"\n"), 0o644)
	}
	mk("system.slice/busy.service", "0")
	mk("system.slice/sshd.service", "0")
	mk("user.slice/user-1000.slice/user@1000.service/app.slice/mine.service", "0")
	os.WriteFile(filepath.Join(root, "cpu.stat"), []byte("usage_usec 0\n"), 0o644)

	c := New(&runner.Fake{})
	c.cgroup, c.supply, c.window = root, t.TempDir(), 50*time.Millisecond
	go func() { // bump counters mid-window
		time.Sleep(15 * time.Millisecond)
		os.WriteFile(filepath.Join(root, "system.slice/busy.service/cpu.stat"), []byte("usage_usec 90000\n"), 0o644)
		os.WriteFile(filepath.Join(root, "system.slice/sshd.service/cpu.stat"), []byte("usage_usec 20000\n"), 0o644)
		os.WriteFile(filepath.Join(root, "user.slice/user-1000.slice/user@1000.service/app.slice/mine.service/cpu.stat"), []byte("usage_usec 5000\n"), 0o644)
		os.WriteFile(filepath.Join(root, "cpu.stat"), []byte("usage_usec 200000\n"), 0o644)
	}()
	ch := make(chan core.Item, 16)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	if len(items) != 3 || items[0].ID != "system:busy.service" || items[1].Risk != core.Danger || items[2].ID != "user:mine.service" {
		t.Fatalf("items = %+v", items)
	}
	rm := c.Remove(items[:1])
	if got := strings.Join(rm.Sudo, " "); got != "systemctl stop busy.service" {
		t.Errorf("cmd = %q", got)
	}
	f := &runner.Fake{}
	c.r = f
	res := c.Remove(items[2:]).Do(context.Background())
	if len(res.Done) != 1 || len(f.Calls) != 1 || strings.Join(f.Calls[0], " ") != "systemctl --user stop mine.service" {
		t.Errorf("res=%+v calls=%v", res, f.Calls)
	}
}
