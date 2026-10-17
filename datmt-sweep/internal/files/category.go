package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datmt/datmt-sweep/internal/core"
)

// permanentBase is base plus the PermanentRemover methods. It is a separate
// type so categories that are already permanent (caches, trash) don't get them.
type permanentBase struct{ *base }

// base carries what every filesystem category shares.
type base struct {
	guard Guard
	name  string
	sc    core.Scanner
	verb  string // shown in DryRun / Summary
	rm    func(it core.Item) error
	// rmHard is the permanent delete used by permanentBase.
	rmHard func(it core.Item) error
}

func (b *base) Name() string          { return b.name }
func (b *base) Scanner() core.Scanner { return b.sc }

func (b *base) Info(_ context.Context, it core.Item) string {
	var s strings.Builder
	fmt.Fprintf(&s, "%s\n\nSize:     %s\nModified: %s\nRisk:     %s\n", it.Desc, core.HumanSize(it.Size), it.Date, it.Risk)
	return s.String()
}

func (b *base) DryRun(_ context.Context, items []core.Item) (string, error) {
	return b.dryRun(b.verb, items), nil
}

func (b *base) dryRun(verb string, items []core.Item) string {
	var s strings.Builder
	var total int64
	for _, it := range items {
		fmt.Fprintf(&s, "%s  %10s  %s\n", verb, core.HumanSize(it.Size), it.Desc)
		total += it.Size
	}
	fmt.Fprintf(&s, "\n%d items, %s\n", len(items), core.HumanSize(total))
	return s.String()
}

func (b *base) Remove(items []core.Item) core.Removal {
	return b.remove(b.verb, b.rm, items)
}

// DryRunPermanent and RemovePermanent make permanentBase a core.PermanentRemover.
func (b permanentBase) DryRunPermanent(_ context.Context, items []core.Item) (string, error) {
	return "PERMANENT, cannot be undone. Space is freed immediately.\n\n" + b.dryRun("delete", items), nil
}

func (b permanentBase) RemovePermanent(items []core.Item) core.Removal {
	return b.remove("permanently delete", b.rmHard, items)
}

func (b *base) remove(verb string, rm func(core.Item) error, items []core.Item) core.Removal {
	items = append([]core.Item(nil), items...)
	return core.Removal{
		Summary: fmt.Sprintf("%s %d item(s)", verb, len(items)),
		Do: func(ctx context.Context) core.Result {
			res := core.Result{Failed: map[string]error{}}
			for _, it := range items {
				if ctx.Err() != nil {
					res.Failed[it.ID] = ctx.Err()
					continue
				}
				if err := rm(it); err != nil {
					res.Failed[it.ID] = err
					continue
				}
				res.Done = append(res.Done, it.ID)
			}
			return res
		},
	}
}

// BigFiles lists regular files >= minSize under root; removal goes to trash,
// or deletes for good via core.PermanentRemover.
func BigFiles(root string, minSize int64, guard Guard, trashDir string) core.Category {
	return permanentBase{&base{
		name: "Big files", guard: guard, verb: "trash",
		sc: &bigScanner{root: root, minSize: minSize, guard: guard},
		rm: func(it core.Item) error {
			if err := guard.Check(it.ID); err != nil {
				return err
			}
			return Trash(trashDir, it.ID)
		},
		rmHard: func(it core.Item) error {
			if err := guard.Check(it.ID); err != nil {
				return err
			}
			// Scanner only lists regular files; re-check so a path that
			// turned into a directory or symlink since the scan survives.
			fi, err := os.Lstat(it.ID)
			if err != nil {
				return err
			}
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("not a regular file any more")
			}
			return os.Remove(it.ID)
		},
	}}
}

// Caches lists entries of cacheDir. Removal is permanent: regenerable data,
// and trashing it would not reclaim the space.
func Caches(cacheDir string, guard Guard) core.Category {
	return &base{
		name: "Caches", guard: guard, verb: "delete",
		sc: &cacheScanner{dir: cacheDir},
		rm: func(it core.Item) error {
			if err := guard.Check(it.ID); err != nil {
				return err
			}
			if filepath.Dir(it.ID) != filepath.Clean(cacheDir) {
				return fmt.Errorf("not a direct child of %s", cacheDir)
			}
			return os.RemoveAll(it.ID)
		},
	}
}

// TrashBin lists trashed entries; removal empties them for good.
func TrashBin(trashDir string) core.Category {
	return &base{
		name: "Trash", verb: "purge",
		sc: &trashScanner{dir: trashDir},
		rm: func(it core.Item) error {
			if it.ID == "" || it.ID != filepath.Base(it.ID) || it.ID == "." || it.ID == ".." {
				return fmt.Errorf("bad trash entry %q", it.ID)
			}
			if err := os.RemoveAll(filepath.Join(trashDir, "files", it.ID)); err != nil {
				return err
			}
			err := os.Remove(filepath.Join(trashDir, "info", it.ID+".trashinfo"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		},
	}
}
