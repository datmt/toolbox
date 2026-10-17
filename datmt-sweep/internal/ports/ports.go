// Package ports lists TCP/UDP sockets with their owning process and offers
// to stop the owner.
package ports

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/power"
	"github.com/datmt/datmt-sweep/internal/services"
)

// Sock is one parsed /proc/net row.
type Sock struct {
	Proto  string // tcp, tcp6, udp, udp6
	Local  string // addr:port
	Remote string
	State  string // LISTEN, ESTAB, OPEN (bound UDP)
	Addr   net.IP
	Inode  string
}

var protectedComms = map[string]bool{
	"sshd": true, "Xorg": true, "Xwayland": true, "dbus-daemon": true, "dbus-broker": true,
	"systemd": true, "systemd-resolve": true, "gnome-shell": true, "NetworkManager": true,
}

func parseAddr(s string, v6 bool) (net.IP, int, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	b, err := hex.DecodeString(a)
	port, err2 := strconv.ParseUint(p, 16, 16)
	if err != nil || err2 != nil {
		return nil, 0, false
	}
	switch {
	case !v6 && len(b) == 4:
		return net.IPv4(b[3], b[2], b[1], b[0]), int(port), true
	case v6 && len(b) == 16:
		ip := make(net.IP, 16)
		for i := 0; i < 4; i++ { // four little-endian 32-bit words
			binary.BigEndian.PutUint32(ip[i*4:], binary.LittleEndian.Uint32(b[i*4:]))
		}
		return ip, int(port), true
	}
	return nil, 0, false
}

// ParseNet parses one /proc/net/{tcp,tcp6,udp,udp6} file body.
func ParseNet(proto, body string) []Sock {
	v6 := strings.HasSuffix(proto, "6")
	var out []Sock
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		lip, lp, ok1 := parseAddr(f[1], v6)
		rip, rp, ok2 := parseAddr(f[2], v6)
		if !ok1 || !ok2 {
			continue
		}
		s := Sock{Proto: proto, Addr: lip, Inode: f[9]}
		s.Local = net.JoinHostPort(lip.String(), strconv.Itoa(lp))
		s.Remote = net.JoinHostPort(rip.String(), strconv.Itoa(rp))
		isTCP := strings.HasPrefix(proto, "tcp")
		switch {
		case isTCP && f[3] == "0A":
			s.State = "LISTEN"
		case isTCP && f[3] == "01":
			s.State = "ESTAB"
		case !isTCP && f[3] == "07":
			s.State = "OPEN"
		case !isTCP && f[3] == "01":
			s.State = "ESTAB"
		default:
			continue // TIME_WAIT etc: nothing to kill
		}
		out = append(out, s)
	}
	return out
}

type Category struct {
	proc string
	uid  uint32
	kill func(int, syscall.Signal) error
}

func New() *Category {
	return &Category{proc: "/proc", uid: uint32(os.Getuid()),
		kill: func(pid int, s syscall.Signal) error { return syscall.Kill(pid, s) }}
}

func (c *Category) Name() string          { return "Ports" }
func (c *Category) Scanner() core.Scanner { return c }

// inodeOwners maps socket inode -> pid via /proc/PID/fd. Other users' fds
// are unreadable without root; those sockets simply have no owner.
func (c *Category) inodeOwners() map[string]int {
	out := map[string]int{}
	ents, _ := os.ReadDir(c.proc)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(c.proc, e.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join(c.proc, e.Name(), "fd", fd.Name()))
			if err == nil && strings.HasPrefix(l, "socket:[") {
				out[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = pid
			}
		}
	}
	return out
}

// unitOf returns the innermost .service in a cgroup path, and whether it is
// a system (not user-manager) unit.
func unitOf(cg string) (unit string, system bool) {
	for _, part := range strings.Split(cg, "/") {
		if strings.HasSuffix(part, ".service") && !strings.HasPrefix(part, "user@") {
			unit = part
		}
	}
	return unit, unit != "" && !strings.Contains(cg, "/user@")
}

func exposure(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "LOCAL"
	case ip.IsUnspecified():
		return "EXPOSED"
	}
	return "BOUND"
}

