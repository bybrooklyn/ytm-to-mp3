// Package meta merges the tags of one song as several providers know it. When a song is
// replaced by a better copy (YouTube -> Bandcamp purchase, YouTube -> Apple Music) the new
// file's tags must not simply win: the old file may hold an ISRC, MusicBrainz ids, a
// release date, a genre or lyrics the new one lacks. Merge decides each field by an
// explicit rule and says where every value came from.
//
// It is provider-agnostic and pure: it works on canonical tag names (internal/names:
// title, artist, album, album_artist, date, genre, comment, composer, copyright, label,
// barcode, track "N" or "N/T", disc, isrc, explicit, lyrics, musicbrainz_*), so the callers
// do the reading and writing (internal/tag, through ffmpeg).
package meta

import (
	"regexp"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/names"
)

// Provider names. They match catalog.Source where one exists.
const (
	MusicBrainz      = "musicbrainz"
	BandcampPurchase = "bandcamp-purchase"
	Apple            = "apple"
	Spotify          = "spotify"
	Bandcamp         = "bandcamp"
	SoundCloud       = "soundcloud"
	YouTube          = "youtube"
)

// trust orders providers by how much their metadata is believed, most trusted first. A
// purchase is the artist's own tagging; Apple's is curated and complete; MusicBrainz is
// community-edited; YouTube's tags are scraped from uploads and trusted least. A provider
// that is not listed ranks below all of them.
var trust = []string{BandcampPurchase, Apple, MusicBrainz, Spotify, Bandcamp, SoundCloud, YouTube}

func rank(source string) int {
	for i, s := range trust {
		if s == source {
			return len(trust) - i
		}
	}
	return 0
}

// Set is one provider's view of a song: its tags and, when it has embedded cover art,
// that picture's size.
type Set struct {
	Source string
	Tags   map[string]string
	// CoverW, CoverH are the embedded picture's size in pixels (0 = no picture).
	CoverW, CoverH int
}

// Merged is the outcome.
type Merged struct {
	// Tags is the merged tag set (empty values left out).
	Tags map[string]string
	// From says which provider each tag came from.
	From map[string]string
	// Cover is the index into the merged sets of the one whose embedded picture is best, or
	// -1 when none has a picture.
	Cover int
}

