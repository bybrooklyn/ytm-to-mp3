// Package catalog is ytm's source-agnostic song identity: one canonical Track
// type that any metadata source (Apple Music, Spotify, YouTube, a Takeout
// export) maps onto, so the downloader can treat them alike and fetch each
// song from the best available audio source.
package catalog

import (
	"regexp"
	"strings"
	"unicode"

	textnorm "golang.org/x/text/unicode/norm"
)

// Source names where a song's metadata came from or where its audio is fetched.
type Source string

const (
	Apple   Source = "apple"   // Apple Music (256 kbps AAC)
	YouTube Source = "youtube" // YouTube / YouTube Music (Opus/AAC)
	Spotify Source = "spotify" // metadata only; Spotify audio is never downloaded
)

// Rank orders audio sources by quality for best-source selection (higher is
// better). Spotify is metadata only, so it is not a download source.
func Rank(s Source) int {
	switch s {
	case Apple:
		return 3
	case YouTube:
		return 2
	default:
		return 0
	}
}

// Track is one song, independent of where its metadata or audio comes from.
type Track struct {
	Title          string
	Artists        []string // first is the main artist
	Album          string
	AlbumArtist    string
	TrackNo        int
	TrackTotal     int
	DiscNo         int
	DiscTotal      int
	DurationMillis int
	ISRC           string // stable cross-source id when known (Apple has it; Spotify embed does not)
	ArtworkURL     string
	ReleaseDate    string
	Genre          string

	// Origin is the metadata source and its id there (e.g. a Spotify track id),
	// for provenance and per-source dedup.
	Origin   Source
	OriginID string
}

// PrimaryArtist is the main artist, or "" when unknown.
func (t Track) PrimaryArtist() string {
	if len(t.Artists) > 0 {
		return t.Artists[0]
	}
	return ""
}

// Key identifies the same song across sources and runs: its ISRC when known,
// else a normalized title + main artist (+ album). Two imports of one song
// share a Key only when both carry the ISRC, or their names agree.
func (t Track) Key() string {
	if isrc := strings.ToUpper(strings.TrimSpace(t.ISRC)); isrc != "" {
		return "isrc:" + isrc
	}
	parts := []string{norm(t.Title), norm(t.PrimaryArtist())}
	if t.Album != "" {
		parts = append(parts, norm(t.Album))
	}
	return "name:" + strings.Join(parts, "|")
}

// NameKey is the loose identity of a song by name alone, "title|artist" folded the way
// Key folds them, for finding a song already held when the other side has no ISRC (a
// YouTube library song, a Spotify track).
func NameKey(title, artist string) string {
	return norm(title) + "|" + norm(artist)
}

// Label is a short "Artist - Title" for messages.
func (t Track) Label() string {
	if a := t.PrimaryArtist(); a != "" {
		return a + " - " + t.Title
	}
	return t.Title
}

var nonAlnum = regexp.MustCompile(`[^\pL\pN ]+`)

// norm lowercases, folds accents and strips punctuation, so "Beyoncé (Live)"
// and "beyonce live" compare equal.
func norm(s string) string {
	s = strings.ToLower(foldAccents(s))
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", "&", " and ", "’", "", "'", "").Replace(s)
	s = nonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func foldAccents(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range textnorm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		out = append(out, r)
	}
	return textnorm.NFC.String(string(out))
}
