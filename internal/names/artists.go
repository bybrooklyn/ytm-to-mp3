package names

import (
	"regexp"
	"strings"
)

// joinWordRe finds the words that join two artists' names ("A feat. B", "A x B").
var joinWordRe = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|with|x|vs\.?)\s+`)

// SplitArtists splits a credit like "A; B", "A, B", "A & B", "A feat. B", "A x B" or
// "A / B" into the artists' names, in order, without repeats and without YouTube's
// " - Topic" channel suffix. It is the one splitter ytm uses for every source (YouTube
// tags, Spotify's comma-joined credits, Apple's "A & B feat. C").
func SplitArtists(s string) []string {
	s = joinWordRe.ReplaceAllString(s, ";")
	s = strings.NewReplacer(" & ", ";", ", ", ";", " / ", ";", "/", ";").Replace(s)
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ";") {
		p = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p), " - Topic"))
		if k := strings.ToLower(p); p != "" && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}
