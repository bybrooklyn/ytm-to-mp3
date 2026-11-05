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
	// ytm's own spinner is coloured; the caller's text must arrive clean
	row := strings.Join(u.block, "")
	if !strings.HasSuffix(row, CReset+" x yz") || strings.Contains(row, "\x1b[31m") || strings.ContainsAny(row, "\x07\n") {
		t.Errorf("status not clean: %q", u.block)
	}
}

// countingWriter records each Write separately, to check frames go out in one piece.
type countingWriter struct{ writes []string }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes = append(c.writes, string(p))
	return len(p), nil
}

func TestBlockIsOneWriteWithNoEraseFirst(t *testing.T) {
	w := &countingWriter{}
	u := &UI{out: w, tty: true}
	u.Block([]string{"a", "b", "c"})
	u.Block([]string{"a", "b2"})
	if len(w.writes) != 2 {
		t.Fatalf("want one write per frame, got %d", len(w.writes))
	}
	f := w.writes[1]
	// back to the top of the 3-row block, rows overwritten in place, leftovers wiped last
	if !strings.Contains(f, "\x1b[2A") {
		t.Errorf("should move up 2 rows: %q", f)
	}
	if i, j := strings.Index(f, eraseBelow), strings.Index(f, "b2"); i < j {
		t.Errorf("erase-below must come after the rows: %q", f)
	}
}

func TestIdenticalFrameIsSkipped(t *testing.T) {
	w := &countingWriter{}
	u := &UI{out: w, tty: true}
	u.Block([]string{"same"})
	u.Block([]string{"same"})
	if len(w.writes) != 1 {
		t.Fatalf("identical frame should not be rewritten, got %d writes", len(w.writes))
	}
}

func TestLineAndBlockShareOneWrite(t *testing.T) {
	w := &countingWriter{}
	u := &UI{out: w, tty: true}
	u.Block([]string{"status"})
	u.Line("✓", "song")
	if len(w.writes) != 2 || !strings.Contains(w.writes[1], "✓ song") || !strings.Contains(w.writes[1], "status") {
		t.Fatalf("line and block should go out together: %q", w.writes)
	}
}

func TestClearStatusRestoresCursor(t *testing.T) {
	w := &countingWriter{}
	u := &UI{out: w, tty: true}
	u.Block([]string{"x"})
	if !strings.Contains(w.writes[0], hideCursor) {
		t.Fatal("cursor should be hidden while the block is up")
	}
	u.ClearStatus()
	if !strings.HasSuffix(strings.Join(w.writes, ""), showCursor) {
		t.Fatalf("cursor not restored: %q", w.writes)
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
