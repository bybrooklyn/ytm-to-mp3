// Package names builds filesystem-safe path components that work on Windows,
// macOS and Linux alike, and strips terminal-hostile characters from text.
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
	// Full ANSI escape sequences: CSI, OSC (BEL or ST terminated) and two-byte escapes.
	ansiSeq = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]`)
)

var proxyCreds = regexp.MustCompile(`(?i)(socks[45]?h?|https?)://[^@\s]+@`)

// Redact removes proxy credentials (scheme://user:pass@host) from tool output.
func Redact(s string) string {
	return proxyCreds.ReplaceAllString(s, "${1}://***@")
}

// Clean makes untrusted tool output safe to show or store: no escapes, no secrets.
func Clean(s string) string { return Redact(StripControl(s)) }

// maxComponent is the longest path component (in bytes) common filesystems accept.
const maxComponent = 255

// unsafe reports runes that must never reach a terminal or a file name: C0/C1 controls,
// bidi overrides/isolates and line/paragraph separators.
func unsafe(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0x2028 || r == 0x2029 || r == 0x200E || r == 0x200F || r == 0x061C:
		return true
	}
	return false
}

// StripControl removes escape sequences and every control / bidi-control rune from s,
// keeping only newline and tab. Use it on anything untrusted before printing or storing.
func StripControl(s string) string {
	s = ansiSeq.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unsafe(r) {
			return -1
		}
		return r
	}, s)
}

// Part returns s as a single safe path component, or fallback if nothing is left.
func Part(s, fallback string) string { return PartWithSuffix(s, fallback, "") }

// PartWithSuffix is Part(s, fallback)+suffix where the whole component, suffix included,
// fits in 255 bytes: the title part is cut on a rune boundary, never the suffix.
func PartWithSuffix(s, fallback, suffix string) string {
	p := part(s, fallback, maxComponent-len(suffix))
	return p + suffix
}

func part(s, fallback string, maxBytes int) string {
	s = StripControl(s)
	s = illegal.ReplaceAllString(s, "_")
	s = spaces.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.Trim(s, " .")
	s = truncate(s, 100, maxBytes)
	s = strings.Trim(s, " .")
	if s == "" {
		if fallback == "" || fallback == "_" {
			return "_"
		}
		return part(fallback, "_", maxBytes)
	}
	if reserved.MatchString(s) {
		s = "_" + s
		s = truncate(s, 100, maxBytes)
	}
	return s
}

func truncate(s string, maxRunes, maxBytes int) string {
	if maxBytes < 1 {
		maxBytes = 1
	}
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimRightFunc(s, unicode.IsSpace)
}
