package apple

import (
	"context"
	"regexp"
	"strings"

	ampapi "github.com/bybrooklyn/ytm-to-mp3/internal/apple/amp-api"
)

// Match searches the Apple Music catalog for a library track and returns the
// best song match, or ok=false when nothing is close enough. durationMillis
// may be 0 when unknown.
func (c *Client) Match(ctx context.Context, title, artist, album string, durationMillis int) (Track, bool, error) {
	token, err := c.token()
	if err != nil {
		return Track{}, false, err
	}
	term := strings.TrimSpace(title + " " + artist)
	resp, err := ampapi.Search(c.storefront, term, "songs", c.language, token, 15, 0)
	if err != nil {
		return Track{}, false, err
	}
	if resp.Results.Songs == nil || len(resp.Results.Songs.Data) == 0 {
		return Track{}, false, nil
	}

	wantTitle := norm(title)
	wantArtist := norm(artist)
	wantAlbum := norm(album)

	bestScore := -1.0
	var best ampapi.SongRespData
	for _, cand := range resp.Results.Songs.Data {
		s := score(cand, wantTitle, wantArtist, wantAlbum, durationMillis)
		if s > bestScore {
			bestScore = s
			best = cand
		}
	}
	// Require a solid title+artist match; below this is likely the wrong song.
	if bestScore < 0.6 {
		return Track{}, false, nil
	}
	a := best.Attributes
	return Track{
		ID: best.ID, Title: a.Name, Artist: a.ArtistName, Album: a.AlbumName,
		AlbumArtist: a.ArtistName, TrackNumber: a.TrackNumber, DiscNumber: a.DiscNumber,
		DurationMillis: a.DurationInMillis, ISRC: a.Isrc, ReleaseDate: a.ReleaseDate,
		ArtworkURL: a.Artwork.URL, Storefront: c.storefront,
	}, true, nil
}

// score rates a candidate 0..1 by title and artist similarity, with small
// bonuses for album match and duration proximity.
func score(cand ampapi.SongRespData, wantTitle, wantArtist, wantAlbum string, wantDur int) float64 {
	a := cand.Attributes
	ct, ca, cal := norm(a.Name), norm(a.ArtistName), norm(a.AlbumName)

	titleScore := tokenOverlap(wantTitle, ct)
	artistScore := tokenOverlap(wantArtist, ca)
	s := 0.6*titleScore + 0.3*artistScore

	if wantAlbum != "" && cal != "" {
		s += 0.05 * tokenOverlap(wantAlbum, cal)
	}
	if wantDur > 0 && a.DurationInMillis > 0 {
		diff := wantDur - a.DurationInMillis
		if diff < 0 {
			diff = -diff
		}
		if diff <= 3000 { // within 3 seconds
			s += 0.05
		}
	}
	return s
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9 ]+`)

// norm lowercases, strips bracketed noise and punctuation, and collapses
// whitespace, so "Song (feat. X) [Remastered]" ~ "song feat x remastered".
func norm(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", "&", " and ").Replace(s)
	s = nonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
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
