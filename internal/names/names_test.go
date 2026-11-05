package names

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPart(t *testing.T) {
	cases := map[string]string{
		"AC/DC":           "AC_DC",
		"What?":           "What_",
		"  spaced   out ": "spaced out",
		"trailing dot.":   "trailing dot",
		"CON":             "_CON",
		"nul.txt":         "_nul.txt",
		"":                "fallback",
		"...":             "fallback",
		"a‮b\u0085c":      "abc",
	}
	for in, want := range cases {
		if got := Part(in, "fallback"); got != want {
			t.Errorf("Part(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartByteLimit(t *testing.T) {
	suffix := " [dQw4w9WgXcQ].flac"
	for _, title := range []string{
		strings.Repeat("a", 500),
		strings.Repeat("日", 99),  // 297 bytes, under the rune limit
		strings.Repeat("😀", 100), // 400 bytes
	} {
		got := PartWithSuffix(title, "x", suffix)
		if len(got) > 255 {
			t.Errorf("%d bytes > 255", len(got))
		}
		if !strings.HasSuffix(got, suffix) || !utf8.ValidString(got) {
			t.Errorf("bad component %q", got)
		}
	}
	if got := Part(strings.Repeat("日", 99), "x"); len(got) > 255 || !utf8.ValidString(got) {
		t.Errorf("Part not capped: %d", len(got))
	}
}

func TestStripControl(t *testing.T) {
	cases := map[string]string{
		"plain":                      "plain",
		"a\tb\nc":                    "a\tb\nc",
		"\x1b[31mred\x1b[0m":         "red",
		"\x1b]0;evil title\x07after": "after",
		"\x1b]8;;http://x\x1b\\link": "link",
		"\x1b[2J\x1b[Hgone":          "gone",
		"x\x00y\rz\x7f":              "xyz",
		"c1\u009b31mq\u0085":         "c131mq",
		"‮evil‬ ⁦x⁩":                 "evil x",
		"line sep":                   "linesep",
	}
	for in, want := range cases {
		if got := StripControl(in); got != want {
			t.Errorf("StripControl(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImageDecoderOf(t *testing.T) {
	cases := map[string]string{
		"\xff\xd8\xff\xe0abcdefgh":    "mjpeg",
		"\x89PNG\r\n\x1a\nabcd":       "png",
		"RIFF\x00\x00\x00\x00WEBPVP8": "webp",
		"GIF89a......":                "",
		"#EXTM3U\n":                   "",
		"RIFF\x00\x00\x00\x00WAVEfmt": "",
		"":                            "",
	}
	for in, want := range cases {
		if got := imageDecoderOf([]byte(in)); got != want {
			t.Errorf("imageDecoderOf(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFFPath(t *testing.T) {
	if got := FFPath("-evil.mp3"); !strings.HasPrefix(got, "file:") || !filepath.IsAbs(strings.TrimPrefix(got, "file:")) {
		t.Errorf("FFPath = %q", got)
	}
}

func TestRedact(t *testing.T) {
	got := Clean("proxy socks5h://user:pa:ss@1.2.3.4:1080 failed \x1b[31m")
	if strings.Contains(got, "user") || strings.Contains(got, "pa:ss") || strings.Contains(got, "\x1b") {
		t.Errorf("leak: %q", got)
	}
}

func TestTrackPrefixAndParse(t *testing.T) {
	for _, c := range []struct {
		no, total int
		want      string
	}{{0, 0, ""}, {3, 0, "03 - "}, {3, 12, "03 - "}, {12, 12, "12 - "}, {7, 120, "007 - "}, {101, 0, "101 - "}} {
		if got := TrackPrefix(c.no, c.total); got != c.want {
			t.Errorf("TrackPrefix(%d,%d) = %q, want %q", c.no, c.total, got, c.want)
		}
	}
	for in, want := range map[string][2]int{"7": {7, 0}, "7/12": {7, 12}, " 3 / 9 ": {3, 9}, "": {0, 0}, "x/y": {0, 0}, "-2": {0, 0}} {
		if no, total := ParseTrack(in); no != want[0] || total != want[1] {
			t.Errorf("ParseTrack(%q) = %d,%d", in, no, total)
		}
	}
	if got := PartBetween("01 - ", strings.Repeat("é", 300), "x", ".opus"); len(got) > 255 || !strings.HasPrefix(got, "01 - ") || !strings.HasSuffix(got, ".opus") {
		t.Errorf("PartBetween over limit or lost its ends: %d bytes", len(got))
	}
}
