package ui

import (
	"os"
	"strings"
)

// ytm's one palette: plain ANSI colours, so it follows the terminal's own theme. Cyan is
// the accent (what's selected, what's moving), green is done or on, yellow is a warning or
// something waiting, red is a failure, dim is detail. NO_COLOR (https://no-color.org)
// turns all of it off.
var (
	CDim    = sgr("2")
	CBold   = sgr("1")
	CAccent = sgr("1;36")
	CCyan   = sgr("36")
	CGreen  = sgr("32")
	CYellow = sgr("33")
	CRed    = sgr("31")
	CReset  = sgr("0")
)

func sgr(code string) string {
	if _, off := os.LookupEnv("NO_COLOR"); off {
		return ""
	}
	return "\x1b[" + code + "m"
}

func paint(c, s string) string {
	if c == "" {
		return s
	}
	return c + s + CReset
}

// Dim, Bold, Accent, Green, Yellow, Red wrap s in that style.
func Dim(s string) string    { return paint(CDim, s) }
func Bold(s string) string   { return paint(CBold, s) }
func Accent(s string) string { return paint(CAccent, s) }
func Cyan(s string) string   { return paint(CCyan, s) }
func Green(s string) string  { return paint(CGreen, s) }
func Yellow(s string) string { return paint(CYellow, s) }
func Red(s string) string    { return paint(CRed, s) }

// OK, Fail and Warn are the marks in front of a line: "✓", "✗", "!" and the like.
func OK(mark string) string   { return Green(mark) }
func Fail(mark string) string { return Red(mark) }
func Warn(mark string) string { return Yellow(mark) }

// Bar is a progress bar of width cells for f in [0,1]: the done part in color (cyan when
// "" is given, green once it's full), the rest as a dim track.
func Bar(f float64, width int, color string) string {
	f = min(max(f, 0), 1)
	if color == "" {
		color = CCyan
		if f >= 1 {
			color = CGreen
		}
	}
	full := min(int(f*float64(width)), width)
	return paint(color, strings.Repeat("█", full)) + Dim(strings.Repeat("░", width-full))
}

// Badge is a song's source as a short tag in a fixed width of 5: "yt", "ytm" (found on
// YouTube Music), "apple".
func Badge(src string) string {
	pad := strings.Repeat(" ", max(0, 5-len(src)))
	switch src {
	case "apple":
		return Accent(src) + pad
	case "ytm":
		return Yellow(src) + pad
	}
	return Dim(src) + pad
}
