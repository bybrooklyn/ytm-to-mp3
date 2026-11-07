package names

import (
	"regexp"
	"strings"
	"unicode"

	textnorm "golang.org/x/text/unicode/norm"
)

// FoldKey is the identity of a folder or file-name component for "is this the same
// spelling?": Unicode-normalised (NFKC, so macOS's decomposed names equal composed
// ones), lower-cased, with runs of space collapsed and trailing dots, spaces and
// punctuation dropped. "MASTER BOOT RECORD" and "Master Boot Record" share a key;
// accents are NOT folded ("Beyoncé" and "Beyonce" stay apart), because those are
// different spellings someone chose.
func FoldKey(s string) string {
	s = textnorm.NFKC.String(s)
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRightFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) && r != ')' && r != ']' })
	return s
}

// editionWords are the words of an edition marker; strongEdition are the ones that make
// a bracket an edition marker at all ("(2011)" alone is a year, "(Live)" is a different
// recording, neither is a marker).
var (
	editionWords = map[string]bool{
		"deluxe": true, "expanded": true, "special": true, "standard": true, "collectors": true,
		"collector's": true, "collector’s": true, "anniversary": true, "super": true, "ultimate": true,
		"platinum": true, "bonus": true, "track": true, "tracks": true, "international": true,
		"tour": true, "digital": true, "remaster": true, "remastered": true, "edition": true,
		"version": true,
	}
	strongEdition = map[string]bool{
		"deluxe": true, "expanded": true, "special": true, "standard": true, "collectors": true,
		"collector's": true, "collector’s": true, "anniversary": true, "super": true, "ultimate": true,
		"platinum": true, "bonus": true, "remaster": true, "remastered": true, "edition": true,
	}
	ordinalRe = regexp.MustCompile(`^\d+(?:st|nd|rd|th)$`)
	yearRe    = regexp.MustCompile(`^(?:19|20)\d\d$`)
	// a trailing "(...)", "[...]" or " - ..." group
	tailGroupRe = regexp.MustCompile(`^(.*?)\s*(?:[\(\[]([^\)\]]*)[\)\]]|\s-\s(.+?))\s*$`)
)

// isEditionMarker reports whether phrase is only edition words ("Deluxe Edition",
// "2011 Remaster", "Expanded", "Super Deluxe"), with at least one strong one.
func isEditionMarker(phrase string) bool {
	toks := strings.Fields(strings.ToLower(textnorm.NFKC.String(phrase)))
	if len(toks) == 0 || len(toks) > 5 {
		return false
	}
	strong := false
	for _, t := range toks {
		t = strings.Trim(t, ",.")
		switch {
		case editionWords[t], ordinalRe.MatchString(t), yearRe.MatchString(t):
		default:
			return false
		}
		if strongEdition[t] {
			strong = true
		}
	}
	return strong
}

// EditionBase is an album title without a clear edition marker at its end: "(Deluxe)",
// "(Deluxe Edition)", "[Expanded Edition]", "- Special Edition", "(2011 Remaster)", a
// trailing " +", or a bare trailing "Deluxe Edition". Anything that could be a different
// album ("(Live)", "Vol. 2", "(Instrumental)", "(Acoustic)", "Part II") is kept, and a
// title that is nothing but a marker is returned unchanged.
func EditionBase(album string) string {
	s := strings.TrimSpace(album)
	for i := 0; i < 3; i++ { // "Album (Deluxe) [Remastered]"
		prev := s
		if t := strings.TrimSpace(strings.TrimSuffix(s, "+")); strings.HasSuffix(s, "+") && t != "" {
			s = t
		}
		if m := tailGroupRe.FindStringSubmatch(s); m != nil && m[1] != "" {
			if phrase := m[2] + m[3]; isEditionMarker(phrase) {
				s = strings.TrimSpace(m[1])
			}
		} else if toks := strings.Fields(s); len(toks) > 2 {
			// a bare "... Deluxe Edition": the longest tail of edition words
			for k := 1; k <= 3 && k < len(toks); k++ {
				if isEditionMarker(strings.Join(toks[len(toks)-k:], " ")) {
					s = strings.Join(toks[:len(toks)-k], " ")
				}
			}
		}
		if s == prev {
			break
		}
	}
	if s == "" {
		return album
	}
	return s
}

