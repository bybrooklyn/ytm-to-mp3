package names

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Library folders are keyed by how they are spelled once case, Unicode form and trailing
// punctuation are set aside (FoldKey), and album folders also by their edition-free title
// (EditionKey). Whichever spelling is already on disk wins: a new song by "meganeko" goes
// into an existing "Meganeko" folder, a song of "CHROMAKOPIA" into an existing
// "CHROMAKOPIA +". Every place that builds an Artist/Album path goes through Dirs.

// Dirs is root/Artist[/Album], reusing the folders that already exist under root in any
// spelling that differs only by case, Unicode form or trailing punctuation (and, for the
// album, by an edition marker such as "(Deluxe Edition)" or "+"). An empty album gives
// root/Artist. Nothing is created.
func Dirs(root, artist, album string) string {
	dir := filepath.Join(root, Folder(root, artist, "Unknown Artist", false))
	if album == "" {
		return dir
	}
	return filepath.Join(dir, Folder(dir, album, "Unknown Album", true))
}

// folderState remembers, per parent folder, what was listed (valid while the parent's
// modification time is the same) and the spellings this process has settled on but not
// created yet, so two songs of one artist handled at the same moment agree.
var folderState = struct {
	sync.Mutex
	listed  map[string]*listing
	claimed map[string]string // parent + "\x00" + kind + key -> spelling
}{listed: map[string]*listing{}, claimed: map[string]string{}}

type listing struct {
	mtime  time.Time
	byFold map[string][]string // FoldKey -> folder names
	byEd   map[string][]string // EditionKey -> folder names
}

// Folder is the name of the child folder of parent that holds raw: an existing one that
// differs only by case, Unicode form or trailing punctuation (edition=true: or by an
// edition marker) when there is one, else the new name.
func Folder(parent, raw, fallback string, edition bool) string {
	name := Part(raw, fallback)
	fk, ek := FoldKey(name), EditionKey(name)
	folderState.Lock()
	defer folderState.Unlock()
	l := list(parent)
	var cands []string
	if edition {
		cands = l.byEd[ek]
	} else {
		cands = l.byFold[fk]
	}
	kind := "f"
	key := fk
	if edition {
		kind, key = "e", ek
	}
	ck := parent + "\x00" + kind + key
	switch {
	case len(cands) == 0:
		if c, ok := folderState.claimed[ck]; ok {
			return c
		}
		folderState.claimed[ck] = name
		return name
	case len(cands) == 1:
		return cands[0]
	}
	for _, c := range cands {
		if c == name {
			return c // the exact spelling is there: nothing to decide
		}
	}
	stats := make([]DirStat, len(cands))
	for i, c := range cands {
		stats[i] = StatDir(filepath.Join(parent, c))
	}
	best := stats[0]
	for _, s := range stats[1:] {
		if (edition && Fuller(s, best)) || (!edition && Older(s, best)) {
			best = s
		}
	}
	return best.Name
}

// list reads parent's sub-folders into a cached index; folderState must be locked.
func list(parent string) *listing {
	st, err := os.Stat(parent)
	if err != nil {
		return &listing{}
	}
	if l := folderState.listed[parent]; l != nil && l.mtime.Equal(st.ModTime()) {
		return l
	}
	l := &listing{mtime: st.ModTime(), byFold: map[string][]string{}, byEd: map[string][]string{}}
	if ents, err := os.ReadDir(parent); err == nil {
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, ".") || !isDir(parent, e) {
				continue
			}
			fk, ek := FoldKey(n), EditionKey(n)
			l.byFold[fk] = append(l.byFold[fk], n)
			l.byEd[ek] = append(l.byEd[ek], n)
		}
	}
	folderState.listed[parent] = l
	return l
}

func isDir(parent string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink != 0 {
		st, err := os.Stat(filepath.Join(parent, e.Name()))
		return err == nil && st.IsDir()
	}
	return false
}

// DirStat describes one folder for choosing which spelling to keep.
type DirStat struct {
	Name   string
	Files  int       // regular files below it, not counting hidden ones
	Oldest time.Time // modification time of its earliest file (zero: none)
}

// StatDir walks dir (not following links, skipping dot entries) for its DirStat.
func StatDir(dir string) DirStat {
	s := DirStat{Name: filepath.Base(dir)}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		s.Files++
		if info, err := d.Info(); err == nil {
			if s.Oldest.IsZero() || info.ModTime().Before(s.Oldest) {
				s.Oldest = info.ModTime()
			}
		}
		return nil
	})
	return s
}

// Older reports whether a was downloaded before b: its earliest file is older; a folder
// with no files counts as newest; ties go to the one with more files, then the name.
func Older(a, b DirStat) bool {
	switch {
	case a.Oldest.IsZero() != b.Oldest.IsZero():
		return !a.Oldest.IsZero()
	case !a.Oldest.Equal(b.Oldest):
		return a.Oldest.Before(b.Oldest)
	case a.Files != b.Files:
		return a.Files > b.Files
	}
	return a.Name < b.Name
}

// Fuller reports whether a holds more files than b (the fuller edition); ties go to the
// older one.
func Fuller(a, b DirStat) bool {
	if a.Files != b.Files {
		return a.Files > b.Files
	}
	return Older(a, b)
}

// Preferred picks one of stats by Older (edition=false) or Fuller (edition=true).
func Preferred(stats []DirStat, edition bool) DirStat {
	s := append([]DirStat(nil), stats...)
	sort.SliceStable(s, func(i, j int) bool {
		if edition {
			return Fuller(s[i], s[j])
		}
		return Older(s[i], s[j])
	})
	return s[0]
}
