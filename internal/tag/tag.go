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
	"sort"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

type Options struct {
	Dir    string
	Apply  bool // write changes (default: preview only)
	Rename bool // also move files to Artist/Album/NN - Title.ext
	// OnMove, if set, is told about every file moved (by Rename or by Undo), so the state
	// database can follow it.
	OnMove func(from, to string)
	// TrackNumber, if set, gives a file's place on its album when its tags have no track
	// number (from the album lookups), so a rename can still name it "NN - Title". id is
	// the YouTube video id from the comment tag ("" if none). 0 = unknown.
	TrackNumber func(artist, album, title, id string) (no, total int)
	// Files, if set, limits the run to these files (under Dir) instead of every one there.
	Files   []string
	Undo    bool // revert the last applied batch
	Offline bool // local cleanup only, no MusicBrainz
	NoClean bool // skip local cleanup
	FFmpeg  string
	FFprobe string
	UI      *ui.UI
	MB      *Client // nil -> default client
}

var audioExt = map[string]bool{".mp3": true, ".m4a": true, ".opus": true, ".flac": true, ".ogg": true, ".aac": true}

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
		o.MB = NewClient(filepath.Join(appname.StateDir(o.Dir), "mb"))
	}
	var files []string
	for _, p := range o.Files {
		// only real audio files inside the folder: the journal and renames assume that
		if abs, err := filepath.Abs(p); err == nil && strings.HasPrefix(abs, o.Dir+string(filepath.Separator)) && audioExt[strings.ToLower(filepath.Ext(abs))] {
			if st, err := os.Lstat(abs); err == nil && st.Mode().IsRegular() {
				files = append(files, abs)
			}
		}
	}
	walk := len(o.Files) == 0
	_ = filepath.WalkDir(o.Dir, func(p string, d fs.DirEntry, err error) error {
		if !walk {
			return filepath.SkipAll
		}
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
	o.UI.Note("Checking %d files%s...", len(files), map[bool]string{true: " · offline", false: ""}[o.Offline])

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
			o.UI.Bad("%s · %v", rel, err)
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
		o.UI.Styled(describe(rel, pr, o.Dir))
		if o.Apply {
			if err := apply(ctx, o, pr, batch); err != nil {
				failed++
				o.UI.Bad("%v", err)
			}
		}
	}
	o.UI.ClearStatus()
	o.UI.Head("%d files checked · %d to change", len(files), changed)
	o.UI.Hint("%d identified on MusicBrainz for sure, %d left as they were, %d errors", matched, unmatched, failed)
	if changed > 0 && !o.Apply {
		o.UI.Hint("This was a preview. Re-run with --apply to write these changes (undo with --undo).")
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
			// Album art only replaces a missing, small or non-square picture: a good cover
			// (Apple's, or one ytm already put there) is never swapped on every run.
			if m.Release != nil && m.Release.ID != "" && !info.GoodCover() {
				pr.coverURL = CoverURL(m.Release.ID)
			}
		}
	}

	// The album lookups know the place of a song whose tags have none: write it to the tag,
	// not only to the file name.
	if no, total := trackFallback(o, info); no > 0 && pr.set["track"] == "" {
		pr.set["track"] = names.TrackTag(no, total)
	}

	// Drop no-op changes, and what this file's format can't hold (it would be proposed
	// again on every run), so previews only show real differences.
	ext := filepath.Ext(path)
	for k, v := range pr.set {
		if v == info.Tags[k] || !names.TagSupported(ext, k) || sameTrack(k, v, info) {
			delete(pr.set, k)
		}
	}
	if o.Rename {
		no, total := trackFallback(o, info)
		pr.newPath = renameTarget(o.Dir, path, info, pr.set, no, total)
	}
	return pr, nil
}

