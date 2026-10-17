package pkgs

import "testing"

const zypperUnneeded = `Loading repository data...
Reading installed packages...

S | Repository | Name        | Version | Arch
--+------------+-------------+---------+-------
i | @System    | libfoo1     | 1.0-1.1 | x86_64
i | @System    | old-helper  | 2.3-4.2 | noarch
`

func TestParseZypperPackages(t *testing.T) {
	got := ParseZypperPackages(zypperUnneeded)
	if len(got) != 2 || !got["libfoo1"] || !got["old-helper"] {
		t.Fatalf("got %v", got)
	}
}

func TestParseAutoInstalled(t *testing.T) {
	got := ParseAutoInstalled("# c\nlibfoo1\n\nold-helper\n")
	if len(got) != 2 || !got["libfoo1"] {
		t.Fatalf("got %v", got)
	}
}

func TestZypperCmds(t *testing.T) {
	z := NewZypper(nil)
	if c := z.RemoveCmd([]string{"a", "b"}); c[0] != "zypper" || c[len(c)-1] != "b" {
		t.Fatalf("%v", c)
	}
	if c := z.DryRunCmd([]string{"a"}); c[0] != "sh" || c[len(c)-1] != "a" {
		t.Fatalf("%v", c)
	}
}
