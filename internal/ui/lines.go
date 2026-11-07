package ui

import (
	"fmt"
	"strings"
)

// The kinds of line mfetchy prints, so every screen reads the same way. Each is indented two
// spaces under a one-character mark, with any detail dimmed after "·":
//
//	  ♪ Heading            a section or what's starting
//	  ✓ done               green: worked
//	  ✗ failed             red: didn't
//	  ! careful            yellow: worth knowing, nothing broke
//	  · by the way         dim mark: plain information
//	      a hint           dim: what to do about the line above
//	    key  value         a list row (dry-run plans, matches)
//
// Their text is untrusted and sanitized (format is the caller's own); colour is dropped
// when not on a terminal, by Styled.

// Mark builds one such line: "  " + mark + " " + text, with the part after the first
// " · " (if any) dimmed. text must already be sanitized.
func Mark(mark, text string) string {
	if head, detail, ok := strings.Cut(text, " · "); ok {
		text = head + Dim(" · "+detail)
	}
	return "  " + mark + " " + text
}

func line(format string, a []any) string {
	return strings.ReplaceAll(Sanitize(fmt.Sprintf(format, a...)), "\n", " ")
}

// Head prints a section heading, with a blank line above it.
func (u *UI) Head(format string, a ...any) {
	head, detail, ok := strings.Cut(line(format, a), " · ")
	text := Bold(head)
	if ok {
		text += Dim(" · " + detail)
	}
	u.Styled("\n  " + Accent("♪") + " " + text)
}

// Good, Bad, Warn and Note print a line with that mark.
func (u *UI) Good(format string, a ...any) { u.Styled(Mark(OK("✓"), line(format, a))) }
func (u *UI) Bad(format string, a ...any)  { u.Styled(Mark(Fail("✗"), line(format, a))) }
func (u *UI) Warn(format string, a ...any) { u.Styled(Mark(Yellow("!"), line(format, a))) }
func (u *UI) Note(format string, a ...any) { u.Styled(Mark(Dim("·"), line(format, a))) }

// Hint prints a dim line under the one before (what to do about it).
func (u *UI) Hint(format string, a ...any) { u.Styled("      " + Dim(line(format, a))) }

// Item prints a list row: a dim key column and the text.
func (u *UI) Item(key, format string, a ...any) {
	u.Styled("    " + Dim(Sanitize(key)) + "  " + line(format, a))
}

// Logf is Note as a log function, for the helpers that report as they go (fetching
// yt-dlp, connecting the VPN).
// Lines the helpers mark themselves ("  ✓ server", "  ✗ server: why") keep their meaning.
func (u *UI) Logf(format string, a ...any) {
	s := strings.TrimSpace(fmt.Sprintf(format, a...))
	switch {
	case strings.HasPrefix(s, "✓ "):
		u.Good("%s", strings.TrimPrefix(s, "✓ "))
	case strings.HasPrefix(s, "✗ "):
		u.Bad("%s", strings.TrimPrefix(s, "✗ "))
	case s != "":
		u.Note("%s", s)
	}
}
