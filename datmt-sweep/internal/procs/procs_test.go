package procs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
)

func mkproc(t *testing.T, root string, pid int, comm string, ppid int, ticks int, cgroup string) {
	d := filepath.Join(root, fmt.Sprint(pid))
	os.MkdirAll(d, 0o755)
	set := func(n, s string) { os.WriteFile(filepath.Join(d, n), []byte(s), 0o644) }
	set("stat", fmt.Sprintf("%d (%s) S %d 1 1 0 -1 0 0 0 0 0 %d 0 0 0\n", pid, comm, ppid, ticks))
	set("cmdline", comm+"\x00--flag\x00")
	set("cgroup", "0::"+cgroup+"\n")
	set("status", "voluntary_ctxt_switches:\t10\nnonvoluntary_ctxt_switches:\t0\n")
}

func TestScanAndKill(t *testing.T) {
	root := t.TempDir()
	mkproc(t, root, 100, "firefox", 1, 0, "/user.slice/app-firefox.scope")
	mkproc(t, root, 101, "daemon", 1, 0, "/system.slice/x.service")
	c := New()
	c.proc, c.cgroup, c.supply, c.window = root, t.TempDir(), t.TempDir(), 40*time.Millisecond
	go func() {
		time.Sleep(10 * time.Millisecond)
		mkproc(t, root, 100, "firefox", 1, 50, "/user.slice/app-firefox.scope")
		mkproc(t, root, 101, "daemon", 1, 50, "/system.slice/x.service")
	}()
	ch := make(chan core.Item, 8)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	if len(items) != 1 || items[0].ID != "100" {
		t.Fatalf("items = %+v", items)
	}
	var sent []int
	c.kill = func(pid int, _ syscall.Signal) error { sent = append(sent, pid); return nil }
	// scanned item has uid of the temp dir (== us), so OWN
	res := c.Remove(items).Do(context.Background())
	if len(sent) != 1 || sent[0] != 100 || len(res.Done) != 1 {
		t.Errorf("sent=%v res=%+v", sent, res)
	}
	other := core.Item{ID: "55", Tags: []string{"UID0"}}
	if got := strings.Join(c.Remove([]core.Item{other}).Sudo, " "); got != "kill -TERM 55" {
		t.Errorf("sudo = %q", got)
	}
}
