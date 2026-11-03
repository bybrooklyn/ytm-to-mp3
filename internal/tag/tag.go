// Package tag improves the metadata of already-downloaded files. It only ever
// adds or corrects information: tags YouTube provided are kept unless
// MusicBrainz identifies the exact recording with very high confidence.
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
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

type Options struct {
	Dir     string
	Apply   bool // write changes (default: preview only)
	Rename  bool // also move files to Artist/Album/Title [id].ext
	Undo    bool // revert the last applied batch
	Offline bool // local cleanup only, no MusicBrainz
	NoClean bool // skip local cleanup
	FFmpeg  string
	FFprobe string
	UI      *ui.UI
	MB      *Client // nil -> default client
}

var audioExt = map[string]bool{".mp3": true, ".m4a": true, ".opus": true, ".flac": true, ".ogg": true, ".aac": true}
var idRe = regexp.MustCompile(`\[([\w-]{11})\]$`)

type proposal struct {
	info     Info
	set      map[string]string // tag -> new value
	coverURL string
	newPath  string
	note     string // why MusicBrainz was not applied
	matched  bool
}

// Run previews (or applies) metadata improvements for every audio file under o.Dir.
func Run(ctx context.Context, o Options) error {
	// Everything below (walk, journal, containment checks) relies on an absolute root.
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return err
	}
	o.Dir = filepath.Clean(abs)
	if o.Undo {
		return undo(ctx, o)
	}
	if o.MB == nil && !o.Offline {
		o.MB = NewClient(filepath.Join(o.Dir, ".ytm", "mb"))
	}
	var files []string
	_ = filepath.WalkDir(o.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != o.Dir {
			return fs.SkipDir
		}
		if !d.IsDir() && audioExt[strings.ToLower(filepath.Ext(p))] {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no audio files found in %s", o.Dir)
	}
	o.UI.Printf("Checking %d files%s...", len(files), map[bool]string{true: " (offline)", false: ""}[o.Offline])

	batch := time.Now().Format("20060102-150405")
	var changed, matched, unmatched, failed int
	for i, p := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(o.Dir, p)
		o.UI.Status("[%d/%d] %s", i+1, len(files), rel)
		pr, err := propose(ctx, o, p)
		if err != nil {
			failed++
			o.UI.Printf("✗ %s  (%v)", rel, err)
			continue
		}
		if pr.matched {
			matched++
		} else if !o.Offline {
			unmatched++
		}
		if len(pr.set) == 0 && pr.coverURL == "" && pr.newPath == "" {
			continue
		}
		changed++
		o.UI.Printf("%s", describe(rel, pr, o.Dir))
		if o.Apply {
			if err := apply(ctx, o, pr, batch); err != nil {
				failed++
				o.UI.Printf("  ✗ %v", err)
			}
		}
	}
	o.UI.ClearStatus()
	o.UI.Printf("\n%d files checked: %d to change, %d confidently identified on MusicBrainz, %d left as YouTube tagged them, %d errors",
		len(files), changed, matched, unmatched, failed)
	if changed > 0 && !o.Apply {
		o.UI.Printf("This was a preview. Re-run with --apply to write these changes (undo with --undo).")
	}
	return nil
}

func propose(ctx context.Context, o Options, path string) (*proposal, error) {
	info, err := Probe(ctx, o.FFprobe, path)
	if err != nil {
		return nil, err
	}
	pr := &proposal{info: info, set: map[string]string{}}
	title, artist, album := info.get("title"), info.get("artist"), info.get("album")

	if !o.NoClean {
		if a := CleanArtist(artist); a != artist {
			pr.set["artist"], artist = a, a
		}
		if t := CleanTitle(title, firstArtist(artist)); t != title {
			pr.set["title"], title = t, t
		}
	}

	if !o.Offline {
		recs, err := o.MB.SearchRecordings(ctx, title, firstArtist(artist))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			pr.note = err.Error()
		} else if m, why := Decide(title, artist, album, info.Duration, recs); m == nil {
			pr.note = why
		} else {
			pr.matched = true
			enrich(pr, info, m, artist)
			if m.Release != nil && m.Release.ID != "" {
				pr.coverURL = CoverURL(m.Release.ID)
			}
		}
	}

	// Drop no-op changes so previews only show real differences.
	for k, v := range pr.set {
		if v == info.Tags[k] {
			delete(pr.set, k)
		}
	}
	if o.Rename {
		pr.newPath = renameTarget(o.Dir, path, info, pr.set)
	}
	return pr, nil
}

func firstArtist(a string) string {
	if p := splitArtists(a); len(p) > 0 {
		return p[0]
	}
	return a
}

