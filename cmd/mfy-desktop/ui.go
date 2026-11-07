//go:build cgo

package main

import (
	"image/color"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

// view draws a screen.Host in a window and sends what the user does back to it. All the
// behaviour is the Host's, shared with the terminal UI; this file only draws and listens.
// The look is described in docs/DESIGN.md.
type view struct {
	host *screen.Host
	win  fyne.Window

	// The window is built once; draw refills these.
	bgRect, sideRect *canvas.Rectangle
	sidebar, top     *fyne.Container
	header, shell    *fyne.Container
	scroll           *container.Scroll
	selected         fyne.CanvasObject // the cursor row, kept in view

	mu sync.Mutex // draw

	// The Downloads page (see downloads.go).
	dl    *screen.Downloads
	entry *widget.Entry
}

// minWindow is the smallest the window can be made.
var minWindow = fyne.NewSize(680, 440)

// newView fills win with the host's screen and wires up the keyboard and mouse.
func newView(win fyne.Window, host *screen.Host) *view {
	v := &view{host: host, win: win}
	v.bgRect = canvas.NewRectangle(color.Transparent)
	v.sideRect = canvas.NewRectangle(color.Transparent)
	v.sidebar = container.New(layout.NewCustomPaddedVBoxLayout(sp4))
	v.top = container.NewVBox()
	v.scroll = container.NewVScroll(container.NewVBox())

	side := container.NewStack(v.sideRect, container.New(fixedWidth{sidebarWidth}, v.sidebar))
	v.header = container.New(column{contentMax}, v.top)
	floor := canvas.NewRectangle(color.Transparent)
	floor.SetMinSize(minWindow)
	v.shell = container.New(shell{}, side, v.header, v.scroll)
	win.SetPadded(false) // the sidebar runs to the window's edges
	win.SetContent(container.NewStack(v.bgRect, floor, v.shell))

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
	// Ctrl+1, Ctrl+2, ... open the sidebar's items.
	for i := range min(len(host.View().Tabs), 9) {
		c.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyName(strconv.Itoa(i + 1)), Modifier: fyne.KeyModifierControl}, func(fyne.Shortcut) {
			v.send(screen.Event{Tab: &i})
		})
	}
	// Follow the system when it flips between light and dark.
	fyne.CurrentApp().Settings().AddListener(func(fyne.Settings) {
		fyne.Do(func() { v.draw(v.host.View()) })
	})
	v.draw(host.View())
	return v
}

func (v *view) send(ev screen.Event) { v.draw(v.host.Do(ev)) }

var keyNames = map[fyne.KeyName]string{
	fyne.KeyUp: "up", fyne.KeyDown: "down", fyne.KeyLeft: "left", fyne.KeyRight: "right",
	fyne.KeyReturn: "enter", fyne.KeyEnter: "enter", fyne.KeyEscape: "esc", fyne.KeyTab: "tab",
	fyne.KeyBackspace: "backspace", fyne.KeySpace: "space",
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

// ---- layouts -------------------------------------------------------------------------

// shell is the window: the sidebar down the left edge; beside it the page header, and under
// the header the scrolling sections. No gaps of its own: spacing is the pieces' business.
type shell struct{}

func (shell) Layout(o []fyne.CanvasObject, s fyne.Size) {
	side, head, body := o[0], o[1], o[2]
	side.Move(fyne.NewPos(0, 0))
	side.Resize(fyne.NewSize(sidebarWidth, s.Height))
	w := s.Width - sidebarWidth
	h := head.MinSize().Height
	head.Move(fyne.NewPos(sidebarWidth, 0))
	head.Resize(fyne.NewSize(w, h))
	body.Move(fyne.NewPos(sidebarWidth, h))
	body.Resize(fyne.NewSize(w, s.Height-h))
}
func (shell) MinSize(o []fyne.CanvasObject) fyne.Size {
	hm, bm := o[1].MinSize(), o[2].MinSize()
	return fyne.NewSize(sidebarWidth+max(hm.Width, bm.Width), max(o[0].MinSize().Height, hm.Height+bm.Height))
}

// fixedWidth gives its one child a fixed width and its own natural height.
type fixedWidth struct{ w float32 }

func (l fixedWidth) Layout(o []fyne.CanvasObject, s fyne.Size) {
	o[0].Move(fyne.NewPos(0, 0))
	o[0].Resize(fyne.NewSize(l.w, s.Height))
}
func (l fixedWidth) MinSize(o []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(l.w, o[0].MinSize().Height)
}

// column holds its one child in a left-aligned column at most max wide, with page margins
// that shrink in a narrow window. Content is never centred.
type column struct{ max float32 }

func margin(w float32) float32 {
	if w >= 560 {
		return sp32
	}
	return sp16
}

func (l column) Layout(o []fyne.CanvasObject, s fyne.Size) {
	m := margin(s.Width)
	o[0].Move(fyne.NewPos(m, 0))
	o[0].Resize(fyne.NewSize(min(s.Width-2*m, l.max), s.Height))
}
func (l column) MinSize(o []fyne.CanvasObject) fyne.Size {
	ms := o[0].MinSize()
	return fyne.NewSize(ms.Width+2*sp16, ms.Height)
}

// ---- small pieces --------------------------------------------------------------------

func gap(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(1, h))
	return r
}

