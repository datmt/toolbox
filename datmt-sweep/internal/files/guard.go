// Package files holds the filesystem categories: big files, caches, trash.
package files

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Guard refuses deletes outside known roots or on protected paths.
type Guard struct {
	roots []string
	exact []string // never removable themselves
	deep  []string // never removable, nor anything inside
}

// NewGuard allows paths strictly inside home, minus the usual crown jewels.
func NewGuard(home string) Guard {
	h := filepath.Clean(home)
	j := func(p ...string) string { return filepath.Join(append([]string{h}, p...)...) }
	return Guard{
		roots: []string{h},
		exact: []string{h, j(".config"), j(".cache"), j(".local"), j(".local", "share"), j(".local", "state")},
		deep:  []string{j(".ssh"), j(".gnupg"), j(".password-store")},
	}
}

// Check returns nil when path may be removed.
func (g Guard) Check(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing relative path %q", path)
	}
	p := filepath.Clean(path)
	for _, d := range g.deep {
		if inside(d, p) {
			return fmt.Errorf("refusing protected path %s", p)
		}
	}
	for _, e := range g.exact {
		if p == e {
			return fmt.Errorf("refusing protected path %s", p)
		}
	}
	for _, r := range g.roots {
		if inside(r, p) {
			return nil
		}
	}
	return fmt.Errorf("refusing path outside allowed roots: %s", p)
}

// inside reports whether p equals dir or lies below it.
func inside(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
