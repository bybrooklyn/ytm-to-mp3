package tag

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Only unambiguous YouTube upload noise. Things like "(Remastered)" or "(Live)"
	// carry real information and are never touched.
	noiseRe = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:official\s+(?:music\s+)?(?:video|audio|lyric\s+video|visuali[sz]er)|(?:music|lyric|lyrics|audio)\s+video|lyrics?|audio|visuali[sz]er|hd|hq|4k|full\s+(?:song|album))\s*[\)\]]`)
	topicRe = regexp.MustCompile(`(?i)\s+-\s+topic$`)
	spaceRe = regexp.MustCompile(`\s+`)
	featRe  = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:feat\.?|ft\.?|featuring)\s[^\)\]]*[\)\]]`)
)

// CleanTitle strips upload noise and a leading "Artist - " from a title.
func CleanTitle(title, artist string) string {
	t := noiseRe.ReplaceAllString(title, "")
	if artist != "" {
		if p := artist + " - "; len(t) > len(p) && strings.EqualFold(t[:len(p)], p) {
			t = t[len(p):]
		}
	}
	return strings.TrimSpace(spaceRe.ReplaceAllString(t, " "))
}

// CleanArtist removes the " - Topic" suffix YouTube adds to auto-generated channels.
func CleanArtist(a string) string {
	return strings.TrimSpace(topicRe.ReplaceAllString(a, ""))
}

// norm reduces a name to lowercase letters/digits so spelling variants compare equal.
func norm(s string) string {
	s = featRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(strings.ToLower(s), "&", "and")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitArtists splits "A; B", "A, B", "A & B", "A feat. B" into names.
func splitArtists(s string) []string {
	s = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|with|x)\s+`).ReplaceAllString(s, ";")
	s = strings.NewReplacer(" & ", ";", ", ", ";", "/", ";").Replace(s)
	var out []string
	for _, p := range strings.Split(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