func rect(fill fyne.ThemeColorName) *canvas.Rectangle { return canvas.NewRectangle(pal(fill)) }

// outline is a rounded, empty box with a border.
func outline(stroke fyne.ThemeColorName, width float32) *canvas.Rectangle {
	r := canvas.NewRectangle(color.Transparent)
	r.StrokeColor, r.StrokeWidth, r.CornerRadius = pal(stroke), width, radius
	return r
}

// txt is one line of text. size is a point size from the type scale.
func txt(s string, c fyne.ThemeColorName, size float32, bold bool) *canvas.Text {
	t := canvas.NewText(s, pal(c))
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Bold: bold}
	return t
}

// para is wrapping text in the quieter help style.
func para(s string, c fyne.ThemeColorName, size fyne.ThemeSizeName) fyne.CanvasObject {
	rt := widget.NewRichText(&widget.TextSegment{Text: s, Style: widget.RichTextStyle{
		ColorName: c, SizeName: size, Inline: false,
	}})
	rt.Wrapping = fyne.TextWrapWord
	// Widgets pad their text by the inner padding; take it back so text lines up with labels.
	in := theme.InnerPadding()
	return container.New(layout.NewCustomPaddedLayout(-in, -in, -in, -in), rt)
}

func padded(o fyne.CanvasObject, v, h float32) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(v, v, h, h), o)
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// click is anything that can be clicked: a background that lightens under the pointer, some
// content on top.
type click struct {
	widget.BaseWidget
	bg            *canvas.Rectangle
	content       fyne.CanvasObject
	normal, hover color.Color
	onTap         func()
	pointer       bool
}

func newClick(content fyne.CanvasObject, normal, hover color.Color, pointer bool, onTap func()) *click {
	c := &click{bg: canvas.NewRectangle(normal), content: content, normal: normal, hover: hover, onTap: onTap, pointer: pointer}
	c.bg.CornerRadius = radius - 2
	c.ExtendBaseWidget(c)
	return c
}

