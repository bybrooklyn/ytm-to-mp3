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
	mu    sync.Mutex
	out   io.Writer
	tty   bool
	block []string // the live block pinned under the scrolling output (redrawn in place)
	drawn int      // how many terminal rows the block currently occupies
	in    *bufio.Reader
}

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

// clear erases the live block: back to its first row, then wipe to the end of the screen.
func (u *UI) clear() {
	if !u.tty || u.drawn == 0 {
		return
	}
	if u.drawn > 1 {
		fmt.Fprintf(u.out, "\033[%dA", u.drawn-1)
	}
	fmt.Fprint(u.out, "\r\033[J")
	u.drawn = 0
}

// redraw prints the live block, each row cut to the terminal width so it never wraps
// (a wrapped row would break the cursor arithmetic in clear).
func (u *UI) redraw() {
	if !u.tty || len(u.block) == 0 {
		return
	}
	w := u.Width()
	rows := make([]string, len(u.block))
	for i, l := range u.block {
		rows[i] = ansi.Truncate(l, w-1, "…")
	}
	fmt.Fprint(u.out, strings.Join(rows, "\n"))
	u.drawn = len(rows)
}

// Printf prints a line above the status line.
func (u *UI) Printf(format string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	fmt.Fprint(u.out, Sanitize(fmt.Sprintf(format, a...)))
	if !strings.HasSuffix(format, "\n") {
		fmt.Fprintln(u.out)
	}
	u.redraw()
}

// Line prints "prefix text" above the live block. The prefix is the caller's own (it may
// carry colour codes, dropped when not on a terminal); the text is untrusted and sanitized.
func (u *UI) Line(prefix, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	if !u.tty {
		prefix = ansi.Strip(prefix)
	}
	fmt.Fprintln(u.out, prefix+" "+Sanitize(text))
	u.redraw()
}

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
	u.clear()
	u.block = []string{strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(fmt.Sprintf(format, a...)))}
	u.redraw()
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
	u.clear()
	u.block = rows
	u.redraw()
}

// ClearStatus removes the live block for good.
func (u *UI) ClearStatus() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	u.block = nil
}

// Ask prompts for a line of input; empty input returns def.
func (u *UI) Ask(prompt, def string) string {
	u.mu.Lock()
	u.clear()
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
