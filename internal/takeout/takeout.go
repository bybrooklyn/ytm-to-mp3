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

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
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
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var docs []doc
	switch {
	case st.IsDir():
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !isCSV(p) {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			docs = append(docs, parseCSV(p, f))
			return nil
		})
	case strings.EqualFold(filepath.Ext(path), ".zip"):
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
	return merge(docs, all)
}

type doc struct {
	name   string
	tracks []model.Track
	rich   bool // has titles/artists, i.e. a songs CSV rather than an ID-only list
}

func isCSV(p string) bool { return strings.EqualFold(filepath.Ext(p), ".csv") }

func readZip(path string) ([]doc, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var docs []doc
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isCSV(f.Name) {
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
		return nil, errors.New("no tracks found (expected a CSV with a 'Video ID' column, or a list of video IDs/URLs)")
	}
	return out, nil
}

// parseCSV understands Takeout CSVs. Files without a "Video ID" column yield no tracks.
func parseCSV(name string, r io.Reader) doc {
	d := doc{name: name}
	br := bufio.NewReader(r)
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
		id := get(rec, idC)
		if len(id) != 11 {
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
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if id, ok := ExtractID(sc.Text()); ok {
			d.tracks = append(d.tracks, model.Track{ID: id})
		}
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
	return fmt.Sprintf("%d tracks (%d with metadata)", len(tracks), rich)
}
