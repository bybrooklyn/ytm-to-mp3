package lyrics

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// Query is everything a provider is ever told about a song: nothing else is sent.
type Query struct {
	Title, Artist, Album string
	Duration             time.Duration // 0 = unknown
}

// Candidate is one record a provider offered.
type Candidate struct {
	Provider     string
	Rank         int // position of the provider in the configured order (0 = preferred)
	Title        string
	Artist       string
	Album        string
	Duration     time.Duration // 0 = the record has no length
	Synced       []Line
	Plain        string
	Instrumental bool
}

// Kind says what a Result holds.
type Kind string

const (
	KindSynced       Kind = "synced"
	KindPlain        Kind = "plain"
	KindInstrumental Kind = "instrumental" // the song has no words: nothing to store
)

// Matching rules. Synced lyrics are only as good as their timing, so they need a record of
// (nearly) the same length; plain text only needs the same words.
const (
	// SyncedTolerance: a synced record whose length differs from the song's by more than
	// this is another cut, and its stamps would drift.
	SyncedTolerance = 3 * time.Second
	// PlainTolerance: the words of a version a few seconds longer are still the words.
	PlainTolerance = 15 * time.Second
	// unknownPenalty is the length difference assumed when either side doesn't know it.
	unknownPenalty = 4 * time.Second
	minSimilarity  = 600 // permille: below this the record is a different song
	// minLines: fewer lines than this is a stub, not lyrics (LRCLIB has "probe" records).
	minLines = 4
)

// noise words: brackets or dash suffixes containing one are upload decoration, not part of
// the song's name. Live, acoustic, demo, remix and the like are NOT here: they are other
// recordings (see versionWords).
var noiseWords = setOf("official", "video", "audio", "lyric", "lyrics", "mv", "hd", "hq", "4k",
	"visualizer", "visualiser", "explicit", "clean", "remaster", "remastered", "mono", "stereo",
	"deluxe", "edition", "bonus", "track", "single", "album", "version", "feat", "ft", "featuring",
	"with", "prod", "topic")

// versionWords mark a different recording of the song. A query and a record must agree on
// the set of these they carry: a live take must not get the studio version's timing.
var versionWords = setOf("live", "acoustic", "demo", "remix", "instrumental", "karaoke",
	"cover", "extended", "slowed", "reverb", "sped", "nightcore", "unplugged", "orchestral",
	"session", "mix", "edit", "radio", "rework", "reprise", "piano", "acapella")

func setOf(w ...string) map[string]bool {
	m := make(map[string]bool, len(w))
	for _, x := range w {
		m[x] = true
	}
	return m
}

