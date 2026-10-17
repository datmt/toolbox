package files

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// TrashDir is the freedesktop home trash.
func TrashDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "Trash")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "Trash")
}

// Trash moves path into trashDir following the freedesktop spec, so file
// managers can restore it. Same-filesystem only: a cross-device rename
// fails rather than silently copying gigabytes.
func Trash(trashDir, path string) error {
	files, info := filepath.Join(trashDir, "files"), filepath.Join(trashDir, "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	base := filepath.Base(abs)
	name := base
	var f *os.File
	for i := 1; ; i++ {
		f, err = os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) || i > 10000 {
			return err
		}
		name = base + "." + strconv.Itoa(i)
	}
	u := url.URL{Path: abs}
	_, werr := fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", u.EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))
	cerr := f.Close()
	infoPath := f.Name()
	if werr != nil || cerr != nil {
		os.Remove(infoPath)
		return fmt.Errorf("write trashinfo: %v %v", werr, cerr)
	}
	if err := os.Rename(abs, filepath.Join(files, name)); err != nil {
		os.Remove(infoPath)
		return err
	}
	return nil
}
