package tag

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// Folder merging is the migration for libraries that already hold the same artist or album
// under two spellings: "MASTER BOOT RECORD" and "Master Boot Record", "meganeko" and
// "Meganeko" (names.FoldKey), or a standard and a deluxe edition, "CHROMAKOPIA" and
// "CHROMAKOPIA +" (names.EditionKey). New downloads already avoid creating these (names.Dirs).
//
//   - Artist and album spellings that differ only by case or Unicode form: the folder that
//     was downloaded first (its earliest file is the oldest; then the one with more files)
//     keeps its name and takes the rest.
//   - Editions of one album: the fullest one (most files) takes the others, so the standard
//     edition's songs end up with the deluxe's.
//   - Nothing is deleted. A song that is already in the target (same title) is a duplicate:
//     the copy being merged goes to .ytm/replaced, as every replaced file does.
//
// Every move is journaled like a rename, so "Undo the last fix" puts it all back.

// MergeMove is one file's move.
type MergeMove struct {
	From, To string
	// Duplicate: the target already has this song (Kept); From goes to .ytm/replaced
	// instead of To.
	Duplicate bool
	Kept      string
}

// MergeGroup is one folder taking in others.
type MergeGroup struct {
	Kind    string   // "artist" or "album"
	Target  string   // the folder that stays
	Sources []string // folders emptied into it
	Moves   []MergeMove
}

type dirRef struct {
	artist string // artist folder (absolute)
	path   string // the album folder (absolute)
}

// PlanMerges finds the artist and album folders under root that should be one. It only
// reads the disk.
func PlanMerges(root string) ([]MergeGroup, error) {
	artists, err := subdirs(root)
	if err != nil {
		return nil, err
	}
	byFold := map[string][]string{}
	for _, a := range artists {
		k := names.FoldKey(filepath.Base(a))
		byFold[k] = append(byFold[k], a)
	}
	keys := make([]string, 0, len(byFold))
	for k := range byFold {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []MergeGroup
	for _, k := range keys {
		variants := byFold[k]
		canon := pickDir(variants, false)
		// Artist folders other than the canonical one empty into it.
		var artistMoves []MergeMove
		var artistSources []string
		// Album folders of all variants, grouped by edition.
		albums := map[string][]string{} // EditionKey -> album dirs
		for _, av := range variants {
			ds, _ := subdirs(av)
			for _, d := range ds {
				ek := names.EditionKey(filepath.Base(d))
				albums[ek] = append(albums[ek], d)
			}
			if av != canon {
				artistSources = append(artistSources, av)
				// loose files directly under the artist folder
				for _, f := range looseFiles(av) {
					artistMoves = append(artistMoves, MergeMove{From: f, To: filepath.Join(canon, filepath.Base(f))})
				}
			}
		}
		taken := newTakenSet()
		if len(variants) > 1 {
			g := MergeGroup{Kind: "artist", Target: canon, Sources: artistSources}
			g.Moves = taken.resolve(artistMoves)
			// albums are moved by their own groups below, so a plain artist merge has the
			// loose files only; folders follow.
			out = append(out, g)
		}
		ekeys := make([]string, 0, len(albums))
		for ek := range albums {
			ekeys = append(ekeys, ek)
		}
		sort.Strings(ekeys)
		for _, ek := range ekeys {
			dirs := albums[ek]
			// spellings: case-variants of one album are one spelling group
			sp := map[string][]string{}
			for _, d := range dirs {
				fk := names.FoldKey(filepath.Base(d))
				sp[fk] = append(sp[fk], d)
			}
			// The fullest edition across spellings, counting every folder of a spelling.
			canonAlbum := pickEdition(sp)
			target := filepath.Join(canon, filepath.Base(canonAlbum))
			var srcs []string
			var moves []MergeMove
			for _, d := range dirs {
				if d == target {
					continue
				}
				srcs = append(srcs, d)
				moves = append(moves, filesBelow(d, target)...)
			}
			if len(srcs) == 0 {
				continue
			}
			taken.seedDir(target)
			g := MergeGroup{Kind: "album", Target: target, Sources: srcs, Moves: taken.resolve(moves)}
			out = append(out, g)
		}
	}
	return out, nil
}

// pickEdition chooses the spelling to keep among album spellings (FoldKey -> folders): the
// spelling whose folders hold the most files in total (the fullest edition); equal ones go
// to the older spelling. The returned path is that spelling's own preferred folder.
func pickEdition(sp map[string][]string) string {
	var stats []names.DirStat
	for _, ds := range sp {
		agg := names.DirStat{}
		for _, d := range ds {
			s := names.StatDir(d)
			agg.Files += s.Files
			if agg.Oldest.IsZero() || (!s.Oldest.IsZero() && s.Oldest.Before(agg.Oldest)) {
				agg.Oldest = s.Oldest
			}
		}
		best := pickDir(ds, false)
		agg.Name = best
		stats = append(stats, agg)
	}
	return names.Preferred(stats, true).Name
}

// pickDir is the folder of dirs downloaded first (edition=false) or fullest (true).
func pickDir(dirs []string, edition bool) string {
	if len(dirs) == 1 {
		return dirs[0]
	}
	stats := make([]names.DirStat, len(dirs))
	for i, d := range dirs {
		stats[i] = names.StatDir(d)
		stats[i].Name = d
	}
	return names.Preferred(stats, edition).Name
}

func subdirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() { // links are left alone
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

func looseFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// filesBelow lists the regular files below src as moves into the same relative place under
// target.
func filesBelow(src, target string) []MergeMove {
	var out []MergeMove
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != src && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		out = append(out, MergeMove{From: p, To: filepath.Join(target, rel)})
		return nil
	})
	return out
}

