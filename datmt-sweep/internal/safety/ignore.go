// Package safety holds the user-facing guard rails: the ignore list and the
// audit log of everything that was removed.
package safety

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Ignore is the per-category list of items that must never be offered.
//
// File format (a TOML subset):
//
//	[ignore]
//	Packages = ["firefox", "linux-*"]
//	"Big files" = ["/home/me/VMs"]
//
// Patterns match the item ID with path.Match globbing; a pattern that is an
// absolute path also covers everything below it.
type Ignore struct {
	mu   sync.Mutex
	path string
	m    map[string][]string
}

// DefaultIgnorePath is ~/.config/datmt-sweep/ignore.toml (XDG aware).
func DefaultIgnorePath(home string) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "datmt-sweep", "ignore.toml")
}

// LoadIgnore reads path; a missing file is an empty list.
func LoadIgnore(p string) (*Ignore, error) {
	ig := &Ignore{path: p, m: map[string][]string{}}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return ig, nil
	}
	if err != nil {
		return ig, err
	}
	ig.m, err = parseIgnore(string(b))
	if ig.m == nil {
		ig.m = map[string][]string{}
	}
	return ig, err
}

func parseIgnore(s string) (map[string][]string, error) {
	out := map[string][]string{}
	var key string
	var buf strings.Builder
	inArr := false
	flush := func(lineNo int) error {
		vals, err := parseArray(buf.String())
		if err != nil {
			return fmt.Errorf("ignore.toml line %d: %w", lineNo, err)
		}
		out[key] = append(out[key], vals...)
		buf.Reset()
		inArr = false
		return nil
	}
	for i, line := range strings.Split(s, "\n") {
		line = stripComment(line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if inArr {
			buf.WriteString(" " + line)
			if strings.Contains(line, "]") {
				if err := flush(i + 1); err != nil {
					return out, err
				}
			}
			continue
		}
		if strings.HasPrefix(line, "[") && !strings.Contains(line, "=") {
			continue // [ignore] header
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return out, fmt.Errorf("ignore.toml line %d: expected key = [..]", i+1)
		}
		k = strings.TrimSpace(k)
		if uq, err := strconv.Unquote(k); err == nil {
			k = uq
		}
		key = k
		buf.WriteString(strings.TrimSpace(v))
		inArr = true
		if strings.Contains(v, "]") {
			if err := flush(i + 1); err != nil {
				return out, err
			}
		}
	}
	if inArr {
		return out, fmt.Errorf("ignore.toml: unterminated array for %q", key)
	}
	return out, nil
}

// stripComment drops a trailing # comment that is outside quotes.
func stripComment(l string) string {
	q := false
	for i, r := range l {
		switch {
		case r == '"':
			q = !q
		case r == '#' && !q:
			return l[:i]
		}
	}
	return l
}

func parseArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("value must be an array of strings")
	}
	s = strings.TrimSpace(s[1 : len(s)-1])
	var out []string
	for s != "" {
		if s[0] != '"' {
			return nil, fmt.Errorf("array entries must be quoted strings")
		}
		end := 1
		for end < len(s) && (s[end] != '"' || s[end-1] == '\\') {
			end++
		}
		if end >= len(s) {
			return nil, fmt.Errorf("unterminated string")
		}
		v, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		s = strings.TrimLeft(strings.TrimSpace(s[end+1:]), ",")
		s = strings.TrimSpace(s)
	}
	return out, nil
}

// Match reports whether id in category cat is ignored.
func (ig *Ignore) Match(cat, id string) bool {
	if ig == nil {
		return false
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	for _, pat := range ig.m[cat] {
		if pat == id {
			return true
		}
		if ok, _ := path.Match(pat, id); ok {
			return true
		}
		if strings.HasPrefix(pat, "/") && strings.HasPrefix(id, strings.TrimSuffix(pat, "/")+"/") {
			return true
		}
	}
	return false
}

// Add ignores id in cat and persists the list.
func (ig *Ignore) Add(cat, id string) error {
	if ig == nil {
		return fmt.Errorf("no ignore list configured")
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	for _, p := range ig.m[cat] {
		if p == id {
			return nil
		}
	}
	ig.m[cat] = append(ig.m[cat], id)
	return ig.saveLocked()
}

func (ig *Ignore) saveLocked() error {
	if ig.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(ig.path), 0o755); err != nil {
		return err
	}
	cats := make([]string, 0, len(ig.m))
	for c := range ig.m {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var b strings.Builder
	b.WriteString("# datmt-sweep ignore list: items listed here are never offered.\n[ignore]\n")
	for _, c := range cats {
		q := make([]string, len(ig.m[c]))
		for i, v := range ig.m[c] {
			q[i] = strconv.Quote(v)
		}
		fmt.Fprintf(&b, "%s = [%s]\n", strconv.Quote(c), strings.Join(q, ", "))
	}
	tmp := ig.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ig.path)
}