func (c *click) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack(c.bg, c.content))
}
func (c *click) Tapped(*fyne.PointEvent) { c.onTap() }
func (c *click) Cursor() desktop.Cursor {
	if c.pointer {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}
func (c *click) MouseIn(*desktop.MouseEvent)    { c.bg.FillColor = c.hover; c.bg.Refresh() }
func (c *click) MouseMoved(*desktop.MouseEvent) {}
func (c *click) MouseOut()                      { c.bg.FillColor = c.normal; c.bg.Refresh() }

// ---- controls ------------------------------------------------------------------------

func button(label string, disabled bool, onTap func()) fyne.CanvasObject {
	c := fyne.ThemeColorName(theme.ColorNameForeground)
	if disabled {
		c = colMuted
	}
	body := container.NewStack(outline(colBorder, 1), padded(container.NewCenter(txt(label, c, 14, false)), sp8, sp12))
	return newClick(body, color.Transparent, pal(colHover), true, onTap)
}

func sized(w, h float32, o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWrap(fyne.NewSize(w, h), o)
}

// toggle is a switch.
func toggle(on, disabled bool, onTap func()) fyne.CanvasObject {
	const w, h = 36, 20
	track := canvas.NewRectangle(color.Transparent)
	track.CornerRadius = h / 2
	knob := canvas.NewCircle(pal(colMuted))
	knob.Resize(fyne.NewSize(12, 12))
	knob.Move(fyne.NewPos(4, 4))
	track.StrokeWidth, track.StrokeColor = 1.5, pal(colMuted)
	if on {
		track.FillColor, track.StrokeColor = pal(theme.ColorNamePrimary), pal(theme.ColorNamePrimary)
		knob.FillColor = pal(colOnAccent)
		knob.Move(fyne.NewPos(w-12-4, 4))
	}
	if disabled {
		track.StrokeColor = pal(colBorder)
		if on {
			track.FillColor = pal(colBorder)
		}
		knob.FillColor = pal(colMuted)
	}
	track.Resize(fyne.NewSize(w, h))
	face := sized(w, h, container.NewWithoutLayout(track, knob))
	return newClick(padded(face, sp4, sp4), color.Transparent, color.Transparent, true, onTap)
}

// segmented is a row of choices, one picked.
func segmented(opts []string, cur string, pick func(string)) fyne.CanvasObject {
	var segs []fyne.CanvasObject
	for _, o := range opts {
		o := o
		fg, normal, hover := fyne.ThemeColorName(theme.ColorNameForeground), color.Color(color.Transparent), pal(colHover)
		if o == cur {
			fg, normal, hover = colOnAccent, pal(theme.ColorNamePrimary), pal(theme.ColorNamePrimary)
		}
		segs = append(segs, newClick(padded(container.NewCenter(txt(o, fg, 14, o == cur)), sp4, sp12), normal, hover, true, func() { pick(o) }))
	}
	return container.NewStack(outline(colBorder, 1), padded(container.New(layout.NewCustomPaddedHBoxLayout(0), segs...), 2, 2))
}

// dropdown is a closed list that opens a menu of its options.
func (v *view) dropdown(opts []string, cur string, pick func(string)) fyne.CanvasObject {
	icon := canvas.NewImageFromResource(theme.MenuDropDownIcon())
	icon.FillMode = canvas.ImageFillContain
	icon.SetMinSize(fyne.NewSize(16, 16))
	body := container.NewStack(outline(colBorder, 1), padded(container.NewHBox(txt(cur, theme.ColorNameForeground, 14, false), gap(1), icon), sp8, sp12))
	var c *click
	c = newClick(body, color.Transparent, pal(colHover), true, func() {
		var items []*fyne.MenuItem
		for _, o := range opts {
			o := o
			it := fyne.NewMenuItem(o, func() { pick(o) })
			it.Checked = o == cur
			items = append(items, it)
		}
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(c)
		widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), v.win.Canvas(), pos.Add(fyne.NewPos(0, c.Size().Height)))
	})
	return c
}

// stepper is "- value +" for a number.
func stepper(val string, step func(int)) fyne.CanvasObject {
	btn := func(s string, d int) fyne.CanvasObject {
		return newClick(sized(32, 32, container.NewCenter(txt(s, theme.ColorNameForeground, 16, false))), color.Transparent, pal(colHover), true, func() { step(d) })
	}
	mid := container.NewStack(gap(1), container.NewCenter(txt(val, theme.ColorNameForeground, 14, true)))
	mid = container.NewStack(sized(56, 32, mid))
	return container.NewStack(outline(colBorder, 1), padded(container.New(layout.NewCustomPaddedHBoxLayout(0), btn("−", -1), mid, btn("+", 1)), 0, 2))
}

