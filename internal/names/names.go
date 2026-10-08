// Package names builds filesystem-safe path components that work on Windows,
// macOS and Linux alike.
package names

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	illegal = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)
	spaces  = regexp.MustCompile(`\s+`)
	// Windows device names are invalid as file names, with or without extension.
	reserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)
)

// Part returns s as a single safe path component, or fallback if nothing is left.
func Part(s, fallback string) string {
	s = illegal.ReplaceAllString(s, "_")
	s = spaces.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.Trim(s, " .")
	s = truncate(s, 100)
	s = strings.Trim(s, " .")
	if s == "" {
		return Part(fallback, "_")
	}
	if reserved.MatchString(s) {
		s = "_" + s
	}
	return s
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max]
	return strings.TrimRightFunc(string(r), unicode.IsSpace)
}