// sameTrack reports whether a "track"/"disc" value "N/T" is what the file already says,
// where a Vorbis file keeps N and T in two fields.
func sameTrack(k, v string, info Info) bool {
	if k != "track" && k != "disc" {
		return false
	}
	cur := info.Tags[k]
	if tt := info.Tags[k+"total"]; tt != "" && !strings.Contains(cur, "/") {
		cur += "/" + tt
	}
	return cur == v
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
		pr.set["musicbrainz_artistid"] = r.ArtistCredit[0].Artist.ID
		if len(splitArtists(artist)) == 1 && norm(artist) == norm(r.ArtistCredit[0].Artist.Name) {
			pr.set["artist"] = r.ArtistCredit[0].Artist.Name
		}
	}
	// The standard name players and Picard read; ytm used to write musicbrainz_recordingid.
	pr.set["musicbrainz_trackid"] = r.ID
	if info.Tags["musicbrainz_recordingid"] != "" {
		pr.set["musicbrainz_recordingid"] = ""
	}
	if len(r.ISRCs) > 0 {
		pr.set["isrc"] = r.ISRCs[0]
	}
	rel := m.Release
	// The album artist is the release's credit when it has its own (a compilation), else
	// the recording's main artist. One already in the file is kept unless the release
	// says otherwise: it is usually right, and a featured artist must not replace it.
	switch {
	case rel != nil && len(rel.ArtistCredit) > 0:
		pr.set["album_artist"] = rel.ArtistCredit[0].Artist.Name
		pr.set["musicbrainz_albumartistid"] = rel.ArtistCredit[0].Artist.ID
	case len(r.ArtistCredit) > 0 && info.get("album_artist") == "":
		pr.set["album_artist"] = r.ArtistCredit[0].Artist.Name
		pr.set["musicbrainz_albumartistid"] = r.ArtistCredit[0].Artist.ID
	case len(r.ArtistCredit) > 0 && norm(info.get("album_artist")) == norm(r.ArtistCredit[0].Artist.Name):
		pr.set["album_artist"] = r.ArtistCredit[0].Artist.Name // canonical spelling
		pr.set["musicbrainz_albumartistid"] = r.ArtistCredit[0].Artist.ID
	}
	if rel != nil {
		pr.set["album"] = rel.Title
		pr.set["musicbrainz_albumid"] = rel.ID
		if d := rel.Date; d != "" && (info.get("date", "year") == "" || year(info.get("date", "year")) > year(d)) {
			pr.set["date"] = d
		}
		if m.Track != "" && info.get("track") == "" {
			no, _ := names.ParseTrack(m.Track)
			pr.set["track"] = names.TrackTag(no, mediumTracks(rel, m.Disc))
			if no <= 0 {
				pr.set["track"] = m.Track
			}
		}
		if m.Disc > 0 && info.get("disc") == "" {
			pr.set["disc"] = fmt.Sprint(m.Disc)
		}
	}
}

// mediumTracks is how many tracks the disc a song is on has (0 when unknown).
func mediumTracks(rel *mbRelease, disc int) int {
	for _, md := range rel.Media {
		if md.Position == disc {
			return md.TrackCount
		}
	}
	return 0
}

func year(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}

// trackFallback is the track number for a file without one, from o.TrackNumber, looked up
// by the tags as ytm wrote them (before any MusicBrainz corrections).
func trackFallback(o Options, info Info) (no, total int) {
	if o.TrackNumber == nil || info.get("track") != "" || info.get("album") == "" {
		return 0, 0
	}
	id := ""
	if _, v, ok := strings.Cut(info.get("comment"), "watch?v="); ok {
		id = v
	}
	for _, a := range []string{info.get("album_artist"), firstArtist(info.get("artist"))} {
		if a == "" {
			continue
		}
		if no, total := o.TrackNumber(a, info.get("album"), info.get("title"), id); no > 0 {
			return no, total
		}
	}
	return 0, 0
}

func renameTarget(root, path string, info Info, set map[string]string, fallbackNo, fallbackTotal int) string {
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
	// Same scheme as new downloads: Artist/Album/NN - Title.ext, no video id in the name
	// (ytm keeps it in the comment tag).
	ext := filepath.Ext(path)
	parts := []string{names.Dirs(root, artist, get("album"))}
	prefix := ""
	if al := get("album"); al != "" {
		no, total := names.ParseTrack(get("track"))
		if no <= 0 {
			no, total = fallbackNo, fallbackTotal
		}
		prefix = names.TrackPrefix(no, total)
	}
	parts = append(parts, names.PartBetween(prefix, title, "Unknown", ext))
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

// describe is a file's proposed changes, for ui.Styled:
//
//	~ Artist/Album/Song.opus
//	    title                    old → new
//
// Tag values come from the file and MusicBrainz, so they are sanitized here.
func describe(rel string, pr *proposal, root string) string {
	var b strings.Builder
	b.WriteString(ui.Mark(ui.Accent("~"), ui.Sanitize(rel)) + "\n")
	keys := make([]string, 0, len(pr.set))
	for k := range pr.set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	row := func(k, old, now string) {
		fmt.Fprintf(&b, "      %s %s %s %s\n", ui.Dim(fmt.Sprintf("%-26s", k)), ui.Dim(old), ui.Dim("→"), now)
	}
	for _, k := range keys {
		old := ui.Sanitize(pr.info.Tags[k])
		if old == "" {
			old = "(none)"
		}
		now := ui.Green(ui.Sanitize(pr.set[k]))
		if pr.set[k] == "" {
			now = ui.Dim("(cleared)")
		}
		row(k, old, now)
	}
	if pr.coverURL != "" {
		row("cover", "", ui.Green("album art"))
	}
	if pr.newPath != "" {
		nrel, _ := filepath.Rel(root, pr.newPath)
		row("move", "", ui.Cyan(ui.Sanitize(nrel)))
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
		dir := filepath.Join(appname.StateDir(o.Dir), "tag-backup")
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
		if err := Write(ctx, o.FFmpeg, o.FFprobe, path, pr.set, cover); err != nil {
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
		if o.OnMove != nil {
			o.OnMove(path, pr.newPath)
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
