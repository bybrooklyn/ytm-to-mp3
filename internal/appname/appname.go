// Package appname holds what the app is called on disk and in the environment, and the
// one-time move of data from the names the app used before it was renamed (ytm).
//
// Everything that builds a ".mfetchy" path, reads an MFY_ variable or locates a config or
// cache directory goes through here, so the names live in exactly one place.
package appname

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// Name is the project's name in prose; Cmd is the command-line binary.
	Name = "mfetchy"
	Cmd  = "mfy"

	// StateDirName is the hidden folder inside a music folder (state.db, replaced/, ...).
	StateDirName = ".mfetchy"
	// OldStateDirName, OldDirName and OldEnvPrefix are the names before the rename.
	OldStateDirName = ".ytm"
	OldDirName      = "ytm"
	OldEnvPrefix    = "YTM_"

	// UserAgent identifies the app honestly to services that ask who is calling.
	UserAgent = "mfetchy (https://github.com/bybrooklyn/mfetchy)"
)

// Getenv reads MFY_<name>, falling back to the old YTM_<name>. name has no prefix
// ("DEBUG" reads MFY_DEBUG, then YTM_DEBUG).
func Getenv(name string) string {
	v, _ := LookupEnv(name)
	return v
}

// LookupEnv is Getenv that also says whether either variable was set. A set MFY_ variable
// wins even when empty.
func LookupEnv(name string) (string, bool) {
	if v, ok := os.LookupEnv("MFY_" + name); ok {
		return v, true
	}
	return os.LookupEnv(OldEnvPrefix + name)
}

var noted sync.Map // paths already reported

// note prints one line about a migration that could not be done, once per path.
func note(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if _, dup := noted.LoadOrStore(msg, true); dup {
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// Migrate makes newPath the place to use, moving oldPath there when it can.
//
//   - newPath exists: it is used; oldPath is left untouched.
//   - newPath absent, oldPath present: oldPath is renamed to newPath (one atomic rename,
//     so a database's -wal and -shm files move with it). If the rename fails, oldPath is
//     returned so this run keeps working on the old data, with one note.
//   - neither exists: newPath.
//
// moved reports that the rename happened on this call. Nothing is ever copied or deleted.
func Migrate(newPath, oldPath string) (use string, moved bool) {
	if _, err := os.Lstat(newPath); err == nil {
		return newPath, false
	}
	if _, err := os.Lstat(oldPath); err != nil {
		return newPath, false
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		note("mfetchy: could not move %s to %s (%v); using the old folder for now", oldPath, newPath, err)
		return oldPath, false
	}
	return newPath, true
}

// StateDir is the state folder of a music folder: <root>/.mfetchy, migrated from
// <root>/.ytm on first use. When the move fails it returns the old path.
func StateDir(root string) string {
	use, moved := Migrate(filepath.Join(root, StateDirName), filepath.Join(root, OldStateDirName))
	if moved {
		recordMoved(root)
	}
	return use
}

// movedRoots remembers music folders whose state folder was renamed by this process but
// whose stored paths have not been rewritten yet (store.Open does that).
var movedRoots sync.Map

func recordMoved(root string) { movedRoots.Store(root, true) }

// TakeMoved reports (once) that root's state folder was renamed by this process.
func TakeMoved(root string) bool {
	_, ok := movedRoots.LoadAndDelete(root)
	return ok
}

// ConfigDir is <user config dir>/mfetchy, migrated from <user config dir>/ytm.
func ConfigDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	use, _ := Migrate(filepath.Join(d, Name), filepath.Join(d, OldDirName))
	return use, nil
}

// CacheDir is <user cache dir>/mfetchy, migrated from <user cache dir>/ytm. It is not
// created.
func CacheDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	use, _ := Migrate(filepath.Join(d, Name), filepath.Join(d, OldDirName))
	return use, nil
}
