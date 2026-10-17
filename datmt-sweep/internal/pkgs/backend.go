// Package pkgs holds per-distro package backends.
package pkgs

import (
	"bufio"
	"os"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

// Backend is the only code that talks to a package manager.
type Backend interface {
	Name() string
	// Scanner lists removable packages. all=true offers every package.
	Scanner(all bool) core.Scanner
	// RemoveCmd builds the privileged removal command (no sudo prefix).
	RemoveCmd(pkgs []string) []string
	// DryRunCmd builds the command that prints what RemoveCmd would remove.
	DryRunCmd(pkgs []string) []string
	// InfoCmd prints details for one package (preview pane).
	InfoCmd(pkg string) []string
}

// Detect picks a backend from /etc/os-release ID / ID_LIKE.
func Detect(r runner.Runner) Backend {
	ids := osReleaseIDs("/etc/os-release")
	for _, id := range ids {
		switch id {
		case "arch", "manjaro", "endeavouros", "cachyos", "garuda":
			return NewPacman(r)
		case "fedora", "rhel", "centos", "rocky", "almalinux", "ol", "nobara", "amzn":
			return NewDnf(r)
		case "opensuse", "opensuse-leap", "opensuse-tumbleweed", "opensuse-slowroll", "sles", "sle-micro", "suse":
			return NewZypper(r)
		case "debian", "ubuntu", "linuxmint", "pop", "raspbian", "kali", "elementary", "zorin":
			return NewApt(r)
		}
	}
	return nil
}

func osReleaseIDs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ids []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		for _, key := range []string{"ID=", "ID_LIKE="} {
			if v, ok := strings.CutPrefix(line, key); ok {
				ids = append(ids, strings.Fields(strings.Trim(v, `"'`))...)
			}
		}
	}
	return ids
}
