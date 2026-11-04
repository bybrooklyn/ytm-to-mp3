package apple

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	textnorm "golang.org/x/text/unicode/norm"

	ampapi "github.com/bybrooklyn/ytm-to-mp3/internal/apple/amp-api"
)

// Query is a library song to find on Apple Music.
type Query struct {
	Title          string
	Artists        []string // first is the main artist
	Album          string
	DurationMillis int // 0 = unknown
}

// Match is the best Apple Music candidate for a Query.
type Match struct {
	Track Track
	Score float64 // 0..1
	// Sure: title, artist and version all agree. Otherwise it is a plausible match that
	// is worth a look before downloading.
	Sure bool
}

// Thresholds: below minScore nothing is taken; at sureScore and up (with the version
// agreeing) the match is certain.
const (
	minScore  = 0.72
	sureScore = 0.9
)

// Match searches the Apple Music catalog for q and returns the best candidate, or
// ok=false when nothing is close enough. It searches "title artist" first and, when
// that finds nothing good, "title album".
func (c *Client) Match(ctx context.Context, q Query) (Match, bool, error) {
	token, err := c.token(ctx)
	if err != nil {
		return Match{}, false, err
	}
	core := coreTitle(q.Title)
	artist := ""
	if len(q.Artists) > 0 {
		artist = q.Artists[0]
	}
	terms := []string{strings.TrimSpace(core + " " + artist)}
	if q.Album != "" && !strings.EqualFold(q.Album, q.Title) {
		terms = append(terms, strings.TrimSpace(core+" "+q.Album))
	}
	var best Match
	found := false
	for _, term := range terms {
		resp, err := ampapi.Search(ctx, c.storefront, term, "songs", c.language, token, 15, 0)
		if err != nil {
			return Match{}, false, apiErr(err)
		}
		if resp.Results.Songs == nil {
			continue
		}
		for _, cand := range resp.Results.Songs.Data {
			s, sure := score(cand, q)
			if s > best.Score {
				best, found = Match{Track: songTrack(cand, c.storefront), Score: s, Sure: sure}, true
			}
		}
		if found && best.Score >= minScore {
			break
		}
	}
	if !found || best.Score < minScore {
		return Match{}, false, nil
	}
	return best, true, nil
}

func songTrack(d ampapi.SongRespData, storefront string) Track {
	a := d.Attributes
	return Track{
		ID: d.ID, Title: a.Name, Artist: a.ArtistName, Album: a.AlbumName,
		AlbumArtist: a.ArtistName, TrackNumber: a.TrackNumber, DiscNumber: a.DiscNumber,
		DurationMillis: a.DurationInMillis, ISRC: a.Isrc, ReleaseDate: a.ReleaseDate,
		Genre: firstGenre(a.GenreNames), ArtworkURL: a.Artwork.URL, Storefront: storefront,
	}
}

// score rates a candidate 0..1 and says whether it is a sure match. The title (without
// bracketed parts and "feat." credits) and the main artist carry the score; a different
// version (live, remix, instrumental, ...) costs a lot, the album and the duration
// adjust it.
func score(cand ampapi.SongRespData, q Query) (float64, bool) {
	a := cand.Attributes
	wantTitle, haveTitle := norm(coreTitle(q.Title)), norm(coreTitle(a.Name))
	t := similarity(wantTitle, haveTitle)

	ar := 0.0
	// Apple's artists one by one ("Queen & David Bowie"), and whole, for names that hold a
	// separator themselves ("Tyler, The Creator").
	have := append(splitArtistNames(a.ArtistName), a.ArtistName)
	for i, w := range q.Artists {
		for _, h := range have {
			s := similarity(norm(w), norm(h))
			if i > 0 {
				s *= 0.8 // only a featured artist agrees
			}
			ar = max(ar, s)
		}
	}
	if len(q.Artists) == 0 {
		ar = 0.5 // nothing to compare: neither for nor against
	}

	s := 0.65*t + 0.35*ar
	sameVersion := sameMarkers(versionMarkers(q.Title), versionMarkers(a.Name))
	if !sameVersion {
		s -= 0.3
	}
	if q.Album != "" && a.AlbumName != "" && norm(coreTitle(q.Album)) == norm(coreTitle(a.AlbumName)) {
		s += 0.05
	}
	if q.DurationMillis > 0 && a.DurationInMillis > 0 {
		diff := q.DurationMillis - a.DurationInMillis
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff <= 3000:
			s += 0.05
		case diff > 20000:
			s -= 0.2 // a different cut of the song
		}
	}
	s = min(1, max(0, s))
	return s, s >= sureScore && t >= 0.95 && ar >= 0.9 && sameVersion
}

