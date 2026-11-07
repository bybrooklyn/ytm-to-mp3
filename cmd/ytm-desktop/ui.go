//go:build cgo

package main

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/ytm-to-mp3/internal/screen"
)

// The terminal UI's palette: cyan accent, green for on, yellow for hints, dim for the rest.
var (
	cBG     = color.NRGBA{0x0e, 0x11, 0x14, 0xff}
	cFG     = color.NRGBA{0xd7, 0xda, 0xe0, 0xff}
	cDim    = color.NRGBA{0x6b, 0x73, 0x80, 0xff}
	cAccent = color.NRGBA{0x4d, 0xd0, 0xe1, 0xff}
	cGreen  = color.NRGBA{0x7e, 0xe7, 0x87, 0xff}
	cYellow = color.NRGBA{0xe3, 0xb3, 0x41, 0xff}
	cLine   = color.NRGBA{0x1b, 0x20, 0x26, 0xff}
)

const textSize = 15

// ytmTheme gives the window the terminal's colours.
type ytmTheme struct{ fyne.Theme }

func (t ytmTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return cBG
	case theme.ColorNameForeground:
		return cFG
	}
	return t.Theme.Color(n, theme.VariantDark)
}

// view draws a screen.Host in a window and sends what the user does back to it. All the
// behaviour is the Host's, shared with the terminal UI; this file only draws and listens.
type view struct {
	host *screen.Host
	win  fyne.Window
}

// newView fills win with the host's screen and wires up the keyboard and mouse.
func newView(win fyne.Window, host *screen.Host) *view {
	v := &view{host: host, win: win}
	c := win.Canvas()
	c.SetOnTypedKey(v.onKey)
	c.SetOnTypedRune(v.onRune)
	c.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyU, Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) {
		v.send(screen.Event{Key: "ctrl+u"})
	})
	c.AddShortcut(&fyne.ShortcutPaste{}, func(s fyne.Shortcut) {
		if p, ok := s.(*fyne.ShortcutPaste); ok && p.Clipboard != nil {
			v.send(screen.Event{Paste: p.Clipboard.Content()})
		}
	})
	v.draw(host.View())
	return v
}

func (v *view) send(ev screen.Event) { v.draw(v.host.Do(ev)) }

var keyNames = map[fyne.KeyName]string{
	fyne.KeyUp: "up", fyne.KeyDown: "down", fyne.KeyLeft: "left", fyne.KeyRight: "right",
	fyne.KeyReturn: "enter", fyne.KeyEnter: "enter", fyne.KeyEscape: "esc", fyne.KeyTab: "tab",
	fyne.KeyBackspace: "backspace",
}

func (v *view) onKey(e *fyne.KeyEvent) {
	if k, ok := keyNames[e.Name]; ok {
		v.send(screen.Event{Key: k})
	}
}

func (v *view) onRune(r rune) {
	if v.host.View().Editing {
		v.send(screen.Event{Text: string(r)})
		return
	}
	k := string(r)
	if r == ' ' {
		k = "space"
	}
	v.send(screen.Event{Key: k})
}

// text is one run of monospace text in one colour.
func text(s string, c color.Color, bold bool) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextSize = textSize
	t.TextStyle = fyne.TextStyle{Monospace: true, Bold: bold}
	return t
}

// tapped is a row (or tab) that reports clicks.
type tapped struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
}

func newTapped(content fyne.CanvasObject, onTap func()) *tapped {
	t := &tapped{content: content, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapped) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.content) }
func (t *tapped) Tapped(*fyne.PointEvent)             { t.onTap() }
func (t *tapped) Cursor() desktop.Cursor              { return desktop.PointerCursor }

// line lays out texts side by side with no gaps, as a terminal does.
func line(objs ...fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedHBoxLayout(0), objs...)
}

// pad right-pads s with spaces to n characters (a monospace font keeps columns aligned).
func pad(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func (v *view) draw(st screen.View) {
	var items []fyne.CanvasObject

	// Tabs.
	var tabs []fyne.CanvasObject
	for i, name := range st.Tabs {
		c, bold := cDim, false
		if i == st.Current {
			c, bold = cAccent, true
		}
		i := i
		tabs = append(tabs, newTapped(text("  "+name+"  ", c, bold), func() { v.send(screen.Event{Tab: &i}) }))
	}
	items = append(items, line(tabs...), canvas.NewRectangle(cLine))
	items[len(items)-1].(*canvas.Rectangle).SetMinSize(fyne.NewSize(1, 1))

	// Header.
	s := st.Screen
	head := []fyne.CanvasObject{text("  ♪ ", cAccent, false), text(s.Title, cFG, true)}
	if s.Subtitle != "" {
		head = append(head, text("  "+s.Subtitle, cDim, false))
	}
	items = append(items, text("", cFG, false), line(head...), text("", cFG, false))
	if st.Notice != "" {
		items = append(items, line(text("  ! "+st.Notice, cYellow, false)), text("", cFG, false))
	}

	// Rows.
	w := 0
	for _, r := range s.Rows {
		w = max(w, utf8.RuneCountInString(r.Label))
	}
	for i, r := range s.Rows {
		if r.Gap {
			items = append(items, text("", cFG, false))
		}
		marker, labelC, valueC := "  ", cDim, cFG
		switch {
		case r.Selected:
			marker, labelC, valueC = "› ", cAccent, cAccent
		case r.Dim:
			valueC = cDim
		case r.Good:
			valueC = cGreen
		}
		val := r.Value
		switch {
		case r.Selected && r.Editing:
			val += "▏"
		case r.Selected && r.Stepper && !r.Action:
			val = "‹ " + val + " ›"
		}
		row := line(text("  "+marker, cAccent, false), text(pad(r.Label, w)+"  ", labelC, false), text(val, valueC, false))
		var obj fyne.CanvasObject = row
		if r.Selected {
			obj = container.NewStack(canvas.NewRectangle(cLine), row)
		}
		i, selected := i, r.Selected
		items = append(items, newTapped(obj, func() {
			if selected {
				v.send(screen.Event{Key: "enter"})
			} else {
				v.send(screen.Event{Row: &i})
			}
		}))
		if r.Selected && r.Message != "" {
			items = append(items, line(text(strings.Repeat(" ", 6+w+2)+r.Message, cYellow, false)))
		}
	}

	// Footer. Esc leaves a screen in the terminal; in a window you just close it.
	foot := strings.Replace(s.Footer, " · esc done", "", 1)
	fl := []fyne.CanvasObject{text("  "+foot, cDim, false)}
	if s.Saved {
		fl = append(fl, text("   ✓ saved", cGreen, false))
	}
	items = append(items, text("", cFG, false), line(fl...))

	v.win.SetContent(container.NewStack(canvas.NewRectangle(cBG), container.NewVScroll(container.NewVBox(items...))))
}
