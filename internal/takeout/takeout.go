// Package takeout reads a YouTube Music library out of a Google Takeout export
// (zip, extracted folder, single CSV) or a plain list of IDs / URLs.
package takeout

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

var idRe = regexp.MustCompile(`(?:v=|youtu\.be/|/shorts/|/embed/|^)([\w-]{11})(?:[^\w-]|$)`)

// ExtractID pulls a video ID out of a URL or bare ID.
func ExtractID(s string) (string, bool) {
	m := idRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Load reads tracks from path. Unless all is set, ID-only playlist CSVs are
// ignored whenever the export has real song CSVs, because Takeout playlists
// also contain ordinary (non-music) videos.
func Load(path string, all bool) ([]model.Track, error) {
	return LoadAll([]string{path}, all)
}

// LoadAll merges several inputs (e.g. the numbered parts of one multi-zip export).
func LoadAll(paths []string, all bool) ([]model.Track, error) {
	var docs []doc
	for _, p := range paths {
		d, err := loadDocs(p)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d...)
	}
	return merge(docs, all)
}

// maxWalk bounds how many files a folder import will look at, so pointing mfetchy at a huge
// directory (like your home folder) can't run away.
const maxWalk = 50_000

func loadDocs(path string) ([]doc, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var docs []doc
	switch {
	case st.IsDir():
		seen := 0
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable corners are skipped, not fatal
			}
			if d.IsDir() {
				if p != path && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if seen++; seen > maxWalk {
				return fs.SkipAll
			}
			switch {
			case isCSV(p):
				f, err := os.Open(p)
				if err != nil {
					return nil
				}
				defer f.Close()
				docs = append(docs, parseCSV(p, f))
			case isZip(p) && zipLooksLikeYouTube(p):
				// Takeout often splits one export into several zips; take them all.
				zd, err := readZip(p)
				if err != nil {
					return nil
				}
				docs = append(docs, zd...)
			}
			return nil
		})
	case isZip(path):
		docs, err = readZip(path)
	case isCSV(path):
		var f *os.File
		if f, err = os.Open(path); err == nil {
			defer f.Close()
			docs = append(docs, parseCSV(path, f))
		}
	default:
		var f *os.File
		if f, err = os.Open(path); err == nil {
			defer f.Close()
			docs = append(docs, parseList(f))
		}
	}
	if err != nil {
		return nil, err
	}
	return docs, nil
}

// Limits guarding against hostile or corrupt exports.
const (
	maxReadBytes   = 64 << 20 // per CSV / text input
	maxRows        = 500_000  // total rows across the whole load
	maxZipEntries  = 10_000
	maxLineBytes   = 1 << 20
	maxEntryUncomp = maxReadBytes
)

type doc struct {
	name    string
	tracks  []model.Track
	rich    bool  // has titles/artists, i.e. a songs CSV rather than an ID-only list
	invalid int   // rows dropped for a malformed video id
	err     error // read problem (e.g. an over-long line)
}

func isCSV(p string) bool { return strings.EqualFold(filepath.Ext(p), ".csv") }

func readZip(path string) ([]doc, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("zip has %d entries (limit %d); not a normal Takeout export", len(zr.File), maxZipEntries)
	}
	var docs []doc
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isCSV(f.Name) || f.UncompressedSize64 > maxEntryUncomp {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		docs = append(docs, parseCSV(f.Name, rc))
		rc.Close()
	}
	return docs, nil
}

func merge(docs []doc, all bool) ([]model.Track, error) {
	hasRich := false
	invalid, rows := 0, 0
	var firstErr error
	for _, d := range docs {
		invalid += d.invalid
		rows += len(d.tracks)
		if d.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", d.name, d.err)
		}
	}
	if rows > maxRows {
		return nil, fmt.Errorf("export has more than %d rows; refusing to load it", maxRows)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	for _, d := range docs {
		hasRich = hasRich || (d.rich && len(d.tracks) > 0)
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].rich && !docs[j].rich })
	seen := map[string]int{}
	var out []model.Track
	for _, d := range docs {
		if hasRich && !d.rich && !all && len(docs) > 1 {
			continue
		}
		for _, t := range d.tracks {
			if i, ok := seen[t.ID]; ok {
				if out[i].Title == "" && t.Title != "" {
					out[i] = t
				}
				continue
			}
			seen[t.ID] = len(out)
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		msg := "no tracks found (expected a CSV with a 'Video ID' column, or a list of video IDs/URLs)"
		if invalid > 0 {
			msg += fmt.Sprintf("; %d entries had malformed video ids and were dropped", invalid)
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// parseCSV understands Takeout CSVs. Files without a "Video ID" column yield no tracks.
func parseCSV(name string, r io.Reader) doc {
	d := doc{name: name}
	br := bufio.NewReader(io.LimitReader(r, maxReadBytes))
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return d
	}
	col := func(names ...string) int {
		for i, h := range head {
			h = strings.ToLower(strings.TrimSpace(h))
			for _, n := range names {
				if h == n {
					return i
				}
			}
		}
		return -1
	}
	idC, titleC, albumC := col("video id", "videoid"), col("song title", "title"), col("album title", "album")
	if idC < 0 {
		return d
	}
	var artistCs []int
	for i, h := range head {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(h)), "artist") {
			artistCs = append(artistCs, i)
		}
	}
	get := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	d.rich = col("song title") >= 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if len(d.tracks)+d.invalid >= maxRows {
			d.err = fmt.Errorf("more than %d rows", maxRows)
			break
		}
		id := get(rec, idC)
		if id == "" {
			continue
		}
		if !model.ValidID(id) {
			d.invalid++
			continue
		}
		t := model.Track{ID: id, Title: get(rec, titleC), Album: get(rec, albumC)}
		for _, c := range artistCs {
			if a := get(rec, c); a != "" {
				t.Artists = append(t.Artists, a)
			}
		}
		d.tracks = append(d.tracks, t)
	}
	return d
}

func parseList(r io.Reader) doc {
	d := doc{name: "list"}
	sc := bufio.NewScanner(io.LimitReader(r, maxReadBytes))
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for sc.Scan() {
		if len(d.tracks)+d.invalid >= maxRows {
			d.err = fmt.Errorf("more than %d rows", maxRows)
			return d
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, ok := ExtractID(line)
		if !ok || !model.ValidID(id) {
			d.invalid++
			continue
		}
		d.tracks = append(d.tracks, model.Track{ID: id})
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			err = fmt.Errorf("a line is longer than %d bytes; this is not a list of video ids", maxLineBytes)
		}
		d.err = err
	}
	return d
}

// Describe summarises a load for the user.
func Describe(tracks []model.Track) string {
	rich := 0
	for _, t := range tracks {
		if t.Title != "" {
			rich++
		}
	}
	word := "tracks"
	if len(tracks) == 1 {
		word = "track"
	}
	return fmt.Sprintf("%d %s (%d with metadata)", len(tracks), word, rich)
}