// groups splits s into the text outside brackets and the contents of each bracket group.
func groups(s string) (outside string, inside []string) {
	var out, cur strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{', '（', '【':
			if depth == 0 {
				cur.Reset()
			}
			depth++
		case ')', ']', '}', '）', '】':
			if depth > 0 {
				depth--
				if depth == 0 {
					inside = append(inside, cur.String())
				}
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			} else {
				cur.WriteRune(r)
			}
		}
	}
	if depth > 0 {
		inside = append(inside, cur.String())
	}
	return out.String(), inside
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// Versions lists the version markers ("live", "remix", ...) a title carries in brackets or
// after a dash, sorted. Featured credits and remaster notes are not versions.
func Versions(title string) []string {
	outside, inside := groups(title)
	parts := inside
	if i := strings.LastIndex(outside, " - "); i >= 0 {
		parts = append(parts, outside[i+3:])
	}
	found := map[string]bool{}
	for _, p := range parts {
		for _, w := range words(p) {
			if versionWords[w] {
				found[w] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for w := range found {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// Normalize lower-cases a title or artist and drops decoration: bracket groups that carry
// noise or version words, "feat." tails, " - Topic" channel suffixes, dash suffixes made of
// noise ("- Remastered 2011") and punctuation. Words inside other brackets are kept, since
// "(Turning in Somersaults)" is part of a name.
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(strings.ToLower(s), " - topic"); i >= 0 && strings.TrimSpace(s[i+8:]) == "" {
		s = s[:i]
	}
	outside, inside := groups(s)
	// a dash suffix is decoration when every word of it is noise, a version marker or a number
	if i := strings.LastIndex(outside, " - "); i >= 0 {
		decoration := true
		for _, w := range words(outside[i+3:]) {
			if !noiseWords[w] && !versionWords[w] && !allDigits(w) {
				decoration = false
			}
		}
		if decoration {
			outside = outside[:i]
		}
	}
	keep := []string{outside}
	for _, g := range inside {
		decor := false
		for _, w := range words(g) {
			if noiseWords[w] || versionWords[w] {
				decor = true
			}
		}
		if !decor {
			keep = append(keep, g)
		}
	}
	var out []string
	for _, w := range words(strings.Join(keep, " ")) {
		// "feat. X": everything after is a guest, not part of the name
		if (w == "feat" || w == "ft" || w == "featuring") && len(out) > 0 {
			break
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// Similarity compares two titles or artists after Normalize, in permille (1000 = same).
// One empty side is neutral (500).
func Similarity(a, b string) int {
	na, nb := Normalize(a), Normalize(b)
	if na == "" || nb == "" {
		return 500
	}
	if na == nb {
		return 1000
	}
	ta, tb := strings.Fields(na), strings.Fields(nb)
	set := map[string]bool{}
	for _, w := range tb {
		set[w] = true
	}
	common := 0
	for _, w := range ta {
		if set[w] {
			common++
		}
	}
	dice := 2000 * common / (len(ta) + len(tb))
	if common == min(len(ta), len(tb)) && common > 0 {
		dice = max(dice, 800) // one name inside the other ("Song" in "Song Name")
	}
	return dice
}

// artistSimilarity compares artist lists ("A, B", "A & B", "A feat. B") by their best member.
func artistSimilarity(a, b string) int {
	best := 0
	for _, x := range splitArtists(a) {
		for _, y := range splitArtists(b) {
			best = max(best, Similarity(x, y))
		}
	}
	if best == 0 {
		return Similarity(a, b)
	}
	return best
}

func splitArtists(s string) []string {
	r := strings.NewReplacer(" feat. ", "\x00", " ft. ", "\x00", " featuring ", "\x00", " & ", "\x00",
		", ", "\x00", " x ", "\x00", " and ", "\x00", "; ", "\x00", " / ", "\x00", " with ", "\x00")
	var out []string
	for _, p := range strings.Split(r.Replace(s), "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return append(out, s)
}

func sameVersion(a, b string) bool {
	x, y := Versions(a), Versions(b)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

// durationDiff is |candidate - query|, or unknownPenalty when either is unknown.
func durationDiff(c Candidate, q Query) time.Duration {
	if c.Duration <= 0 || q.Duration <= 0 {
		return unknownPenalty
	}
	d := c.Duration - q.Duration
	if d < 0 {
		d = -d
	}
	return d
}

// Pick is the chosen lyrics.
type Pick struct {
	Kind     Kind
	Provider string
	Synced   []Line
	Plain    string
	Title    string // the record's own title and artist (for the header)
	Artist   string
	Album    string
	Duration time.Duration // the record's length (0 = unknown)
}

// Choose picks the best believable candidate for q, or reports ok=false.
//
// Rules, in order: a record for another song (title/artist unlike, or a different version:
// live vs studio, remix, instrumental...) is dropped; stubs under 4 lines are dropped; a
// synced record counts as synced only when its length is within SyncedTolerance of the
// song's and its last stamp fits inside the song (otherwise its plain text is used if it
// has some and is within PlainTolerance). Then synced beats plain whatever the length, the
// closest length wins, then the better title/artist match, then more lines, then the
// provider order. When only instrumental records exist the answer is KindInstrumental.
func Choose(cands []Candidate, q Query) (Pick, bool) {
	type scored struct {
		key  [5]int
		pick Pick
	}
	var best *scored
	instrumental := false
	var instrumentalFrom string
	for _, c := range cands {
		ts, as := Similarity(c.Title, q.Title), artistSimilarity(c.Artist, q.Artist)
		sim := (ts*2 + as) / 3
		// 500 is "unknown on one side": neutral, not a mismatch
		if (ts < minSimilarity && ts != 500) || (as < minSimilarity && as != 500) || (c.Title != "" && !sameVersion(c.Title, q.Title)) {
			continue
		}
		if c.Instrumental {
			// only believed when the length agrees: a different cut may well have words
			if durationDiff(c, q) <= PlainTolerance && c.Title != "" {
				instrumental, instrumentalFrom = true, c.Provider
			}
			continue
		}
		diff := durationDiff(c, q)
		synced := c.Synced
		known := c.Duration > 0 && q.Duration > 0
		if SyncedLines(synced) < minLines || (known && diff > SyncedTolerance) || overruns(synced, q.Duration) {
			synced = nil
		}
		plain := c.Plain
		if strings.TrimSpace(plain) == "" && len(synced) == 0 && SyncedLines(c.Synced) >= minLines {
			plain = Plain(c.Synced) // synced for another cut still gives the words
		}
		p := Pick{Provider: c.Provider, Title: c.Title, Artist: c.Artist, Album: c.Album, Duration: c.Duration}
		var lines int
		syncedBonus := 0
		switch {
		case len(synced) > 0:
			p.Kind, p.Synced, lines, syncedBonus = KindSynced, synced, SyncedLines(synced), 1
		case PlainLines(plain) >= minLines && diff <= PlainTolerance:
			p.Kind, p.Plain, lines = KindPlain, strings.TrimSpace(plain), PlainLines(plain)
		default:
			continue
		}
		key := [5]int{syncedBonus, -int(diff.Round(time.Second) / time.Second), sim, lines, -c.Rank}
		if best == nil || less(best.key, key) {
			best = &scored{key, p}
		}
	}
	if best != nil {
		return best.pick, true
	}
	if instrumental {
		return Pick{Kind: KindInstrumental, Provider: instrumentalFrom}, true
	}
	return Pick{}, false
}

func less(a, b [5]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// overruns reports a synced record whose last stamp is well past the end of the song, a
// sure sign of a different (longer) cut.
func overruns(lines []Line, dur time.Duration) bool {
	if dur <= 0 || len(lines) == 0 {
		return false
	}
	return lines[len(lines)-1].At > dur+2*time.Second
}