// status is a dot and words: never colour alone.
func status(s string, good, dim bool) fyne.CanvasObject {
	dot := canvas.NewCircle(pal(colMuted))
	c := fyne.ThemeColorName(colMuted)
	if good {
		dot.FillColor, c = pal(colSuccess), theme.ColorNameForeground
	} else if dim {
		dot.FillColor = color.Transparent
		dot.StrokeColor, dot.StrokeWidth = pal(colMuted), 1.5
	}
	return container.New(layout.NewCustomPaddedHBoxLayout(sp8), container.NewCenter(sized(8, 8, dot)), txt(s, c, 13, false))
}

// ---- rows ----------------------------------------------------------------------------

func (v *view) rowView(i int, r screen.Row) fyne.CanvasObject {
	set := func(s screen.Set) func() { s.Row = i; return func() { v.send(screen.Event{Set: &s}) } }

	name := fyne.ThemeColorName(theme.ColorNameForeground)
	if r.Dim && r.Kind != screen.KindInfo {
		name = colMuted
	}
	left := container.New(layout.NewCustomPaddedVBoxLayout(sp4), txt(r.Label, name, 14, true))
	if r.Kind == screen.KindAction || r.Kind == screen.KindInfo {
		s := r.Value
		if r.Status != "" {
			s = r.Status
		}
		if s != "" {
			left.Add(status(capital(s), r.Good, r.Dim))
		}
	}
	help := r.Help
	if r.Editing {
		help = "Type a folder, then press Enter to save or Esc to cancel."
	}
	if help == "" && r.Selected && r.Message != "" && r.Error == "" && r.Kind == "" {
		help = r.Message
	}
	if help != "" {
		left.Add(para(help, colMuted, theme.SizeNameCaptionText))
	}
	if r.Error != "" {
		left.Add(para(r.Error, colProblem, theme.SizeNameCaptionText))
	}

	var body fyne.CanvasObject
	switch r.Kind {
	case screen.KindToggle:
		next := "on"
		if r.On {
			next = "off"
		}
		body = container.NewBorder(nil, nil, nil, container.NewCenter(toggle(r.On, r.Dim, set(screen.Set{Value: next}))), left)
	case screen.KindChoice:
		pick := func(o string) { v.send(screen.Event{Set: &screen.Set{Row: i, Value: o}}) }
		var ctl fyne.CanvasObject
		if len(r.Options) <= 5 {
			ctl = segmented(r.Options, r.Value, pick)
		} else {
			ctl = v.dropdown(r.Options, r.Value, pick)
		}
		body = container.NewBorder(nil, nil, nil, container.NewCenter(ctl), left)
	case screen.KindNumber:
		body = container.NewBorder(nil, nil, nil, container.NewCenter(stepper(r.Value, func(d int) {
			v.send(screen.Event{Set: &screen.Set{Row: i, Step: d}})
		})), left)
	case screen.KindText:
		body = container.New(layout.NewCustomPaddedVBoxLayout(sp8), left, v.textField(i, r))
	case screen.KindAction:
		label := r.Button
		if label == "" {
			label = "Open"
		}
		body = container.NewBorder(nil, nil, nil, container.NewCenter(button(label, false, set(screen.Set{Press: true}))), left)
	case screen.KindInfo:
		body = left
	default: // no hint: label and value, whatever they are
		val := widget.NewLabel(r.Value)
		val.Alignment = fyne.TextAlignTrailing
		val.Truncation = fyne.TextTruncateEllipsis
		body = container.NewBorder(nil, nil, nil, val, left)
	}

	// The cursor row: faint accent tint and a ring. This is the keyboard focus.
	bg := canvas.NewRectangle(color.Transparent)
	var ring fyne.CanvasObject = gap(0)
	if r.Selected {
		bg.FillColor = pal(colTint)
		rr := outline(theme.ColorNamePrimary, 2)
		rr.CornerRadius = radius - 2
		ring = rr
	}
	content := container.NewStack(bg, ring, padded(body, sp12, sp16))
	row := newClick(content, color.Transparent, pal(colHover), false, func() {
		if r.Kind == screen.KindToggle {
			next := "on"
			if r.On {
				next = "off"
			}
			v.send(screen.Event{Set: &screen.Set{Row: i, Value: next}})
			return
		}
		v.send(screen.Event{Row: &i})
	})
	row.bg.FillColor, row.normal = color.Transparent, color.Transparent
	if r.Selected {
		v.selected = row
	}
	return row
}

