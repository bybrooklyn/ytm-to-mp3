// Package ui prints progress: event lines scroll, one status line stays pinned.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

type UI struct {
	mu     sync.Mutex
	out    io.Writer
	tty    bool
	block  []string // the live block pinned under the scrolling output (redrawn in place)
	widths []int    // visible width of each block row as last drawn (for resize maths)
	last   string   // the last frame written, to skip identical redraws
	hidden bool     // the cursor is hidden while the block is up
	in     *bufio.Reader
}

// Terminal control sequences used to draw the live block.
const (
	syncBegin  = "\x1b[?2026h" // synchronized update: terminals that know it show the frame at once
	syncEnd    = "\x1b[?2026l"
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	eraseLine  = "\x1b[K" // to the end of the line
	eraseBelow = "\x1b[J" // to the end of the screen
)

// Width is the terminal width in columns (80 when unknown).
func (u *UI) Width() int {
	if f, ok := u.out.(*os.File); ok {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// Height is the terminal height in rows (24 when unknown).
func (u *UI) Height() int {
	if f, ok := u.out.(*os.File); ok {
		if _, h, err := term.GetSize(f.Fd()); err == nil && h > 0 {
			return h
		}
	}
	return 24
}

func New() *UI {
	st, err := os.Stdout.Stat()
	tty := err == nil && st.Mode()&os.ModeCharDevice != 0
	return &UI{out: os.Stdout, tty: tty, in: bufio.NewReader(os.Stdin)}
}

// Interactive reports whether both ends are a terminal, so prompting makes sense.
func (u *UI) Interactive() bool {
	st, err := os.Stdin.Stat()
	return u.tty && err == nil && st.Mode()&os.ModeCharDevice != 0
}

// drawnRows is how many terminal rows the block occupies now, at width w. Rows were cut
// to fit when drawn, but a terminal that has since got narrower re-wraps them.
func (u *UI) drawnRows(w int) int {
	n := 0
	for _, rw := range u.widths {
		n += max(1, (rw+w-1)/max(1, w))
	}
	return n
}

// frame writes, as one write, everything needed to: go back to the top of the block, print
// the scrolling lines (if any) over it, then the block itself, wiping only what's left over.
// Nothing is erased ahead of being overwritten, so the terminal never shows a blank block.
func (u *UI) frame(lines []string) {
	if !u.tty {
		for _, l := range lines {
			fmt.Fprintln(u.out, l)
		}
		return
	}
	w := u.Width()
	var b strings.Builder
	b.WriteString(syncBegin)
	if len(u.block) > 0 {
		b.WriteString(hideCursor)
		u.hidden = true
	}
	b.WriteString("\r")
	if n := u.drawnRows(w); n > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", n-1)
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString(eraseLine)
		b.WriteString("\n")
	}
	u.widths = u.widths[:0]
	for i, l := range u.block {
		row := ansi.Truncate(l, w-1, "…") // never wrap: wrapping breaks the arithmetic above
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(row)
		b.WriteString(eraseLine)
		u.widths = append(u.widths, ansi.StringWidth(row))
	}
	b.WriteString(eraseBelow) // rows left over from a taller block
	if len(u.block) == 0 && u.hidden {
		b.WriteString(showCursor)
		u.hidden = false
	}
	b.WriteString(syncEnd)
	s := b.String()
	if len(lines) == 0 && s == u.last {
		return // nothing moved
	}
	u.last = s
	io.WriteString(u.out, s)
}

// Printf prints a line above the status line.
func (u *UI) Printf(format string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	msg := Sanitize(fmt.Sprintf(format, a...))
	msg = strings.TrimSuffix(msg, "\n")
	u.frame(strings.Split(msg, "\n"))
}

// Line prints "prefix text" above the live block. The prefix is the caller's own (it may
// carry colour codes, dropped when not on a terminal); the text is untrusted and sanitized.
func (u *UI) Line(prefix, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty {
		prefix = ansi.Strip(prefix)
	}
	u.frame([]string{prefix + " " + strings.ReplaceAll(Sanitize(text), "\n", " ")})
}

// Styled prints a line of the caller's own making above the live block, colours and all
// (dropped when not on a terminal). Any untrusted text in it must go through Sanitize first.
func (u *UI) Styled(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty {
		s = ansi.Strip(s)
	}
	u.frame(strings.Split(strings.TrimSuffix(s, "\n"), "\n"))
}

// LineNote is Line with a dim note after the text ("✓ Artist - Song  · Album"). The note is
// untrusted too and sanitized; "" leaves it out.
func (u *UI) LineNote(prefix, text, note string) {
	note = strings.ReplaceAll(Sanitize(note), "\n", " ")
	if note == "" {
		u.Line(prefix, text)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	sep := Dim("  · " + note)
	if !u.tty {
		prefix, sep = ansi.Strip(prefix), "  · "+note
	}
	u.frame([]string{prefix + " " + strings.ReplaceAll(Sanitize(text), "\n", " ") + sep})
}

// Notice prints a message above the live block; safe to call from any goroutine (e.g. a
// signal handler), unlike writing to the terminal directly.
func (u *UI) Notice(msg string) { u.Printf("%s", msg) }

// Sanitize strips escape sequences and control / bidi characters (except newline and
// tab) so untrusted text (titles, tags, tool output) can't drive the terminal.
func Sanitize(s string) string { return names.StripControl(s) }

// Status replaces the pinned status line (ignored when not on a terminal).
func (u *UI) Status(format string, a ...any) {
	if !u.tty {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.block = []string{strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(fmt.Sprintf(format, a...)))}
	u.frame(nil)
}

// Block replaces the live block with several rows. Rows may carry the caller's own
// colour codes; any untrusted text in them (titles, errors) must go through Sanitize first.
// Ignored when not on a terminal.
func (u *UI) Block(rows []string) {
	if !u.tty {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.block = rows
	u.frame(nil)
}

// erase wipes the block from the screen (it stays remembered) and shows the cursor again.
// The caller holds u.mu.
func (u *UI) erase() {
	if !u.tty || (len(u.widths) == 0 && !u.hidden) {
		return
	}
	var b strings.Builder
	b.WriteString("\r")
	if n := u.drawnRows(u.Width()); n > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", n-1)
	}
	b.WriteString(eraseBelow)
	b.WriteString(showCursor)
	io.WriteString(u.out, b.String())
	u.widths, u.last, u.hidden = nil, "", false
}

// RestoreCursor shows the cursor again without taking the lock, for a hard exit that may
// happen while another goroutine is mid-draw.
func (u *UI) RestoreCursor() {
	if u.tty {
		io.WriteString(u.out, "\n"+showCursor)
	}
}

// ClearStatus removes the live block for good.
func (u *UI) ClearStatus() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.erase()
	u.block = nil
}

// Ask prompts for a line of input; empty input returns def.
func (u *UI) Ask(prompt, def string) string {
	u.mu.Lock()
	u.erase()
	fmt.Fprint(u.out, Sanitize(prompt))
	u.mu.Unlock()
	line, _ := u.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// Confirm asks a yes/no question. def is returned on empty input or when not interactive.
func (u *UI) Confirm(prompt string, def bool) bool {
	if !u.Interactive() {
		return def
	}
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	ans := strings.ToLower(u.Ask(prompt+" "+hint+" ", ""))
	switch {
	case ans == "":
		return def
	case strings.HasPrefix(ans, "y"):
		return true
	default:
		return false
	}
}
