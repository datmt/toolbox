package safety

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreParseMatch(t *testing.T) {
	src := `# c
[ignore]
Packages = ["firefox", "linux-*"] # trailing
"Big files" = [
  "/home/me/VMs",
  "/home/me/a#b.iso",
]
`
	m, err := parseIgnore(src)
	if err != nil {
		t.Fatal(err)
	}
	ig := &Ignore{m: m}
	for _, c := range []struct {
		cat, id string
		want    bool
	}{
		{"Packages", "firefox", true},
		{"Packages", "linux-lts", true},
		{"Packages", "vim", false},
		{"Big files", "/home/me/VMs/disk.img", true},
		{"Big files", "/home/me/VMsX/disk.img", false},
		{"Big files", "/home/me/a#b.iso", true},
		{"Caches", "firefox", false},
	} {
		if got := ig.Match(c.cat, c.id); got != c.want {
			t.Errorf("Match(%q,%q)=%v want %v", c.cat, c.id, got, c.want)
		}
	}
}

func TestIgnoreAddRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "ignore.toml")
	ig, err := LoadIgnore(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ig.Add("Big files", "/x/y z"); err != nil {
		t.Fatal(err)
	}
	ig.Add("Packages", "vim")
	ig2, err := LoadIgnore(p)
	if err != nil {
		t.Fatal(err)
	}
	if !ig2.Match("Big files", "/x/y z") || !ig2.Match("Packages", "vim") {
		t.Fatalf("round trip lost entries: %v", ig2.m)
	}
}

func TestIgnoreBadFile(t *testing.T) {
	if _, err := parseIgnore("Packages = [foo]"); err == nil {
		t.Fatal("want error for unquoted entry")
	}
}

func TestAuditAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "actions.log")
	a := NewAudit(p)
	a.Log(Record{Event: "confirm", Category: "Caches", Items: []string{"a"}})
	a.Log(Record{Event: "result", Category: "Caches", Done: []string{"a"}})
	f, _ := os.Open(p)
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.Time.IsZero() {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("lines = %d", n)
	}
	var nilA *Audit
	if err := nilA.Log(Record{}); err != nil {
		t.Fatal(err)
	}
}