// textField is the music folder: a path in monospace, a Browse button, and while it is being
// typed, Save and Cancel.
func (v *view) textField(i int, r screen.Row) fyne.CanvasObject {
	val := r.Value
	box := outline(colBorder, 1)
	var field fyne.CanvasObject
	if r.Editing {
		box.StrokeColor, box.StrokeWidth = pal(theme.ColorNamePrimary), 2
		caret := canvas.NewRectangle(pal(theme.ColorNamePrimary))
		caret.SetMinSize(fyne.NewSize(2, 16))
		field = container.NewStack(box, padded(container.New(layout.NewCustomPaddedHBoxLayout(1), monoLabel(val, false), container.NewCenter(caret)), sp4, sp8))
	} else {
		field = newClick(container.NewStack(box, padded(monoLabel(val, true), sp4, sp8)), color.Transparent, pal(colHover), true,
			func() { v.send(screen.Event{Set: &screen.Set{Row: i, Press: true}}) })
	}
	var btns fyne.CanvasObject
	if r.Editing {
		btns = container.NewHBox(
			button("Save", false, func() { v.send(screen.Event{Key: "enter"}) }),
			button("Cancel", false, func() { v.send(screen.Event{Key: "esc"}) }))
	} else {
		btns = button("Browse...", false, func() {
			dialog.ShowFolderOpen(func(u fyne.ListableURI, err error) {
				if err == nil && u != nil {
					v.send(screen.Event{Set: &screen.Set{Row: i, Value: u.Path()}})
				}
			}, v.win)
		})
	}
	return container.NewBorder(nil, nil, nil, btns, field)
}

func monoLabel(s string, ellipsis bool) *widget.Label {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	if ellipsis {
		l.Truncation = fyne.TextTruncateEllipsis
	}
	return l
}

// ---- the window ----------------------------------------------------------------------

// section is a heading above a bordered list of rows.
func (v *view) section(heading string, rows []fyne.CanvasObject) fyne.CanvasObject {
	list := container.New(layout.NewCustomPaddedVBoxLayout(0))
	for k, r := range rows {
		if k > 0 {
			div := rect(colBorder)
			div.SetMinSize(fyne.NewSize(1, 1))
			list.Add(div)
		}
		list.Add(r)
	}
	box := container.NewStack(
		func() *canvas.Rectangle {
			b := rect(colSurface)
			b.StrokeColor, b.StrokeWidth, b.CornerRadius = pal(colBorder), 1, radius
			return b
		}(), padded(list, 1, 1))
	if heading == "" {
		return box
	}
	return container.New(layout.NewCustomPaddedVBoxLayout(sp8), txt(heading, colMuted, 13, true), box)
}

