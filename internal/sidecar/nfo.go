package sidecar

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/tag"
)

// The element names follow Kodi's music NFO files (kodi.wiki "NFO files/Albums" and
// "NFO files/Artists"): <album> with title, musicBrainzAlbumID, genre, label, year,
// albumArtistCredits and <track> (position, title, duration, musicBrainzTrackID), and
// <artist> with name, musicBrainzArtistID, genre. Jellyfin reads the same files.
// Everything is sorted, so the same music always gives the same bytes.

type xw struct{ b bytes.Buffer }

func (w *xw) open(indent int, name string)  { fmt.Fprintf(&w.b, "%s<%s>\n", pad(indent), name) }
func (w *xw) close(indent int, name string) { fmt.Fprintf(&w.b, "%s</%s>\n", pad(indent), name) }
func pad(n int) string                      { return strings.Repeat("  ", n) }

// el writes <name>value</name> unless value is empty.
func (w *xw) el(indent int, name, value string) {
	value = cleanXML(value)
	if value == "" {
		return
	}
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(value))
	fmt.Fprintf(&w.b, "%s<%s>%s</%s>\n", pad(indent), name, esc.String(), name)
}

func header() *xw {
	w := &xw{}
	w.b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n<!-- " + Marker + " -->\n")
	return w
}

// cleanXML drops characters XML 1.0 cannot hold and trims.
func cleanXML(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r', r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000 && r <= 0x10FFFF:
			return r
		}
		return -1
	}, s)
	return strings.TrimSpace(s)
}

var (
	yearRe = regexp.MustCompile(`^(\d{4})`)
	dateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
)

// numPair reads "N" or "N/T".
func numPair(s string) (n, total int) {
	a, b, _ := strings.Cut(strings.TrimSpace(s), "/")
	n, _ = strconv.Atoi(strings.TrimSpace(a))
	total, _ = strconv.Atoi(strings.TrimSpace(b))
	return
}

// common is the most frequent non-empty value of get over infos (ties: the smallest).
func common(infos []tag.Info, get func(tag.Info) string) string {
	count := map[string]int{}
	for _, in := range infos {
		if v := cleanXML(get(in)); v != "" {
			count[v]++
		}
	}
	best, n := "", 0
	for v, c := range count {
		if c > n || (c == n && v < best) {
			best, n = v, c
		}
	}
	return best
}

func tagOf(k ...string) func(tag.Info) string {
	return func(in tag.Info) string {
		for _, key := range k {
			if v := strings.TrimSpace(in.Tags[key]); v != "" {
				return v
			}
		}
		return ""
	}
}

func genres(infos []tag.Info) []string {
	spell := map[string]string{} // lower-case -> the smallest spelling seen
	for _, in := range infos {
		for _, g := range strings.FieldsFunc(in.Tags["genre"], func(r rune) bool { return r == ';' || r == '\x00' }) {
			if g = cleanXML(g); g != "" {
				if old, ok := spell[strings.ToLower(g)]; !ok || g < old {
					spell[strings.ToLower(g)] = g
				}
			}
		}
	}
	out := make([]string, 0, len(spell))
	for _, g := range spell {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func albumArtist(infos []tag.Info, fallback string) string {
	if v := common(infos, tagOf("album_artist")); v != "" {
		return v
	}
	if v := common(infos, tagOf("artist")); v != "" {
		return v
	}
	return fallback
}

type nfoTrack struct {
	disc, pos int
	path      string
	in        tag.Info
}

// albumNFO is album.nfo for the songs of one album folder.
func albumNFO(dir string, infos []tag.Info) []byte {
	title := common(infos, tagOf("album"))
	if title == "" {
		title = filepath.Base(dir)
	}
	artist := albumArtist(infos, filepath.Base(filepath.Dir(dir)))
	date := ""
	for _, in := range infos { // the most precise date; equal ones go to the smaller
		d := cleanXML(in.Tags["date"])
		if len(d) > len(date) || (len(d) == len(date) && d != "" && d < date) {
			date = d
		}
	}

	tracks := make([]nfoTrack, len(infos))
	discs := 0
	for i, in := range infos {
		d, dt := numPair(in.Tags["disc"])
		p, _ := numPair(in.Tags["track"])
		if d == 0 {
			d = 1
		}
		discs = max(discs, d, dt)
		tracks[i] = nfoTrack{disc: d, pos: p, path: in.Path, in: in}
	}
	sort.SliceStable(tracks, func(i, j int) bool {
		a, b := tracks[i], tracks[j]
		if a.disc != b.disc {
			return a.disc < b.disc
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.path < b.path
	})

	w := header()
	w.open(0, "album")
	w.el(1, "title", title)
	w.el(1, "musicBrainzAlbumID", common(infos, tagOf("musicbrainz_albumid")))
	w.el(1, "artist", artist)
	for _, g := range genres(infos) {
		w.el(1, "genre", g)
	}
	w.el(1, "label", common(infos, tagOf("label")))
	if m := yearRe.FindString(date); m != "" {
		w.el(1, "year", m)
	}
	if m := dateRe.FindStringSubmatch(date); m != nil {
		w.el(1, "releasedate", m[0])
	}
	w.open(1, "albumArtistCredits")
	w.el(2, "artist", artist)
	w.el(2, "musicBrainzArtistID", common(infos, tagOf("musicbrainz_albumartistid")))
	w.close(1, "albumArtistCredits")
	for _, t := range tracks {
		w.open(1, "track")
		if t.pos > 0 {
			w.el(2, "position", strconv.Itoa(t.pos))
		}
		if discs > 1 {
			w.el(2, "disc", strconv.Itoa(t.disc))
		}
		title := t.in.Tags["title"]
		if strings.TrimSpace(title) == "" {
			title = strings.TrimSuffix(filepath.Base(t.path), filepath.Ext(t.path))
		}
		w.el(2, "title", title)
		if t.in.Duration > 0 {
			w.el(2, "duration", strconv.Itoa(int(math.Round(t.in.Duration))))
		}
		w.el(2, "musicBrainzTrackID", t.in.Tags["musicbrainz_trackid"])
		w.el(2, "isrc", t.in.Tags["isrc"])
		w.close(1, "track")
	}
	w.close(0, "album")
	return w.b.Bytes()
}

// artistNFO is artist.nfo for every song below one artist folder.
func artistNFO(dir string, infos []tag.Info) []byte {
	name := albumArtist(infos, filepath.Base(dir))
	w := header()
	w.open(0, "artist")
	w.el(1, "name", name)
	w.el(1, "musicBrainzArtistID", common(infos, tagOf("musicbrainz_albumartistid")))
	for _, g := range genres(infos) {
		w.el(1, "genre", g)
	}
	w.close(0, "artist")
	return w.b.Bytes()
}
