package files

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/datmt/datmt-sweep/internal/core"
)

// DirSize sums regular file sizes under path without following symlinks.
func DirSize(ctx context.Context, path string) int64 {
	var n int64
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func send(ctx context.Context, out chan<- core.Item, it core.Item) error {
	select {
	case out <- it:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// skipDirs are never descended into by the big-file scan: repos, and the
// directories other categories own.
var skipDirs = map[string]bool{".git": true, ".cache": true, "Trash": true}

type bigScanner struct {
	root    string
	minSize int64
	guard   Guard
}

func (s *bigScanner) Name() string { return "Big files" }

func (s *bigScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	var sendErr error
	filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			sendErr = ctx.Err()
			return sendErr
		}
		if err != nil {
			return nil // unreadable dir: skip
		}
		if d.IsDir() {
			if p != s.root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Size() < s.minSize {
			return nil
		}
		if s.guard.Check(p) != nil {
			return nil
		}
		rel, _ := filepath.Rel(s.root, p)
		it := core.Item{
			ID: p, Category: "Big files", Title: rel, Desc: p, Size: fi.Size(),
			Risk: core.Review, Date: fi.ModTime().Format("2006-01-02"),
		}
		if sendErr = send(ctx, out, it); sendErr != nil {
			return sendErr
		}
		return nil
	})
	return sendErr
}

type cacheScanner struct{ dir string }

func (s *cacheScanner) Name() string { return "Caches" }

func (s *cacheScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		p := filepath.Join(s.dir, e.Name())
		var size int64
		date := ""
		if fi, err := e.Info(); err == nil {
			date = fi.ModTime().Format("2006-01-02")
			size = fi.Size()
		}
		if e.IsDir() {
			size = DirSize(ctx, p)
		}
		it := core.Item{ID: p, Category: "Caches", Title: e.Name(), Desc: p, Size: size, Risk: core.Safe, Date: date}
		if err := send(ctx, out, it); err != nil {
			return err
		}
	}
	return nil
}

type trashScanner struct{ dir string }

func (s *trashScanner) Name() string { return "Trash" }

func (s *trashScanner) Scan(ctx context.Context, out chan<- core.Item) error {
	ents, err := os.ReadDir(filepath.Join(s.dir, "files"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		p := filepath.Join(s.dir, "files", e.Name())
		size := int64(0)
		if e.IsDir() {
			size = DirSize(ctx, p)
		} else if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		date := ""
		if b, err := os.ReadFile(filepath.Join(s.dir, "info", e.Name()+".trashinfo")); err == nil {
			date = trashDate(string(b))
		}
		it := core.Item{ID: e.Name(), Category: "Trash", Title: e.Name(), Desc: p, Size: size, Risk: core.Safe, Date: date}
		if err := send(ctx, out, it); err != nil {
			return err
		}
	}
	return nil
}

func trashDate(info string) string {
	const key = "DeletionDate="
	for _, l := range splitLines(info) {
		if len(l) > len(key) && l[:len(key)] == key {
			if t, err := time.Parse("2006-01-02T15:04:05", l[len(key):]); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	return ""
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