var trackPrefixRe = regexp.MustCompile(`^(?:\d+[-.]\d+|\d+)\s*[-.]?\s+`)

// songKey is a file's identity inside a folder: its title, with the track number, the
// extension and case set aside, so "03 - Song.opus" and "3 Song.m4a" are one song.
func songKey(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = trackPrefixRe.ReplaceAllString(base, "")
	return names.FoldKey(base)
}

// takenSet tracks which songs a target folder holds, on disk and from moves already
// planned, to spot duplicates.
type takenSet struct {
	m map[string]string // dir + "\x00" + songKey -> path holding it
}

func newTakenSet() *takenSet { return &takenSet{m: map[string]string{}} }

func (t *takenSet) seedDir(dir string) {
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
		if d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") {
			k := filepath.Dir(p) + "\x00" + songKey(d.Name())
			if _, ok := t.m[k]; !ok {
				t.m[k] = p
			}
		}
		return nil
	})
}

// resolve marks the moves whose song the target already has as duplicates, in a stable
// order (oldest file first, so the copy downloaded first is the one that stays).
func (t *takenSet) resolve(moves []MergeMove) []MergeMove {
	type aged struct {
		m   MergeMove
		mod time.Time
	}
	as := make([]aged, len(moves))
	for i, m := range moves {
		as[i].m = m
		if st, err := os.Lstat(m.From); err == nil {
			as[i].mod = st.ModTime()
		}
	}
	sort.SliceStable(as, func(i, j int) bool {
		if !as[i].mod.Equal(as[j].mod) {
			return as[i].mod.Before(as[j].mod)
		}
		return as[i].m.From < as[j].m.From
	})
	out := make([]MergeMove, 0, len(moves))
	for _, a := range as {
		m := a.m
		dir := filepath.Dir(m.To)
		t.seedDir(dir)
		k := dir + "\x00" + songKey(filepath.Base(m.To))
		if kept, ok := t.m[k]; ok {
			m.Duplicate, m.Kept = true, kept
		} else {
			t.m[k] = m.To
		}
		out = append(out, m)
	}
	return out
}

