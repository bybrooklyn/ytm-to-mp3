// Package ui prints progress: event lines scroll, one status line stays pinned.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/bybrooklyn/mfetchy/internal/names"
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
	secret func() (string, error) // reads a line without echoing it (nil: the real terminal)

	// Lanes let several things draw their own live rows at once (a run with Tidal, Apple
	// Music and YouTube downloading side by side). A lane is a view of the UI made by Lane:
	// it prints through the same terminal, and its Status/Block/ClearStatus set only its
	// own rows, which the base UI stacks in the order the lanes first drew. With one lane
	// the output is exactly what it always was.
	parent    *UI
	lane      string
	laneOrder []string
	laneRows  map[string][]string
}

// Lane returns a view of u whose live rows are its own. Every other method behaves as on u.
func (u *UI) Lane(name string) *UI {
	b := u.base()
	return &UI{parent: b, lane: "lane:" + name}
}

func (u *UI) base() *UI {
	if u.parent != nil {
		return u.parent
	}
	return u
}

// setLane sets a lane's rows (nil removes it) and redraws the stacked block.
func (u *UI) setLane(lane string, rows []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.laneRows == nil {
		u.laneRows = map[string][]string{}
	}
	if rows == nil {
		delete(u.laneRows, lane)
		u.laneOrder = slices.DeleteFunc(u.laneOrder, func(s string) bool { return s == lane })
	} else {
		if _, ok := u.laneRows[lane]; !ok {
			u.laneOrder = append(u.laneOrder, lane)
		}
		u.laneRows[lane] = rows
	}
	var block []string
	for _, l := range u.laneOrder {
		block = append(block, u.laneRows[l]...)
	}
	if len(block) == 0 {
		u.erase()
		u.block = nil
		return
	}
	u.block = block
	u.frame(nil)
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
	u = u.base()
	if f, ok := u.out.(*os.File); ok {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// Height is the terminal height in rows (24 when unknown).
func (u *UI) Height() int {
	u = u.base()
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

// NewWriter is a UI that prints plain lines to w and is never interactive: no live block,
// no prompts (a question returns its default). A window uses it to collect a run's text.
func NewWriter(w io.Writer) *UI {
	return &UI{out: w, in: bufio.NewReader(strings.NewReader(""))}
}

// NewTerminal is a UI that talks to in and out as if they were a terminal, with secret
// standing in for the no-echo read. Tests use it to drive the question paths.
func NewTerminal(in io.Reader, out io.Writer, secret func() (string, error)) *UI {
	return &UI{out: out, tty: true, in: bufio.NewReader(in), secret: secret}
}

// Interactive reports whether both ends are a terminal, so prompting makes sense.
func (u *UI) Interactive() bool {
	u = u.base()
	if u.secret != nil { // made by NewTerminal for a test: there is a person on the other end
		return true
	}
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
	u = u.base()
	u.mu.Lock()
	defer u.mu.Unlock()
	msg := Sanitize(fmt.Sprintf(format, a...))
	msg = strings.TrimSuffix(msg, "\n")
	u.frame(strings.Split(msg, "\n"))
}

// Line prints "prefix text" above the live block. The prefix is the caller's own (it may
// carry colour codes, dropped when not on a terminal); the text is untrusted and sanitized.
func (u *UI) Line(prefix, text string) {
	u = u.base()
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty {
		prefix = ansi.Strip(prefix)
	}
	u.frame([]string{"  " + prefix + " " + strings.ReplaceAll(Sanitize(text), "\n", " ")})
}

// Styled prints a line of the caller's own making above the live block, colours and all
// (dropped when not on a terminal). Any untrusted text in it must go through Sanitize first.
func (u *UI) Styled(s string) {
	u = u.base()
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
	u = u.base()
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
	u.frame([]string{"  " + prefix + " " + strings.ReplaceAll(Sanitize(text), "\n", " ") + sep})
}

// Notice prints a message above the live block; safe to call from any goroutine (e.g. a
// signal handler), unlike writing to the terminal directly.
func (u *UI) Notice(msg string) { u.Warn("%s", msg) }

// Sanitize strips escape sequences and control / bidi characters (except newline and
// tab) so untrusted text (titles, tags, tool output) can't drive the terminal.
func Sanitize(s string) string { return names.StripControl(s) }

// Status replaces the pinned status line (ignored when not on a terminal). It is shown as
// "  ⠹ text" with a cyan spinner that turns each time it is updated, and the part after
// " · " dimmed, like mfetchy's other lines.
func (u *UI) Status(format string, a ...any) {
	b := u.base()
	if !b.tty {
		return
	}
	text := strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(fmt.Sprintf(format, a...)))
	frame := spinFrames[int(time.Now().UnixMilli()/100)%len(spinFrames)]
	b.setLane(u.lane, []string{Mark(Accent(frame), text)})
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Block replaces the live block with several rows. Rows may carry the caller's own
// colour codes; any untrusted text in them (titles, errors) must go through Sanitize first.
// Ignored when not on a terminal.
func (u *UI) Block(rows []string) {
	b := u.base()
	if !b.tty {
		return
	}
	if rows == nil {
		rows = []string{}
	}
	b.setLane(u.lane, rows)
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
	u = u.base()
	if u.tty {
		io.WriteString(u.out, "\n"+showCursor)
	}
}

// ClearStatus removes the live block for good.
func (u *UI) ClearStatus() {
	u.base().setLane(u.lane, nil)
}

// Ask prompts for a line of input; empty input returns def.
func (u *UI) Ask(prompt, def string) string {
	u = u.base()
	u.mu.Lock()
	u.erase()
	p := Sanitize(prompt)
	if u.tty { // "  ? question [Y/n] " with the question marked like mfetchy's other lines
		p = Mark(Accent("?"), strings.ReplaceAll(p, "\n", "\n    "))
		p = strings.NewReplacer("[Y/n]", Dim("[Y/n]"), "[y/N]", Dim("[y/N]")).Replace(p)
	}
	fmt.Fprint(u.out, p)
	u.mu.Unlock()
	line, _ := u.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// AskSecret prompts for a line that must not be shown as it is typed (a password, a token,
// an account number): the terminal does not echo it. Empty input returns "". Without a
// terminal it reads a plain line, so a script can pipe one in.
func (u *UI) AskSecret(prompt string) string {
	u = u.base()
	u.mu.Lock()
	u.erase()
	p := Sanitize(prompt)
	if u.tty {
		p = Mark(Accent("?"), strings.ReplaceAll(p, "\n", "\n    "))
	}
	fmt.Fprint(u.out, p)
	u.mu.Unlock()
	if u.secret != nil {
		s, _ := u.secret()
		fmt.Fprintln(u.out)
		return strings.TrimSpace(s)
	}
	if u.tty && term.IsTerminal(os.Stdin.Fd()) {
		b, _ := term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(u.out)
		return strings.TrimSpace(string(b))
	}
	line, _ := u.in.ReadString('\n')
	return strings.TrimSpace(line)
}

// Confirm asks a yes/no question. def is returned on empty input or when not interactive.
func (u *UI) Confirm(prompt string, def bool) bool {
	u = u.base()
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
