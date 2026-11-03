package store

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// CreateExcl creates a brand-new file (O_EXCL, never following a symlink where the
// platform supports it) opened read/write. It fails if path already exists.
func CreateExcl(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|noFollow, perm)
}

// Replace moves src over dst. On unix rename is an atomic replace; Windows can't rename
// over an existing file, so dst is removed first there.
func Replace(src, dst string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(dst)
	}
	return os.Rename(src, dst)
}

// PrivateDir creates dir (and parents) and makes sure dir itself is 0700.
func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return nil
}

// NewTempDir makes a fresh private temp directory under <root>/.ytm and sweeps stale
// ones (older than a day) left by crashed runs. It never touches a live run's directory.
func NewTempDir(root string) (string, error) {
	base := filepath.Join(root, ".ytm")
	if err := PrivateDir(base); err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(base); err == nil {
		for _, e := range ents {
			if !e.IsDir() || len(e.Name()) < 4 || e.Name()[:4] != "tmp-" {
				continue
			}
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(base, e.Name()))
			}
		}
	}
	return os.MkdirTemp(base, "tmp-") // 0700
}
