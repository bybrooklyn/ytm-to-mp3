// Package lyrics finds, ranks, caches and stores song lyrics. Lyrics are line-synced
// (LRC timestamps) whenever a provider has them; plain text is the fallback.
//
// Providers only ever receive a song's title, artist, album and length.
package lyrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Word is one word of an enhanced-LRC line (<mm:ss.xx>word). Providers rarely have these;
// line sync is the guarantee, words are kept only when a provider gives them.
type Word struct {
	At   time.Duration
	Text string
}

// Line is one timed line. An empty Text marks a pause (an instrumental break).
type Line struct {
	At    time.Duration
	Text  string
	Words []Word
}

// Header is the metadata an LRC file starts with.
type Header struct {
	Title, Artist, Album string
	Length               time.Duration
	By                   string // source credit, e.g. "mfetchy · LRCLIB"
}

// ParseLRC reads LRC text: [mm:ss.xx] stamps with 1 to 3 fractional digits (also [m:ss] and
// [mm:ss:xx]), several stamps on one line, [offset:±ms], enhanced <mm:ss.xx> word stamps,
// CRLF and a BOM. Metadata tags are returned in the Header. Lines come back sorted by time
// (equal times keep file order) with exact duplicates removed.
func ParseLRC(s string) ([]Line, Header) {
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	var h Header
	var offset time.Duration
	var out []Line
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		rest := strings.TrimSpace(raw)
		var stamps []time.Duration
		meta := false
		for strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				break
			}
			tag := rest[1:end]
			if d, ok := parseStamp(tag); ok {
				stamps = append(stamps, d)
				rest = rest[end+1:]
				continue
			}
			if k, v, ok := strings.Cut(tag, ":"); ok && len(stamps) == 0 {
				v = strings.TrimSpace(v)
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "offset":
					if ms, err := strconv.Atoi(strings.TrimPrefix(v, "+")); err == nil {
						offset = time.Duration(ms) * time.Millisecond
					}
				case "ti":
					h.Title = v
				case "ar":
					h.Artist = v
				case "al":
					h.Album = v
				case "by":
					h.By = v
				case "length":
					if d, ok := parseStamp(v); ok {
						h.Length = d
					}
				}
				meta = true
			}
			break
		}
		if meta || len(stamps) == 0 {
			continue
		}
		text, words := splitWords(rest)
		for _, at := range stamps {
			out = append(out, Line{At: at, Text: text, Words: words})
		}
	}
	if offset != 0 {
		for i := range out {
			// a positive offset makes the lyrics appear sooner
			out[i].At = max(0, out[i].At-offset)
			for j := range out[i].Words {
				out[i].Words[j].At = max(0, out[i].Words[j].At-offset)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	// drop exact duplicates (same time and text), keeping the first
	dedup := out[:0]
	for _, l := range out {
		dup := false
		for k := len(dedup) - 1; k >= 0 && dedup[k].At == l.At; k-- {
			if dedup[k].Text == l.Text {
				dup = true
				break
			}
		}
		if !dup {
			dedup = append(dedup, l)
		}
	}
	return dedup, h
}

// parseStamp reads "mm:ss", "mm:ss.f", "mm:ss.ff", "mm:ss.fff" or "mm:ss:ff".
func parseStamp(tag string) (time.Duration, bool) {
	m, rest, ok := strings.Cut(strings.TrimSpace(tag), ":")
	if !ok || m == "" || !allDigits(m) || len(m) > 5 {
		return 0, false
	}
	sec, frac, _ := strings.Cut(strings.Replace(rest, ":", ".", 1), ".")
	if sec == "" || !allDigits(sec) || len(sec) > 2 || (frac != "" && !allDigits(frac)) {
		return 0, false
	}
	mi, _ := strconv.Atoi(m)
	si, _ := strconv.Atoi(sec)
	ms := 0
	if frac != "" {
		f := (frac + "00")[:3] // ".5" is 500 ms, ".05" is 50 ms, ".057" is 57 ms
		ms, _ = strconv.Atoi(f)
	}
	return time.Duration(mi)*time.Minute + time.Duration(si)*time.Second + time.Duration(ms)*time.Millisecond, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// splitWords removes enhanced-LRC word stamps from s, returning the plain line text and
// the timed words (nil when the line has none).
func splitWords(s string) (string, []Word) {
	if !strings.Contains(s, "<") {
		return strings.TrimSpace(s), nil
	}
	var text strings.Builder
	var words []Word
	var cur strings.Builder
	var at time.Duration
	have := false
	flush := func() {
		if have && strings.TrimSpace(cur.String()) != "" {
			words = append(words, Word{At: at, Text: strings.TrimSpace(cur.String())})
		}
		cur.Reset()
	}
	for len(s) > 0 {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			text.WriteString(s)
			cur.WriteString(s)
			break
		}
		text.WriteString(s[:i])
		cur.WriteString(s[:i])
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			text.WriteString(s[i:])
			cur.WriteString(s[i:])
			break
		}
		if d, ok := parseStamp(s[i+1 : i+j]); ok {
			flush()
			at, have = d, true
		} else {
			text.WriteString(s[i : i+j+1])
			cur.WriteString(s[i : i+j+1])
		}
		s = s[i+j+1:]
	}
	flush()
	return strings.Join(strings.Fields(text.String()), " "), words
}

// FormatLRC writes clean LRC: headers, then one [mm:ss.xx]text line each. Pauses stay as
// empty lines. Word stamps are never written (line sync is what every player reads).
func FormatLRC(h Header, lines []Line) string {
	var b strings.Builder
	hdr := func(k, v string) {
		v = strings.Join(strings.Fields(v), " ")
		if v != "" {
			fmt.Fprintf(&b, "[%s:%s]\n", k, v)
		}
	}
	hdr("ti", h.Title)
	hdr("ar", h.Artist)
	hdr("al", h.Album)
	if h.Length > 0 {
		s := int(h.Length.Round(time.Second) / time.Second)
		fmt.Fprintf(&b, "[length:%02d:%02d]\n", s/60, s%60)
	}
	hdr("by", h.By)
	for _, l := range lines {
		fmt.Fprintf(&b, "%s%s\n", stamp(l.At), strings.TrimSpace(l.Text))
	}
	return b.String()
}

// WrittenByUs reports whether an LRC header's [by:] credit is ours: "mfetchy · LRCLIB",
// or "ytm · LRCLIB" from before the rename. A file with another credit is the user's.
func WrittenByUs(by string) bool {
	return strings.HasPrefix(by, "mfetchy · ") || strings.HasPrefix(by, "ytm · ")
}

func stamp(d time.Duration) string {
	cs := int((d + 5*time.Millisecond) / (10 * time.Millisecond)) // round to centiseconds
	return fmt.Sprintf("[%02d:%02d.%02d]", cs/6000, cs/100%60, cs%100)
}

// SyncedLines counts the lines with text.
func SyncedLines(l []Line) int {
	n := 0
	for _, x := range l {
		if strings.TrimSpace(x.Text) != "" {
			n++
		}
	}
	return n
}

// PlainLines counts the non-blank lines of plain text.
func PlainLines(s string) int {
	n := 0
	for _, x := range strings.Split(s, "\n") {
		if strings.TrimSpace(x) != "" {
			n++
		}
	}
	return n
}

// Plain turns synced lines into plain text (pauses become blank lines, runs of them one).
func Plain(lines []Line) string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l.Text)
		if t == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, t)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
