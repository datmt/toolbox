package ports

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
)

const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 222 1 0 100 0 0 10 0
   2: 0100007F:1F90 0100007F:D2A0 06 00000000:00000000 00:00000000 00000000     0        0 0 1 0 100 0 0 10 0
`
const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1 0 100 0 0 10 0
`

func TestParseNet(t *testing.T) {
	s := ParseNet("tcp", tcp4)
	if len(s) != 2 || s[0].Local != "127.0.0.1:8080" || s[1].Local != "0.0.0.0:22" || s[0].State != "LISTEN" {
		t.Fatalf("%+v", s)
	}
	s6 := ParseNet("tcp6", tcp6)
	if len(s6) != 1 || s6[0].Local != "[::1]:80" || !s6[0].Addr.IsLoopback() {
		t.Fatalf("%+v", s6)
	}
}

func setup(t *testing.T) string {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "net"), 0o755)
	os.WriteFile(filepath.Join(root, "net/tcp"), []byte(tcp4), 0o644)
	os.WriteFile(filepath.Join(root, "net/tcp6"), []byte(tcp6), 0o644)
	// pid 500 owns inode 111 (own user process); sshd (unit) owns 222 not visible
	d := filepath.Join(root, "500")
	os.MkdirAll(filepath.Join(d, "fd"), 0o755)
	os.Symlink("socket:[111]", filepath.Join(d, "fd/3"))
	os.WriteFile(filepath.Join(d, "stat"), []byte("500 (myserver) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0\n"), 0o644)
	os.WriteFile(filepath.Join(d, "cmdline"), []byte("myserver\x00-p\x008080\x00"), 0o644)
	os.WriteFile(filepath.Join(d, "cgroup"), []byte("0::/user.slice/app.scope\n"), 0o644)
	os.WriteFile(filepath.Join(d, "status"), nil, 0o644)
	return root
}

func TestScanAndKill(t *testing.T) {
	c := New()
	c.proc = setup(t)
	ch := make(chan core.Item, 16)
	if err := c.Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var items []core.Item
	for it := range ch {
		items = append(items, it)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	var mine, hidden core.Item
	for _, it := range items {
		if strings.Contains(it.Title, "myserver") {
			mine = it
		}
		if it.Title == "0.0.0.0:22 (owner hidden)" {
			hidden = it
		}
	}
	if mine.ID == "" || hidden.Risk != core.Danger {
		t.Fatalf("mine=%+v hidden=%+v", mine, hidden)
	}
	// own uid is whoever runs the test; the fake proc dir is owned by us
	var sent []int
	c.kill = func(pid int, _ syscall.Signal) error { sent = append(sent, pid); return nil }
	res := c.Remove([]core.Item{mine}).Do(context.Background())
	if len(sent) != 1 || sent[0] != 500 || len(res.Done) != 1 {
		t.Fatalf("sent=%v res=%+v", sent, res)
	}
	if _, err := c.DryRun(context.Background(), []core.Item{hidden}); err == nil {
		t.Error("hidden-owner item accepted")
	}
	// system unit owned by root -> sudo systemctl stop
	svc := core.Item{ID: "x", Desc: "@@pid=900 uid=0 unit=foo.service"}
	rm := c.Remove([]core.Item{svc})
	if len(rm.Sudo) != 3 || !strings.Contains(rm.Sudo[2], "systemctl stop 'foo.service'") {
		t.Errorf("sudo = %v", rm.Sudo)
	}
}
