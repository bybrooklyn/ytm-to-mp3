package library

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/meta"
	"github.com/bybrooklyn/mfetchy/internal/names"
)

// Entry is one song of the combined library: every account's copy of it, and the best
// metadata the copies agree on.
type Entry struct {
	// Track holds the best value of each field across the copies (internal/meta's rules).
	Track catalog.Track
	// Key is Track.Key(): the identity the music folder's database uses.
	Key string
	// Songs are the accounts' copies, including any whole-album purchase covering it.
	Songs []Song
	// Whole marks an album purchase that matched no song of any account: it is
	// downloaded as the album.
	Whole bool
}

// Accounts is the names of the accounts that have this song, sorted.
func (e Entry) Accounts() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range e.Songs {
		if !seen[s.From] {
			seen[s.From] = true
			out = append(out, s.From)
		}
	}
	sort.Strings(out)
	return out
}

// Best is the copy whose account can supply the best audio (catalog.Rank); the first
// account read wins a tie.
func (e Entry) Best() Song {
	best := e.Songs[0]
	for _, s := range e.Songs[1:] {
		if catalog.Rank(s.Supply) > catalog.Rank(best.Supply) {
			best = s
		}
	}
	return best
}

var featRe = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s[^\)\]]*[\)\]]`)

// titleKey is a song title folded for matching, without a "(feat. X)" credit: the same
// song is "Song" on one service and "Song (feat. X)" on another.
func titleKey(t string) string {
	return catalog.NameKey(featRe.ReplaceAllString(t, ""), "")
}

// albumKey folds an album title for matching: editions are one album, and a song's
// "Album - Single" is the album's name.
func albumKey(a string) string {
	for _, suf := range []string{" - Single", " - EP"} {
		a = strings.TrimSuffix(a, suf)
	}
	return names.EditionKey(a)
}

func isrcOf(t catalog.Track) string { return strings.ToUpper(strings.TrimSpace(t.ISRC)) }

// artistsMatch reports whether two songs are by the same act (any of their credited names).
func artistsMatch(a, b catalog.Track) bool {
	for _, x := range append([]string{a.AlbumArtist}, a.Artists...) {
		for _, y := range append([]string{b.AlbumArtist}, b.Artists...) {
			if x != "" && y != "" && names.SameArtist(x, y) {
				return true
			}
		}
	}
	return false
}

// SameSong is the identity rule for two copies of a song: equal ISRCs when both have one
// (different ISRCs are different recordings whatever their names say); otherwise the same
// title (without a feat. credit) by the same act, on the same album when both name one
// (editions are one album), and of the same length (within 4 s) when both know it.
func SameSong(a, b catalog.Track) bool {
	if ia, ib := isrcOf(a), isrcOf(b); ia != "" && ib != "" {
		return ia == ib
	}
	if titleKey(a.Title) != titleKey(b.Title) || !artistsMatch(a, b) {
		return false
	}
	if a.Album != "" && b.Album != "" && albumKey(a.Album) != albumKey(b.Album) {
		return false
	}
	if a.DurationMillis > 0 && b.DurationMillis > 0 {
		d := a.DurationMillis - b.DurationMillis
		return d < 4000 && d > -4000
	}
	return true
}

// Combine merges the accounts' songs into one library. Songs match by SameSong; an
// account's whole-album purchase attaches to every matching song of that album found in
// the other accounts (so a YouTube copy of a Bandcamp-purchased album is known to be
// supplied by the purchase), or stands as an album entry of its own when it matches none.
// The order is that of the first account's songs, then the others'.
func Combine(songs []Song) []Entry {
	var entries []Entry
	byTitle := map[string][]int{}
	byISRC := map[string]int{}

	find := func(s Song) int {
		if i, ok := byISRC[isrcOf(s.Track)]; ok && isrcOf(s.Track) != "" {
			return i
		}
		for _, i := range byTitle[titleKey(s.Title)] {
			for _, m := range entries[i].Songs {
				if !m.Whole && SameSong(m.Track, s.Track) {
					return i
				}
			}
		}
		return -1
	}
	var wholes []Song
	for _, s := range songs {
		if strings.TrimSpace(s.Title) == "" {
			continue
		}
		if s.Whole {
			wholes = append(wholes, s)
			continue
		}
		i := find(s)
		if i < 0 {
			i = len(entries)
			entries = append(entries, Entry{})
			k := titleKey(s.Title)
			byTitle[k] = append(byTitle[k], i)
		}
		entries[i].Songs = append(entries[i].Songs, s)
		if is := isrcOf(s.Track); is != "" {
			if _, ok := byISRC[is]; !ok {
				byISRC[is] = i
			}
		}
	}
	for i := range entries {
		entries[i].Track = mergeTracks(entries[i].Songs)
		entries[i].Key = entries[i].Track.Key()
	}

	for _, w := range wholes {
		covered := false
		for i := range entries {
			t := entries[i].Track
			if t.Album != "" && albumKey(t.Album) == albumKey(w.Title) && artistsMatch(t, w.Track) {
				entries[i].Songs = append(entries[i].Songs, w)
				covered = true
			}
		}
		if !covered {
			t := w.Track
			t.Album = w.Title
			entries = append(entries, Entry{
				Track: t, Key: "album:" + catalog.NameKey(albumKey(w.Title), w.PrimaryArtist()),
				Songs: []Song{w}, Whole: true,
			})
		}
	}
	return entries
}

// mergeTracks is the best Track from a song's copies, field by field (internal/meta).
func mergeTracks(songs []Song) catalog.Track {
	if len(songs) == 1 {
		return songs[0].Track
	}
	sets := make([]meta.Set, len(songs))
	for i, s := range songs {
		t := s.Track
		tags := map[string]string{
			"title": t.Title, "artist": strings.Join(t.Artists, ", "), "album": t.Album,
			"album_artist": t.AlbumArtist, "date": t.ReleaseDate, "genre": t.Genre, "isrc": t.ISRC,
		}
		if t.TrackNo > 0 {
			tags["track"] = names.TrackTag(t.TrackNo, t.TrackTotal)
		}
		if t.DiscNo > 0 {
			tags["disc"] = names.TrackTag(t.DiscNo, t.DiscTotal)
		}
		src := string(t.Origin)
		if src == "" {
			src = string(s.Supply)
		}
		sets[i] = meta.Set{Source: src, Tags: tags}
	}
	m := meta.Merge(sets...)
	out := songs[0].Track // anything meta doesn't carry starts from the first copy
	out.Title, out.Album, out.AlbumArtist = m.Tags["title"], m.Tags["album"], m.Tags["album_artist"]
	out.ReleaseDate, out.Genre, out.ISRC = m.Tags["date"], m.Tags["genre"], m.Tags["isrc"]
	out.TrackNo, out.TrackTotal = names.ParseTrack(m.Tags["track"])
	out.DiscNo, out.DiscTotal = names.ParseTrack(m.Tags["disc"])
	out.Artists = nil
	for _, s := range songs {
		if strings.Join(s.Artists, ", ") == m.Tags["artist"] {
			out.Artists = append([]string(nil), s.Artists...)
			break
		}
	}
	if out.Artists == nil && m.Tags["artist"] != "" {
		out.Artists = []string{m.Tags["artist"]}
	}
	// What meta has no rule for: the longest known duration, the art of the best-ranked
	// account that has some.
	out.DurationMillis, out.ArtworkURL = 0, ""
	bestArt := -1
	for _, s := range songs {
		out.DurationMillis = max(out.DurationMillis, s.DurationMillis)
		if r := catalog.Rank(s.Origin); s.ArtworkURL != "" && r > bestArt {
			bestArt, out.ArtworkURL = r, s.ArtworkURL
		}
	}
	// Provenance of the identity: the best audio source's id.
	b := songs[0]
	for _, s := range songs[1:] {
		if catalog.Rank(s.Supply) > catalog.Rank(b.Supply) {
			b = s
		}
	}
	out.Origin, out.OriginID = b.Origin, b.OriginID
	return out
}

// String is a one-line description for logs.
func (e Entry) String() string {
	return fmt.Sprintf("%s [%s]", e.Track.Label(), strings.Join(e.Accounts(), "+"))
}
