// Package ui prints progress: event lines scroll, one status line stays pinned.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

type UI struct {
	mu     sync.Mutex
	out    io.Writer
	tty    bool
	status string
	in     *bufio.Reader
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

func (u *UI) clear() {
	if u.tty && u.status != "" {
		fmt.Fprint(u.out, "\r\033[K")
	}
}

func (u *UI) redraw() {
	if u.tty && u.status != "" {
		fmt.Fprint(u.out, u.status)
	}
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
	u.status = strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(fmt.Sprintf(format, a...)))
	u.redraw()
}

// ClearStatus removes the status line for good.
func (u *UI) ClearStatus() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	u.status = ""
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