// Merge combines sets. The first set is the base (the file that stays): when two
// providers are equally trusted for a field its value wins; every other rule is explicit:
//
//   - isrc, musicbrainz_*: any valid value, from the most trusted provider that has one. A
//     later-found id is never dropped because the new file lacks it.
//   - title, album, album_artist, label, barcode, composer, copyright, comment, explicit:
//     the most trusted provider's value; an empty value never overrides a filled one.
//   - artist: the most trusted value, unless another provider names the same artists plus
//     more (featured artists are kept).
//   - date: the earliest year of any provider except YouTube (an upload date is not a
//     release date), at the best precision known for that year; YouTube's only when no one
//     else has a date.
//   - genre: the most trusted non-empty one.
//   - track, disc: the most trusted number; a total fills in from any provider whose number
//     agrees.
//   - lyrics: kept when any provider has them (the longest, as the fullest).
//   - cover: the largest square picture; non-square ones count half.
func Merge(sets ...Set) Merged {
	m := Merged{Tags: map[string]string{}, From: map[string]string{}, Cover: -1}
	if len(sets) == 0 {
		return m
	}
	// order: indexes by trust, the base first among equals
	order := make([]int, len(sets))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ { // stable insertion sort by rank desc
		for j := i; j > 0 && rank(sets[order[j]].Source) > rank(sets[order[j-1]].Source); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	val := func(i int, k string) string { return strings.TrimSpace(sets[i].Tags[k]) }
	set := func(k, v string, from int) {
		if v != "" {
			m.Tags[k] = v
			m.From[k] = sets[from].Source
		}
	}
	first := func(k string, ok func(string) bool) (string, int) {
		for _, i := range order {
			if v := val(i, k); v != "" && (ok == nil || ok(v)) {
				return v, i
			}
		}
		return "", -1
	}

	for _, k := range []string{"title", "album", "album_artist", "label", "barcode", "composer", "copyright", "comment", "explicit", "genre"} {
		v, i := first(k, nil)
		set(k, v, i)
	}
	for k, ok := range map[string]func(string) bool{
		"isrc":                      validISRC,
		"musicbrainz_trackid":       validUUID,
		"musicbrainz_albumid":       validUUID,
		"musicbrainz_artistid":      validUUID,
		"musicbrainz_albumartistid": validUUID,
	} {
		v, i := first(k, ok)
		if k == "isrc" {
			v = strings.ToUpper(strings.ReplaceAll(v, "-", ""))
		}
		set(k, v, i)
	}
	// an old tag name for the recording id counts as the track id
	if m.Tags["musicbrainz_trackid"] == "" {
		v, i := first("musicbrainz_recordingid", validUUID)
		set("musicbrainz_trackid", v, i)
	}

	mergeArtist(sets, order, &m)
	mergeDate(sets, order, &m)
	mergeNumber("track", sets, order, &m)
	mergeNumber("disc", sets, order, &m)

	// lyrics: the fullest
	best, bi := "", -1
	for _, i := range order {
		if v := val(i, "lyrics"); len(v) > len(best) {
			best, bi = v, i
		}
	}
	set("lyrics", best, bi)

	// cover: largest square picture
	bestScore := 0
	for _, i := range order {
		if s := coverScore(sets[i]); s > bestScore {
			bestScore, m.Cover = s, i
		}
	}
	if m.Cover >= 0 {
		m.From["cover"] = sets[m.Cover].Source
	}
	return m
}

func mergeArtist(sets []Set, order []int, m *Merged) {
	var top string
	topI := -1
	for _, i := range order {
		if v := strings.TrimSpace(sets[i].Tags["artist"]); v != "" {
			top, topI = v, i
			break
		}
	}
	if topI < 0 {
		return
	}
	// A provider naming the same artists plus more: keep the fuller credit.
	have := credit(top)
	for _, i := range order {
		v := strings.TrimSpace(sets[i].Tags["artist"])
		if v == "" || i == topI {
			continue
		}
		other := credit(v)
		if len(other) > len(have) && contains(other, have) {
			top, topI, have = v, i, other
		}
	}
	m.Tags["artist"], m.From["artist"] = top, sets[topI].Source
}

// credit is a credit's artists folded for comparison.
func credit(s string) []string {
	var out []string
	for _, a := range names.SplitArtists(s) {
		out = append(out, strings.Join(names.ArtistTokens(a), " "))
	}
	return out
}

func contains(all, part []string) bool {
	for _, p := range part {
		found := false
		for _, a := range all {
			if a == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

var dateRe = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?`)

// precision is how much of a date is known: 0 none, 1 year, 2 month, 3 day.
func precision(d string) int {
	m := dateRe.FindStringSubmatch(strings.TrimSpace(d))
	switch {
	case m == nil:
		return 0
	case m[3] != "":
		return 3
	case m[2] != "":
		return 2
	}
	return 1
}

func mergeDate(sets []Set, order []int, m *Merged) {
	pick := func(skipYouTube bool) {
		bestI, bestD := -1, ""
		for _, i := range order {
			d := strings.TrimSpace(sets[i].Tags["date"])
			if d == "" {
				d = strings.TrimSpace(sets[i].Tags["year"])
			}
			if precision(d) == 0 || (skipYouTube && sets[i].Source == YouTube) {
				continue
			}
			d = dateRe.FindString(d)
			switch {
			case bestI < 0, d[:4] < bestD[:4]:
				bestI, bestD = i, d
			case d[:4] == bestD[:4] && precision(d) > precision(bestD) && strings.HasPrefix(d, bestD):
				bestI, bestD = i, d // the same date, known more exactly
			}
		}
		if bestI >= 0 {
			m.Tags["date"], m.From["date"] = bestD, sets[bestI].Source
		}
	}
	pick(true)
	if m.Tags["date"] == "" {
		pick(false)
	}
}

// mergeNumber merges "track" or "disc": the most trusted "N" (or "N/T"), with the total
// from whichever provider agrees on N when the chosen one has none.
func mergeNumber(k string, sets []Set, order []int, m *Merged) {
	parse := func(i int) (n, total int) { return names.ParseTrack(sets[i].Tags[k]) }
	for _, i := range order {
		n, total := parse(i)
		if n <= 0 {
			continue
		}
		if total <= 0 {
			for _, j := range order {
				if jn, jt := parse(j); jn == n && jt > 0 {
					total = jt
					break
				}
			}
		}
		m.Tags[k] = names.TrackTag(n, total)
		m.From[k] = sets[i].Source
		return
	}
}

// coverScore rates a picture: its shorter side, halved when not square. 0 = none.
func coverScore(s Set) int {
	if s.CoverW <= 0 || s.CoverH <= 0 {
		return 0
	}
	side := min(s.CoverW, s.CoverH)
	if d := s.CoverW - s.CoverH; d*d > 4 {
		side /= 2
	}
	return max(side, 1)
}

var (
	isrcRe = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}\d{7}$`)
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func validISRC(s string) bool {
	return isrcRe.MatchString(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "")))
}

func validUUID(s string) bool { return uuidRe.MatchString(strings.TrimSpace(s)) }

// Diff returns the tags of merged that differ from what base already has, ready to hand
// to a tag writer; keys base has and merged lacks are left alone (never cleared).
func Diff(base map[string]string, merged map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range merged {
		if strings.TrimSpace(base[k]) != v {
			out[k] = v
		}
	}
	return out
}