// EditionKey is FoldKey of EditionBase: two album folders with the same key are editions
// of one album ("CHROMAKOPIA" and "CHROMAKOPIA +").
func EditionKey(album string) string { return FoldKey(EditionBase(album)) }

var (
	topicSuffix = regexp.MustCompile(`(?i)\s*-\s*topic$`)
	vevoSuffix  = regexp.MustCompile(`(?i)(?:\s*-\s*|\s+)?vevo$`)
)

// CleanChannel is an artist name without what YouTube's channel names add: " - Topic" and
// a "VEVO" suffix ("ArtistVEVO", "Artist - VEVO").
func CleanChannel(s string) string {
	s = strings.TrimSpace(s)
	s = topicSuffix.ReplaceAllString(s, "")
	if t := strings.TrimSpace(vevoSuffix.ReplaceAllString(s, "")); t != "" && len(t) < len(s) && len(t) >= 2 {
		s = t
	}
	return strings.TrimSpace(s)
}

// FoldAccents removes the accents from Latin letters ("é" -> "e", "ö" -> "o") and leaves
// every other script alone: the voicing mark of Japanese "が" is part of the letter, not
// an accent. The result is NFC.
func FoldAccents(s string) string {
	out := make([]rune, 0, len(s))
	latin := false
	for _, r := range textnorm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) && latin {
			continue
		}
		latin = unicode.Is(unicode.Latin, r)
		out = append(out, r)
	}
	return textnorm.NFC.String(string(out))
}

// ArtistTokens folds an artist name for comparison between services: accents and case
// folded, "&" and "+" read as "and", stylised letters read as letters ("P!nk", "Ke$ha"),
// punctuation dropped, a leading "The" dropped, YouTube's " - Topic" / VEVO suffixes
// removed. "Tyler, The Creator" and "Tyler The Creator" give the same tokens; so do
// "Beyoncé" and "BEYONCE".
func ArtistTokens(s string) []string {
	s = CleanChannel(s)
	s = textnorm.NFKC.String(s)
	// stylised letters between letters
	rs := []rune(s)
	for i := 1; i+1 < len(rs); i++ {
		if !unicode.IsLetter(rs[i-1]) || !unicode.IsLetter(rs[i+1]) {
			continue
		}
		switch rs[i] {
		case '$':
			rs[i] = 's'
		case '!':
			rs[i] = 'i'
		}
	}
	s = FoldAccents(strings.ToLower(string(rs)))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&' || r == '+':
			b.WriteString(" and ")
		case r == '\'' || r == '’' || r == '`':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	toks := strings.Fields(b.String())
	if len(toks) > 1 && toks[0] == "the" {
		toks = toks[1:]
	}
	return toks
}

// SameArtist reports whether two artist credits name the same act: equal tokens, or one
// is a run of two or more whole words inside the other (one word is never enough: "Pink"
// is not "Pink Floyd"). Each side may be a joined credit ("A & B", "A feat. B"); any one
// name of one credit equal to any one name of the other counts.
func SameArtist(a, b string) bool {
	as, bs := artistVariants(a), artistVariants(b)
	for _, x := range as {
		for _, y := range bs {
			if tokensRelated(x, y) {
				return true
			}
		}
	}
	return false
}

// artistVariants is a credit whole and split into its names, as token lists.
func artistVariants(s string) [][]string {
	var out [][]string
	if t := ArtistTokens(s); len(t) > 0 {
		out = append(out, t)
	}
	if parts := SplitArtists(strings.ReplaceAll(s, " + ", " & ")); len(parts) > 1 {
		for _, p := range parts {
			if t := ArtistTokens(p); len(t) > 0 {
				out = append(out, t)
			}
		}
	}
	return out
}

// tokensRelated: equal, or the shorter (two words or more) is a contiguous whole-word run
// of the longer.
func tokensRelated(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if strings.Join(a, " ") == strings.Join(b, " ") {
		return true
	}
	if strings.Join(a, "") == strings.Join(b, "") {
		return true // "Run DMC" / "RunDMC"
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) == 1 {
		return false // "Pink" inside "Pink Floyd", "Eve" inside "Steve Miller Eve": a different act
	}
	for i := 0; i+len(a) <= len(b); i++ {
		match := true
		for j := range a {
			if b[i+j] != a[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