var (
	bracketed = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
	featTail  = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|with)\s+.*$`)
	dashTail  = regexp.MustCompile(`(?i)\s+-\s+(.*(?:remaster|version|edit|mix|live|mono|stereo|from|single).*)$`)
)

// coreTitle is a title without bracketed parts, " - 2011 Remaster"-style tails and
// "feat." credits: "Song (feat. X) [Live]" -> "Song".
func coreTitle(s string) string {
	c := bracketed.ReplaceAllString(s, "")
	c = dashTail.ReplaceAllString(c, "")
	c = featTail.ReplaceAllString(c, "")
	if strings.TrimSpace(c) == "" {
		return s // the whole title was in brackets
	}
	return strings.TrimSpace(c)
}

// markerWords name a different recording or cut of a song. A remaster is the same
// recording, so it isn't one.
var markerWords = []string{"live", "remix", "acoustic", "instrumental", "karaoke", "cover",
	"demo", "edit", "extended", "sped", "slowed", "reverb", "nightcore", "unplugged",
	"orchestral", "piano", "a cappella", "acapella", "mix", "version", "rework", "bootleg"}

// versionMarkers are the marker words in a title (anywhere: brackets, dash tails).
func versionMarkers(title string) map[string]bool {
	n := " " + norm(title) + " "
	out := map[string]bool{}
	for _, w := range markerWords {
		if strings.Contains(n, " "+w+" ") {
			out[w] = true
		}
	}
	// "radio edit"/"album version"/"original mix" are the song as usually released.
	if strings.Contains(n, " radio edit ") || strings.Contains(n, " original mix ") ||
		strings.Contains(n, " album version ") || strings.Contains(n, " single version ") {
		delete(out, "edit")
		delete(out, "mix")
		delete(out, "version")
	}
	return out
}

func sameMarkers(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

var artistSep = regexp.MustCompile(`(?i)\s*(?:,|&|\band\b|\bx\b|\bfeat\.?|\bft\.?|\bfeaturing\b|\bwith\b)\s*`)

// splitArtistNames splits Apple's "A, B & C" artist string.
func splitArtistNames(s string) []string {
	var out []string
	for _, p := range artistSep.Split(s, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^\pL\pN ]+`)

// norm lowercases, folds accents (é -> e, so accented titles still match),
// strips bracket characters and punctuation, and collapses whitespace, so
// "Beyoncé (feat. X) [Remastered]" ~ "beyonce feat x remastered". Letters of
// other scripts are kept, so a Japanese title still compares to itself.
func norm(s string) string {
	s = strings.ToLower(foldAccents(s))
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", "&", " and ", "’", "", "'", "").Replace(s)
	s = nonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// foldAccents strips combining marks via NFD decomposition, mapping accented
// Latin letters to their base letter (é -> e, ö -> o).
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

// similarity is how alike two normalized strings are (0..1): 1 when equal, else the
// token overlap counted both ways (so "song" vs "song part 2" is not a full match).
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	if strings.ReplaceAll(a, " ", "") == strings.ReplaceAll(b, " ", "") {
		return 0.98 // "Some Body" vs "Somebody"
	}
	x, y := tokenOverlap(a, b), tokenOverlap(b, a)
	if x+y == 0 {
		return 0
	}
	return 2 * x * y / (x + y)
}

// tokenOverlap is the fraction of want's tokens present in have (0..1).
func tokenOverlap(want, have string) float64 {
	wt := strings.Fields(want)
	if len(wt) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, t := range strings.Fields(have) {
		set[t] = true
	}
	hit := 0
	for _, t := range wt {
		if set[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(wt))
}
