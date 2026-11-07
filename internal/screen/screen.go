package screen

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// Row is one line of a screen.
type Row struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Message  string `json:"message,omitempty"` // shown under the selected row (hint or problem)
	Selected bool   `json:"selected,omitempty"`
	Dim      bool   `json:"dim,omitempty"`     // not applicable right now
	Gap      bool   `json:"gap,omitempty"`     // blank line before: starts a group
	Stepper  bool   `json:"stepper,omitempty"` // ←/→ change it: shown as ‹ value ›
	Action   bool   `json:"action,omitempty"`  // enter runs it
	Editing  bool   `json:"editing,omitempty"` // the value is text being typed
	Good     bool   `json:"good,omitempty"`    // on / signed in: shown in green

	// Presentation hints for graphical hosts. The terminal ignores all of them, and a row that
	// sets none (Kind "") still draws correctly everywhere from Label and Value alone.
	Kind    string   `json:"kind,omitempty"`    // what control suits the row: one of the Kind* constants
	Options []string `json:"options,omitempty"` // KindChoice: every value, as it appears in Value
	On      bool     `json:"on,omitempty"`      // KindToggle: the switch is on
	Button  string   `json:"button,omitempty"`  // KindAction: the button's label
	Status  string   `json:"status,omitempty"`  // KindAction/KindInfo: shown instead of Value when set
	Help    string   `json:"help,omitempty"`    // always-visible explanation under the label
	Error   string   `json:"error,omitempty"`   // a problem with this row (also in Message)
	Group   string   `json:"group,omitempty"`   // starts a section with this heading
}

// Row kinds, for Row.Kind.
const (
	KindToggle = "toggle" // on/off: a switch (Row.On); Set Value "on" or "off"
	KindChoice = "choice" // one of Row.Options; Set Value to an option
	KindNumber = "number" // a number (Value, may read "Auto"); Set Step +1 or -1
	KindText   = "text"   // typed text such as a path; Set Value to the new text
	KindAction = "action" // a button (Row.Button) that asks the host to do something; Set Press
	KindInfo   = "info"   // a status with nothing to change
)

// Screen is what a screen shows: a title, rows and a footer of key hints. It carries no
// colours or layout, so a terminal and a window can each draw it their own way.
type Screen struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Rows     []Row  `json:"rows"`
	Footer   string `json:"footer,omitempty"`
	Saved    bool   `json:"saved,omitempty"`
}

// ANSI draws the screen for a terminal.
func (s Screen) ANSI() string {
	w := 0
	for _, r := range s.Rows {
		w = max(w, utf8.RuneCountInString(r.Label))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s%s%s", ui.Accent("♪"), cBold, s.Title, cReset)
	if s.Subtitle != "" {
		fmt.Fprintf(&b, "  %s%s%s", cDim, s.Subtitle, cReset)
	}
	b.WriteString("\n\n")
	for _, r := range s.Rows {
		if r.Gap {
			b.WriteString("\n")
		}
		marker, label := "  ", cDim+fmt.Sprintf("%-*s", w, r.Label)+cReset
		if r.Selected {
			marker, label = cAccent+"› "+cReset, cAccent+fmt.Sprintf("%-*s", w, r.Label)+cReset
		}
		val := r.Value
		switch {
		case r.Selected && r.Editing:
			val = r.Value + cAccent + "▏" + cReset
		case r.Action:
			if r.Selected {
				val = cAccent + val + cReset
			} else {
				val = cDim + val + cReset
			}
		case r.Dim:
			val = cDim + val + cReset
		case r.Selected && r.Stepper:
			val = cAccent + "‹ " + val + " ›" + cReset
		case r.Selected:
			val = cAccent + val + cReset
		case r.Good:
			val = cGreen + val + cReset
		}
		fmt.Fprintf(&b, "  %s%s  %s\n", marker, label, val)
		if r.Selected && r.Message != "" {
			fmt.Fprintf(&b, "      %s%s%s\n", cYellow, r.Message, cReset)
		}
	}
	b.WriteString("\n  " + cDim + s.Footer + cReset)
	if s.Saved {
		b.WriteString("   " + cGreen + "✓ saved" + cReset)
	}
	b.WriteString("\n")
	return b.String()
}

// Model is a screen's behaviour, drawn by whatever host shows it (a terminal or a window).
type Model interface {
	Key(name string)   // "up", "down", "left", "right", "enter", "esc", "tab", "space", "backspace", ...
	Type(text string)  // typed text, when a field is being edited
	Paste(text string) // pasted text
	SelectIndex(i int) // a click on row i
	Screen() Screen    // what to show now
	Editing() bool     // keys are text right now
	Finished() bool    // the screen wants to close, or has work for the host (see Action)
	Action() string    // work for the host to do ("signin", ...), "" if none
}

var _ Model = (*Settings)(nil)
