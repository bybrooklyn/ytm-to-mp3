package takeout

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

func isZip(p string) bool { return strings.EqualFold(filepath.Ext(p), ".zip") }

// zipLooksLikeYouTube reads only the zip's table of contents and reports whether it
// holds YouTube data, whatever the file happens to be called.
func zipLooksLikeYouTube(path string) bool {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer zr.Close()
	if len(zr.File) > maxZipEntries {
		return false
	}
	for _, f := range zr.File {
		n := strings.ToLower(f.Name)
		if strings.Contains(n, "youtube") && strings.HasSuffix(n, ".csv") {
			return true
		}
	}
	return false
}

// CleanPath turns what people actually paste or drag into a terminal into a usable path:
// quotes, file:// links, shell escapes (My\ File.zip) and a leading ~.
func CleanPath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	if rest, ok := strings.CutPrefix(s, "file://"); ok {
		if u, err := url.PathUnescape(rest); err == nil {
			s = u
		}
	}
	if runtime.GOOS != "windows" && strings.Contains(s, `\`) {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, strings.TrimPrefix(s, "~"))
		}
	}
	return s
}

// SplitPaths splits a pasted line holding one or more paths (dragging several files into
// a terminal gives them space-separated, escaped or quoted).
func SplitPaths(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	esc := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, CleanPath(cur.String()))
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			cur.WriteByte(c)
			esc = false
		case c == '\\' && runtime.GOOS != "windows" && quote != '\'':
			esc = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// Source is one importable thing found on disk: a Takeout export (possibly several zips),
// an extracted folder, or a lone CSV.
type Source struct {
	Paths    []string
	Kind     string // "zip", "folder" or "csv"
	Tracks   int
	WithMeta int
	Size     int64
	Mod      time.Time
}

// Name is a short human label.
func (s Source) Name() string {
	base := filepath.Base(s.Paths[0])
	if len(s.Paths) > 1 {
		return base + " + " + itoa(len(s.Paths)-1) + " more"
	}
	return base
}

func itoa(n int) string { return strconv.Itoa(n) }

// part strips Takeout's numbered suffix so takeout-2026...-001.zip and -002.zip group.
var part = regexp.MustCompile(`(?i)[-_ ]?\d{1,3}\.zip$`)

const (
	maxZipsPeeked = 300
	maxSources    = 8
)

// Discover looks for importable exports in dirs (one level deep): zips that contain
// YouTube data under any name, extracted Takeout folders, and library CSVs. Newest first.
func Discover(dirs []string) []Source {
	type found struct {
		path string
		mod  time.Time
		size int64
		kind string
	}
	var cands []found
	seen := map[string]bool{}
	peeked := 0
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			abs, err := filepath.Abs(filepath.Join(d, e.Name()))
			if err != nil || seen[abs] {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			low := strings.ToLower(e.Name())
			switch {
			case strings.HasSuffix(low, ".part") || strings.HasSuffix(low, ".crdownload") || strings.HasSuffix(low, ".download"):
				continue
			case e.IsDir():
				if low == "takeout" || strings.HasPrefix(low, "takeout") {
					if hasYouTube(abs) {
						seen[abs] = true
						cands = append(cands, found{abs, info.ModTime(), 0, "folder"})
					}
				}
			case isZip(low):
				if peeked >= maxZipsPeeked {
					continue
				}
				if strings.HasPrefix(low, "takeout") || func() bool { peeked++; return zipLooksLikeYouTube(abs) }() {
					seen[abs] = true
					cands = append(cands, found{abs, info.ModTime(), info.Size(), "zip"})
				}
			case isCSV(low) && strings.Contains(low, "music library"):
				seen[abs] = true
				cands = append(cands, found{abs, info.ModTime(), info.Size(), "csv"})
			}
		}
	}
	// group the numbered parts of one export
	groups := map[string]*Source{}
	var order []string
	for _, f := range cands {
		key := f.path
		if f.kind == "zip" {
			key = part.ReplaceAllString(f.path, "")
		}
		g := groups[key]
		if g == nil {
			g = &Source{Kind: f.kind}
			groups[key] = g
			order = append(order, key)
		}
		g.Paths = append(g.Paths, f.path)
		g.Size += f.size
		if f.mod.After(g.Mod) {
			g.Mod = f.mod
		}
	}
	var out []Source
	for _, k := range order {
		g := groups[k]
		sort.Strings(g.Paths)
		tracks, err := LoadAll(g.Paths, false)
		if err != nil || len(tracks) == 0 {
			continue // not a music export (or unreadable): don't offer it
		}
		g.Tracks, g.WithMeta = len(tracks), richCount(tracks)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	if len(out) > maxSources {
		out = out[:maxSources]
	}
	return out
}

func richCount(ts []model.Track) int {
	n := 0
	for _, t := range ts {
		if t.Title != "" {
			n++
		}
	}
	return n
}

func hasYouTube(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), "youtube") {
			return true
		}
	}
	return false
}

// Inspect loads paths and summarises them, for a "this is what I found" preview.
func Inspect(paths []string) (Source, error) {
	tracks, err := LoadAll(paths, false)
	if err != nil {
		return Source{}, err
	}
	s := Source{Paths: paths, Tracks: len(tracks), WithMeta: richCount(tracks), Kind: "csv"}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			s.Size += st.Size()
			if st.ModTime().After(s.Mod) {
				s.Mod = st.ModTime()
			}
			if st.IsDir() {
				s.Kind = "folder"
			} else if isZip(p) {
				s.Kind = "zip"
			}
		}
	}
	return s, nil
}
