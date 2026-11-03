package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintfSanitizes(t *testing.T) {
	var buf bytes.Buffer
	u := &UI{out: &buf}
	u.Printf("✓ %s", "Song\x1b]0;pwn\x07\x1b[2J‮evil\u0085")
	got := buf.String()
	if got != "✓ Songevil\n" {
		t.Errorf("got %q", got)
	}
}

func TestStatusSanitizes(t *testing.T) {
	var buf bytes.Buffer
	u := &UI{out: &buf, tty: true}
	u.Status("x\ny\x1b[31m%s", "z\x07")
	if strings.ContainsAny(strings.Join(u.block, ""), "\x1b\x07\n") {
		t.Errorf("status not clean: %q", u.block)
	}
}

func TestSanitizeTable(t *testing.T) {
	for in, want := range map[string]string{
		"ok\ttab\nnl": "ok\ttab\nnl",
		"\x1b[0m\x00": "",
		"⁧x⁩":         "x",
	} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q)=%q want %q", in, got, want)
		}
	}
}