func (c *Category) Scan(ctx context.Context, out chan<- core.Item) error {
	var socks []Sock
	for _, p := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile(filepath.Join(c.proc, "net", p))
		if err != nil {
			continue
		}
		socks = append(socks, ParseNet(p, string(b))...)
	}
	owners := c.inodeOwners()
	procs := power.SnapshotProcs(c.proc)

	sort.SliceStable(socks, func(i, j int) bool { return socks[i].Local < socks[j].Local })
	for _, s := range socks {
		it := core.Item{
			ID:       s.Proto + ":" + s.Local + ">" + s.Remote + "#" + s.Inode,
			Category: "Ports",
			Risk:     core.Review,
		}
		kind := strings.ToUpper(strings.TrimSuffix(s.Proto, "6"))
		it.Tags = []string{kind, s.State, exposure(s.Addr)}
		it.Metric = s.Local
		pid, owned := owners[s.Inode]
		if !owned {
			it.Title = s.Local + " (owner hidden)"
			it.Desc = "owner not visible without root\nremote " + s.Remote
			it.Risk = core.Danger // cannot act on it; never bulk-selected
			it.Tags = append(it.Tags, "NOPID")
		} else {
			p := procs[pid]
			unit, _ := unitOf(p.Cgroup)
			it.Title = fmt.Sprintf("%s %s (%d)", p.Comm, s.Local, pid)
			it.Desc = fmt.Sprintf("pid %d uid %d\n%s\nunit %s\nremote %s", pid, p.UID, p.Cmdline, orDash(unit), s.Remote)
			if unit != "" && services.Protected(unit) || protectedComms[p.Comm] {
				it.Risk = core.Danger
				it.Tags = append(it.Tags, "PROTECTED")
			}
			if p.UID != c.uid {
				it.Tags = append(it.Tags, "OTHER")
			}
			it.Desc += "\n\n" + pidMarker(pid, p.UID, unit)
		}
		select {
		case out <- it:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// pidMarker embeds machine-readable owner data at the end of Desc so
// Remove can act without rescanning.
func pidMarker(pid int, uid uint32, unit string) string {
	return fmt.Sprintf("@@pid=%d uid=%d unit=%s", pid, uid, unit)
}

type target struct {
	pid  int
	uid  uint32
	unit string // system unit to stop instead of killing pid
}

func parseTarget(it core.Item) (target, bool) {
	i := strings.LastIndex(it.Desc, "@@pid=")
	if i < 0 {
		return target{}, false
	}
	var t target
	for _, kv := range strings.Fields(it.Desc[i+2:]) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "pid":
			t.pid, _ = strconv.Atoi(v)
		case "uid":
			n, _ := strconv.ParseUint(v, 10, 32)
			t.uid = uint32(n)
		case "unit":
			t.unit = v
		}
	}
	return t, t.pid > 1
}

func (c *Category) Info(_ context.Context, it core.Item) string {
	d := it.Desc
	if i := strings.LastIndex(d, "\n\n@@pid="); i >= 0 {
		d = d[:i]
	}
	s := it.Title + "\n\n" + d
	if units, pids, _, _ := c.plan([]core.Item{it}); len(units) > 0 {
		s += "\n\nStops the unit (killing the process would just get it restarted)."
	} else if len(pids) > 0 {
		s += "\n\nSends SIGTERM to the process."
	}
	return s
}

// plan groups targets: system units to stop, pids to signal, and whether
// everything is doable unprivileged.
func (c *Category) plan(items []core.Item) (units []string, pids []int, needSudo bool, skipped []string) {
	seenU, seenP := map[string]bool{}, map[int]bool{}
	for _, it := range items {
		t, ok := parseTarget(it)
		if !ok {
			skipped = append(skipped, it.Title)
			continue
		}
		if t.unit != "" && t.uid != c.uid { // root/system service: stop unit via sudo
			if !seenU[t.unit] {
				seenU[t.unit] = true
				units = append(units, t.unit)
			}
			needSudo = true
			continue
		}
		if !seenP[t.pid] {
			seenP[t.pid] = true
			pids = append(pids, t.pid)
		}
		if t.uid != c.uid {
			needSudo = true
		}
	}
	return
}

func (c *Category) DryRun(_ context.Context, items []core.Item) (string, error) {
	units, pids, sudo, skipped := c.plan(items)
	if len(units)+len(pids) == 0 {
		return "", fmt.Errorf("no actionable sockets selected (owner not visible?)")
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "  %s  [%s]\n", it.Title, strings.Join(it.Tags, " "))
	}
	b.WriteString("\n")
	if len(units) > 0 {
		fmt.Fprintf(&b, "sudo systemctl stop %s\n", strings.Join(units, " "))
	}
	if len(pids) > 0 {
		pre := ""
		if sudo {
			pre = "sudo "
		}
		fmt.Fprintf(&b, "%skill -TERM %s\n", pre, strings.Join(pidStrs(pids), " "))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "\nskipped (owner hidden): %s\n", strings.Join(skipped, ", "))
	}
	b.WriteString("\nSIGTERM only; a process that ignores it stays up.\n")
	return b.String(), nil
}

func pidStrs(p []int) []string {
	s := make([]string, len(p))
	for i, v := range p {
		s[i] = strconv.Itoa(v)
	}
	return s
}

func shellQuote(a string) string { return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'" }

func (c *Category) Remove(items []core.Item) core.Removal {
	units, pids, sudo, _ := c.plan(items)
	if !sudo {
		return core.Removal{
			Summary: "SIGTERM " + strings.Join(pidStrs(pids), " "),
			Do: func(context.Context) core.Result {
				res := core.Result{Failed: map[string]error{}}
				done := map[int]bool{}
				for _, it := range items {
					t, ok := parseTarget(it)
					if !ok {
						continue
					}
					if !done[t.pid] {
						done[t.pid] = true
						if err := c.kill(t.pid, syscall.SIGTERM); err != nil {
							res.Failed[it.ID] = err
							done[t.pid] = false
							continue
						}
					} else if res.Failed[it.ID] != nil {
						continue
					}
					res.Done = append(res.Done, it.ID)
				}
				return res
			},
		}
	}
	var cmds []string
	if len(units) > 0 {
		q := []string{"systemctl", "stop"}
		for _, u := range units {
			q = append(q, shellQuote(u))
		}
		cmds = append(cmds, strings.Join(q, " "))
	}
	if len(pids) > 0 {
		cmds = append(cmds, "kill -TERM "+strings.Join(pidStrs(pids), " "))
	}
	return core.Removal{
		Summary: "sudo " + strings.Join(cmds, "; "),
		Sudo:    []string{"sh", "-c", strings.Join(cmds, "; ")},
	}
}
