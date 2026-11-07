package lyrics

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

type tl struct {
	ms   int
	text string
}

func flat(l []Line) []tl {
	var out []tl
	for _, x := range l {
		out = append(out, tl{int(x.At / time.Millisecond), x.Text})
	}
	return out
}

func TestParseLRC(t *testing.T) {
	cases := []struct {
		name, in string
		want     []tl
	}{
		{"two fractional digits", "[00:01.50] hello", []tl{{1500, "hello"}}},
		{"three fractional digits", "[01:02.250]x", []tl{{62250, "x"}}},
		{"one fractional digit is tenths", "[00:03.5]x", []tl{{3500, "x"}}},
		{"two digits .05 is 50 ms", "[00:03.05]x", []tl{{3050, "x"}}},
		{"no fraction", "[1:02]x", []tl{{62000, "x"}}},
		{"colon hundredths", "[00:01:50]x", []tl{{1500, "x"}}},
		{"over 99 minutes", "[100:00.00]long", []tl{{6000000, "long"}}},
		{"several stamps on a chorus line", "[00:03.00][01:00.25]La la la", []tl{{3000, "La la la"}, {60250, "La la la"}}},
		{"pause line", "[00:05.00]", []tl{{5000, ""}}},
		{"out of order is sorted", "[00:10.00]b\n[00:02.00]a", []tl{{2000, "a"}, {10000, "b"}}},
		{"duplicate stamp and text collapses", "[00:01.00]same\n[00:01.00]same", []tl{{1000, "same"}}},
		{"same stamp different text keeps file order (translation)", "[00:01.00]hola\n[00:01.00]hello", []tl{{1000, "hola"}, {1000, "hello"}}},
		{"metadata is not a line", "[ar:Someone]\n[ti:Song]\n[by:me]\n[00:01.00]a", []tl{{1000, "a"}}},
		{"CRLF and BOM", "\xef\xbb\xbf[00:01.00]a\r\n[00:02.00]b\r\n", []tl{{1000, "a"}, {2000, "b"}}},
		{"word stamps are stripped from the text", "[00:01.00]<00:01.00>he<00:01.40>llo <00:01.80>world", []tl{{1000, "hello world"}}},
		{"angle brackets that are not stamps stay", "[00:01.00]a < b and b > c", []tl{{1000, "a < b and b > c"}}},
		{"positive offset makes lines earlier", "[offset:+500]\n[00:01.00]a\n[00:00.20]b", []tl{{0, "b"}, {500, "a"}}},
		{"negative offset makes lines later", "[offset:-250]\n[00:01.00]a", []tl{{1250, "a"}}},
		{"no stamps no lines", "just words\nno stamps\n[broken", nil},
		{"bad stamps ignored", "[00:xx.00]bad\n[::]bad\n[00:1x]bad", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := ParseLRC(c.in)
			if !reflect.DeepEqual(flat(got), c.want) {
				t.Fatalf("got %v want %v", flat(got), c.want)
			}
		})
	}
}

func TestParseHeaderAndWords(t *testing.T) {
	lines, h := ParseLRC("[ti:Bloom]\n[ar:Radiohead]\n[al:The King of Limbs]\n[length:03:35]\n[by:someone]\n[00:12.00]<00:12.00>Open <00:12.40>your <00:12.90>mouth")
	if h.Title != "Bloom" || h.Artist != "Radiohead" || h.Album != "The King of Limbs" || h.By != "someone" || h.Length != 215*time.Second {
		t.Fatalf("header %+v", h)
	}
	if len(lines) != 1 || lines[0].Text != "Open your mouth" || len(lines[0].Words) != 3 || lines[0].Words[1].At != 12400*time.Millisecond || lines[0].Words[1].Text != "your" {
		t.Fatalf("%+v", lines)
	}
}

func TestFormatLRC(t *testing.T) {
	lines, _ := ParseLRC("[00:01.5]Hello\n[00:03.004]\n[01:00.999]Bye")
	got := FormatLRC(Header{Title: "Hi", Artist: "A", Album: "Al", Length: 61 * time.Second, By: "ytm · LRCLIB"}, lines)
	want := "[ti:Hi]\n[ar:A]\n[al:Al]\n[length:01:01]\n[by:ytm · LRCLIB]\n[00:01.50]Hello\n[00:03.00]\n[01:01.00]Bye\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	// the output parses back to the same lines
	back, h := ParseLRC(got)
	if len(back) != 3 || back[2].At != 61*time.Second || h.By != "ytm · LRCLIB" {
		t.Fatalf("%+v %+v", back, h)
	}
	if s := FormatLRC(Header{}, nil); s != "" {
		t.Fatalf("empty = %q", s)
	}
}

func TestPlain(t *testing.T) {
	lines, _ := ParseLRC("[00:01.00]one\n[00:02.00]\n[00:03.00]\n[00:04.00]two\n[00:05.00]")
	if got := Plain(lines); got != "one\n\ntwo" {
		t.Fatalf("%q", got)
	}
	if SyncedLines(lines) != 2 || PlainLines("a\n\n b \n") != 2 {
		t.Fatal("counts")
	}
	if !strings.HasSuffix(FormatLRC(Header{}, lines), "[00:05.00]\n") {
		t.Fatal("trailing pause kept")
	}
}