// MergeFolders previews (or, with o.Apply, performs) the merges PlanMerges finds under
// o.Dir, journaling each move so Undo reverts them. It returns the number of groups.
func MergeFolders(ctx context.Context, o Options) (int, error) {
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return 0, err
	}
	o.Dir = filepath.Clean(abs)
	groups, err := PlanMerges(o.Dir)
	if err != nil {
		return 0, err
	}
	if len(groups) == 0 {
		o.UI.Note("No folders to merge")
		return 0, nil
	}
	batch := time.Now().Format("20060102-150405")
	failed := 0
	for _, g := range groups {
		if ctx.Err() != nil {
			return len(groups), ctx.Err()
		}
		o.UI.Styled(describeMerge(g, o.Dir))
		if !o.Apply {
			continue
		}
		if err := applyMerge(o, g, batch); err != nil {
			failed++
			o.UI.Bad("%v", err)
		}
	}
	o.UI.Head("%d folders to merge", len(groups))
	if failed > 0 {
		o.UI.Hint("%d could not be merged completely", failed)
	}
	return len(groups), nil
}

func describeMerge(g MergeGroup, root string) string {
	rel := func(p string) string { r, _ := filepath.Rel(root, p); return ui.Sanitize(r) }
	dups := 0
	for _, m := range g.Moves {
		if m.Duplicate {
			dups++
		}
	}
	var b strings.Builder
	b.WriteString(ui.Mark(ui.Accent("~"), "Merge into "+ui.Cyan(rel(g.Target))) + "\n")
	for _, s := range g.Sources {
		fmt.Fprintf(&b, "      %s %s\n", ui.Dim("from"), rel(s))
	}
	if len(g.Moves) == 0 {
		return strings.TrimRight(b.String(), "\n") // an artist folder whose albums follow
	}
	fmt.Fprintf(&b, "      %s", ui.Dim(fmt.Sprintf("%d files moved, %d already there (kept in .ytm/replaced)", len(g.Moves)-dups, dups)))
	return b.String()
}

func applyMerge(o Options, g MergeGroup, batch string) error {
	var firstErr error
	for _, m := range g.Moves {
		dst := m.To
		if m.Duplicate {
			rel, err := filepath.Rel(o.Dir, m.From)
			if err != nil || strings.HasPrefix(rel, "..") {
				rel = filepath.Base(m.From)
			}
			dst = uniquePath(filepath.Join(o.Dir, ".ytm", "replaced", rel))
		} else if _, err := os.Lstat(dst); err == nil {
			dst = "" // appeared since the plan: leave this one where it is
		}
		if dst == "" {
			continue
		}
		perm := os.FileMode(0o755)
		if m.Duplicate {
			perm = 0o700
		}
		if err := os.MkdirAll(filepath.Dir(dst), perm); err != nil {
			firstErr = err
			continue
		}
		if err := os.Rename(m.From, dst); err != nil {
			firstErr = err
			continue
		}
		if o.OnMove != nil {
			to := dst
			if m.Duplicate {
				to = m.Kept // the library now knows the song by the copy that stayed
			}
			o.OnMove(m.From, to)
		}
		if err := appendJournal(o.Dir, journalEntry{Batch: batch, Path: m.From, NewPath: dst, Old: map[string]string{}}); err != nil {
			firstErr = err
		}
	}
	for _, s := range g.Sources {
		pruneEmpty(s, o.Dir) // folders left empty (no files, only folders) go too
	}
	return firstErr
}

// pruneEmpty removes dir and the empty folders below it, and empty parents up to root.
func pruneEmpty(dir, root string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // only succeeds when empty
	}
	removeEmptyDirs(filepath.Dir(dir), root)
}

// uniquePath is p, or p with " (2)", " (3)"... before the extension when p exists.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(c); err != nil {
			return c
		}
	}
}