func (v *view) draw(st screen.View) {
	// Fyne runs this on its one UI thread; the lock is for a test driver, which runs
	// callbacks on whichever goroutine made them.
	v.mu.Lock()
	defer v.mu.Unlock()
	v.selected = nil
	v.bgRect.FillColor = pal(theme.ColorNameBackground)
	v.sideRect.FillColor = pal(colSidebar)
	v.bgRect.Refresh()
	v.sideRect.Refresh()

	// Sidebar: the wordmark, then one item per tab. Future sections are more tabs.
	v.sidebar.Objects = nil
	v.sidebar.Add(padded(txt("mfetchy", theme.ColorNameForeground, 18, true), sp24, sp16))
	for i, name := range st.Tabs {
		i := i
		active := i == st.Current
		fg, bold := fyne.ThemeColorName(theme.ColorNameForeground), false
		var bar fyne.CanvasObject = gap(0)
		if active {
			fg, bold = theme.ColorNamePrimary, true
			b := rect(theme.ColorNamePrimary)
			b.SetMinSize(fyne.NewSize(3, 1))
			bar = b
		}
		item := newClick(container.NewBorder(nil, nil, bar, nil, padded(txt(capital(name), fg, 14, bold), sp8, sp12)),
			color.Transparent, pal(colHover), true, func() { v.send(screen.Event{Tab: &i}) })
		v.sidebar.Add(padded(item, 0, sp8))
	}
	v.sidebar.Refresh()

	// Header: title, where it is stored, whether the last change saved.
	s := st.Screen
	title := container.NewBorder(nil, nil, nil, nil, txt(capital(s.Title), theme.ColorNameForeground, 24, true))
	if s.Saved {
		title = container.NewBorder(nil, nil, nil, container.NewCenter(status("Saved", true, false)), title)
	}
	v.top.Objects = []fyne.CanvasObject{gap(sp32), title}
	if s.Subtitle != "" {
		path := txt(s.Subtitle, colMuted, 13, false)
		path.TextStyle.Monospace = true
		v.top.Add(gap(sp4))
		v.top.Add(container.New(layout.NewCustomPaddedHBoxLayout(sp4), txt("Changes save as you make them, to", colMuted, 13, false), path))
	}
	if st.Notice != "" {
		v.top.Add(gap(sp8))
		v.top.Add(notice(st.Notice))
	}
	v.top.Add(gap(sp16))
	v.top.Refresh()
	v.header.Refresh()
	v.shell.Refresh() // the header's height changes with its notice

	if s.Downloads != nil && v.dl != nil {
		v.drawDownloads(*s.Downloads)
		return
	}

	// Sections.
	var secs []fyne.CanvasObject
	var rows []fyne.CanvasObject
	heading := ""
	flush := func() {
		if len(rows) > 0 {
			secs = append(secs, v.section(heading, rows))
		}
		rows = nil
	}
	for i, r := range s.Rows {
		if r.Group != "" {
			flush()
			heading = r.Group
		}
		rows = append(rows, v.rowView(i, r))
	}
	flush()
	page := container.New(layout.NewCustomPaddedVBoxLayout(sp24), secs...)
	v.scroll.Content = container.New(column{contentMax}, container.NewVBox(page, gap(sp16)))
	v.scroll.Refresh()
	v.reveal()
}

// reveal scrolls so the cursor row is on screen.
func (v *view) reveal() {
	if v.selected == nil {
		return
	}
	d := fyne.CurrentApp().Driver()
	top := d.AbsolutePositionForObject(v.selected).Y - d.AbsolutePositionForObject(v.scroll).Y
	bottom := top + v.selected.Size().Height
	switch {
	case top < 0:
		v.scroll.ScrollToOffset(fyne.NewPos(0, max(0, v.scroll.Offset.Y+top-sp16)))
	case bottom > v.scroll.Size().Height:
		v.scroll.ScrollToOffset(fyne.NewPos(0, v.scroll.Offset.Y+bottom-v.scroll.Size().Height+sp16))
	}
}

// notice is an inline message: what to do, and where. Surface background, a caution bar.
func notice(msg string) fyne.CanvasObject {
	lead, body := msg, ""
	if a, b, ok := strings.Cut(msg, ": "); ok {
		lead, body = a, capital(b)
	}
	bar := rect(colCaution)
	bar.SetMinSize(fyne.NewSize(3, 1))
	text := container.New(layout.NewCustomPaddedVBoxLayout(sp4), para(lead, theme.ColorNameForeground, theme.SizeNameText))
	if body != "" {
		text.Add(para(body, colMuted, theme.SizeNameCaptionText))
	}
	box := rect(colSurface)
	box.StrokeColor, box.StrokeWidth, box.CornerRadius = pal(colBorder), 1, radius
	return container.NewStack(box, container.NewBorder(nil, nil, bar, nil, padded(text, sp8, sp12)))
}