// enrich adds MusicBrainz data. Existing values are only replaced by the
// canonical spelling of the same thing; missing fields are filled.
func enrich(pr *proposal, info Info, m *Match, artist string) {
	r := m.Recording
	pr.set["title"] = r.Title
	if len(r.ArtistCredit) > 0 {
		pr.set["album_artist"] = r.ArtistCredit[0].Artist.Name
		pr.set["musicbrainz_artistid"] = r.ArtistCredit[0].Artist.ID
		if len(splitArtists(artist)) == 1 && norm(artist) == norm(r.ArtistCredit[0].Artist.Name) {
			pr.set["artist"] = r.ArtistCredit[0].Artist.Name
		}
	}
	pr.set["musicbrainz_recordingid"] = r.ID
	if len(r.ISRCs) > 0 {
		pr.set["isrc"] = r.ISRCs[0]
	}
	if rel := m.Release; rel != nil {
		pr.set["album"] = rel.Title
		pr.set["musicbrainz_albumid"] = rel.ID
		if d := rel.Date; d != "" && (info.get("date", "year") == "" || year(info.get("date", "year")) > year(d)) {
			pr.set["date"] = d
		}
		if m.Track != "" && info.get("track") == "" {
			pr.set["track"] = m.Track
		}
		if m.Disc > 0 && info.get("disc") == "" {
			pr.set["disc"] = fmt.Sprint(m.Disc)
		}
	}
}

func year(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}

func renameTarget(root, path string, info Info, set map[string]string) string {
	get := func(k string) string {
		if v, ok := set[k]; ok {
			return v
		}
		return info.get(k)
	}
	artist := firstNonEmpty(get("album_artist"), firstArtist(get("artist")))
	title := get("title")
	if artist == "" || title == "" {
		return ""
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	id := ""
	if m := idRe.FindStringSubmatch(stem); m != nil {
		id = " [" + m[1] + "]"
	}
	parts := []string{root, names.Part(artist, "Unknown Artist")}
	if al := get("album"); al != "" {
		parts = append(parts, names.Part(al, "Unknown Album"))
	}
	parts = append(parts, names.Part(title, "Unknown")+id+ext)
	np := filepath.Join(parts...)
	if np == path {
		return ""
	}
	return np
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func describe(rel string, pr *proposal, root string) string {
	var b strings.Builder
	b.WriteString("~ " + rel + "\n")
	keys := make([]string, 0, len(pr.set))
	for k := range pr.set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		old := pr.info.Tags[k]
		if old == "" {
			old = "(none)"
		}
		fmt.Fprintf(&b, "    %-24s %s -> %s\n", k, old, pr.set[k])
	}
	if pr.coverURL != "" {
		b.WriteString("    cover                    replace with album art\n")
	}
	if pr.newPath != "" {
		nrel, _ := filepath.Rel(root, pr.newPath)
		fmt.Fprintf(&b, "    move                     -> %s\n", nrel)
	}
	return strings.TrimRight(b.String(), "\n")
}

func apply(ctx context.Context, o Options, pr *proposal, batch string) error {
	path := pr.info.Path
	entry := journalEntry{Batch: batch, Path: path, Old: map[string]string{}}
	for k := range pr.set {
		entry.Old[k] = pr.info.Tags[k]
	}
	var cover string
	if pr.coverURL != "" {
		dir := filepath.Join(o.Dir, ".ytm", "tag-backup")
		_ = store.PrivateDir(dir)
		newCover := filepath.Join(dir, batch+"-new.jpg")
		if err := getURL(ctx, pr.coverURL, newCover); err == nil {
			cover = newCover
			if pr.info.HasCover {
				bk := filepath.Join(dir, coverBackupName(batch, path))
				if ExtractCover(ctx, o.FFmpeg, path, bk) == nil {
					entry.CoverBackup = bk
				}
			}
		} // no art on the Cover Art Archive: keep what's there
		defer os.Remove(newCover)
	}
	if len(pr.set) > 0 || cover != "" {
		if err := Write(ctx, o.FFmpeg, path, pr.set, cover); err != nil {
			return err
		}
	}
	if pr.newPath != "" {
		if _, err := os.Stat(pr.newPath); err == nil {
			return fmt.Errorf("not moving: %s already exists", pr.newPath)
		}
		if err := os.MkdirAll(filepath.Dir(pr.newPath), 0o755); err != nil {
			return err
		}
		if err := os.Rename(path, pr.newPath); err != nil {
			return err
		}
		entry.NewPath = pr.newPath
		removeEmptyDirs(filepath.Dir(path), o.Dir)
	}
	return appendJournal(o.Dir, entry)
}

func removeEmptyDirs(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if os.Remove(dir) != nil { // only succeeds when empty
			return
		}
		dir = filepath.Dir(dir)
	}
}
