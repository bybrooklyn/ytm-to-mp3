//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/core"
)

// dialogPrompter asks a run's questions as dialogs over the window. A run asks from its own
// goroutine; each method shows its dialog on the UI thread and waits for the answer (or for
// the run to be stopped, which closes the dialog).
type dialogPrompter struct{ win fyne.Window }

// ask shows a dialog built by build on the UI thread and waits for send to be called, or for
// ctx to end (then the dialog is closed). after runs on the UI thread once it is showing
// (to focus a field).
func ask[T any](ctx context.Context, p dialogPrompter, def T, build func(answer func(T)) dialog.Dialog, after ...func()) (T, error) {
	ch := make(chan T, 1)
	var d dialog.Dialog
	onUI(func() {
		d = build(func(v T) {
			select {
			case ch <- v:
			default:
			}
		})
		d.Show()
		for _, f := range after {
			f()
		}
	})
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		onUI(d.Hide)
		return def, ctx.Err()
	}
}

// uiDo runs f on the UI thread and waits for it; uiPost does not wait. Tests replace them
// (their driver has no UI thread of its own).
var (
	uiDo   = fyne.DoAndWait
	uiPost = fyne.Do
)

func onUI(f func()) { uiDo(f) }

// openURL opens a link in the system browser; tests replace it.
var openURL = func(link string) error {
	u, err := url.Parse(link)
	if err != nil {
		return err
	}
	return fyne.CurrentApp().OpenURL(u)
}

// wrapped is a paragraph of ordinary text, lined up with the other pieces of a dialog.
func wrapped(s string) fyne.CanvasObject {
	return para(s, theme.ColorNameForeground, theme.SizeNameText)
}

// prose joins the lines of a text that was broken for a terminal into paragraphs: a line
// break stays before a bullet ("• ") and where there is a blank line.
func prose(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			out = append(out, "")
		case strings.HasPrefix(t, "• ") || len(out) == 0 || out[len(out)-1] == "":
			out = append(out, t)
		default:
			out[len(out)-1] += " " + t
		}
	}
	return strings.Join(out, "\n")
}

// paragraph is a detail text as wrapped prose.
func paragraph(s string) fyne.CanvasObject { return wrapped(prose(s)) }

func heading(s string) fyne.CanvasObject {
	rt := widget.NewRichText(&widget.TextSegment{Text: s, Style: widget.RichTextStyle{
		ColorName: theme.ColorNameForeground, SizeName: theme.SizeNameText, TextStyle: fyne.TextStyle{Bold: true},
	}})
	rt.Wrapping = fyne.TextWrapWord
	in := theme.InnerPadding()
	return container.New(layout.NewCustomPaddedLayout(-in, -in, -in, -in), rt)
}

// dialogWidth is how wide the window's dialogs are, within a narrow window.
func (p dialogPrompter) dialogWidth(want float32) float32 {
	return min(want, max(300, p.win.Canvas().Size().Width-48))
}

func (p dialogPrompter) Confirm(ctx context.Context, q core.Question) (bool, error) {
	yes, no := q.Yes, q.No
	if yes == "" {
		yes = "Yes"
	}
	if no == "" {
		no = "No"
	}
	return ask(ctx, p, q.Default, func(answer func(bool)) dialog.Dialog {
		var content fyne.CanvasObject = wrapped(q.Title)
		if q.Detail != "" {
			content = container.NewVBox(heading(q.Title), paragraph(q.Detail))
		}
		d := dialog.NewCustomConfirm("", yes, no, content, answer, p.win)
		d.Resize(fyne.NewSize(p.dialogWidth(460), d.MinSize().Height))
		return d
	})
}

// choiceList is a list of options, one picked: each is a flat row with a marker and its text
// wrapped, so long labels (a Takeout export's name, size and age) stay readable.
func choiceList(opts []core.Option, pick *string) fyne.CanvasObject {
	list := container.New(layout.NewCustomPaddedVBoxLayout(2))
	var render func()
	render = func() {
		list.Objects = nil
		for _, o := range opts {
			o := o
			sel := o.Value == *pick
			dot := outline(colMuted, 1.5)
			dot.CornerRadius = 7
			var mark fyne.CanvasObject = sized(14, 14, dot)
			if sel {
				fill := rect(theme.ColorNamePrimary)
				fill.CornerRadius = 7
				inner := rect(colOnAccent)
				inner.CornerRadius = 3
				mark = sized(14, 14, container.NewStack(fill, container.NewCenter(sized(6, 6, inner))))
			}
			text := container.New(layout.NewCustomPaddedVBoxLayout(sp4), para(o.Label, theme.ColorNameForeground, theme.SizeNameText))
			if o.Hint != "" {
				text.Add(para(o.Hint, colMuted, theme.SizeNameCaptionText))
			}
			var bg color.Color = color.Transparent
			if sel {
				bg = pal(colTint)
			}
			row := newClick(padded(container.NewBorder(nil, nil, container.NewCenter(mark), nil, text), sp8, sp8), bg, pal(colHover), true, func() {
				*pick = o.Value
				render()
				list.Refresh()
			})
			list.Add(row)
		}
	}
	render()
	return list
}

func (p dialogPrompter) Select(ctx context.Context, title, detail string, options []core.Option, def string) (string, error) {
	pick := def
	if pick == "" && len(options) > 0 {
		pick = options[0].Value
	}
	got, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
		content := container.NewVBox(heading(title))
		if detail != "" {
			content.Add(paragraph(detail))
		}
		var list fyne.CanvasObject = choiceList(options, &pick)
		if len(options) > 5 {
			sc := container.NewVScroll(list)
			sc.SetMinSize(fyne.NewSize(0, 240))
			list = sc
		}
		content.Add(list)
		d := dialog.NewCustomConfirm("", "Choose", "Cancel", content, answer, p.win)
		d.Resize(fyne.NewSize(p.dialogWidth(520), d.MinSize().Height))
		return d
	})
	if err != nil {
		return def, err
	}
	if !got {
		return def, core.ErrBack
	}
	return pick, nil
}

func (p dialogPrompter) Input(ctx context.Context, title, detail, def string) (string, error) {
	var entry *widget.Entry
	onUI(func() { entry = widget.NewEntry(); entry.SetText(def) })
	got, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
		content := container.NewVBox(heading(title))
		if detail != "" {
			content.Add(paragraph(detail))
		}
		content.Add(entry)
		d := dialog.NewCustomConfirm("", "OK", "Cancel", content, answer, p.win)
		d.Resize(fyne.NewSize(p.dialogWidth(460), d.MinSize().Height))
		return d
	}, func() { p.win.Canvas().Focus(entry) })
	if err != nil {
		return def, err
	}
	if !got {
		return def, core.ErrBack
	}
	if entry.Text == "" {
		return def, nil
	}
	return entry.Text, nil
}

// askValidated shows body in a dialog with OK and Cancel. When OK is pressed and validate
// finds a problem, the dialog comes back with the problem written under the fields and
// what was typed kept. Cancel is core.ErrBack.
func (p dialogPrompter) askValidated(ctx context.Context, ok string, width float32, body fyne.CanvasObject, entries []*widget.Entry, focus fyne.Focusable, validate func() error) error {
	msg := ""
	for {
		errBox := container.NewVBox()
		if msg != "" {
			errBox.Add(para(msg, colProblem, theme.SizeNameCaptionText))
		}
		got, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
			d := dialog.NewCustomConfirm("", ok, "Cancel", container.NewVBox(body, errBox), answer, p.win)
			d.Resize(fyne.NewSize(p.dialogWidth(width), d.MinSize().Height))
			for _, e := range entries {
				e.OnSubmitted = func(string) { answer(true); d.Hide() }
			}
			return d
		}, func() {
			if focus != nil {
				p.win.Canvas().Focus(focus)
			}
		})
		if err != nil {
			return err
		}
		if !got {
			return core.ErrBack
		}
		if e := validate(); e != nil {
			msg = capital(strings.TrimSpace(e.Error()))
			continue
		}
		return nil
	}
}

// formControl is one input of a form and how to read what the person made of it.
type formControl struct {
	fd  core.Field
	get func() string
}

// Form asks several fields in one dialog. Secrets are masked. The widgets are built on the
// UI thread; what is typed is read after the dialog has answered.
func (p dialogPrompter) Form(ctx context.Context, f core.FormSpec) (map[string]string, error) {
	var body *fyne.Container
	var entries []*widget.Entry
	var focus fyne.Focusable
	var ctls []formControl
	onUI(func() { body, entries, focus, ctls = buildForm(f) })
	// Enter in the last text field accepts; in an earlier one it moves on (the dialog's OK).
	err := p.askValidated(ctx, orStr(f.Submit, "OK"), 480, body, entries, focus, func() error {
		for _, c := range ctls {
			if c.fd.Validate != nil {
				if err := c.fd.Validate(c.get()); err != nil {
					// the message sits under the whole form, so say which field it is about
					if label := strings.TrimSpace(c.fd.Label); label != "" && len(f.Fields) > 1 {
						return fmt.Errorf("%s (%s)", strings.TrimSpace(err.Error()), label)
					}
					return err
				}
			}
		}
		return nil
	})
	out := map[string]string{}
	for _, c := range ctls {
		out[c.fd.Key] = c.get()
	}
	return out, err
}

func buildForm(f core.FormSpec) (body *fyne.Container, entries []*widget.Entry, focus fyne.Focusable, ctls []formControl) {
	type control = formControl
	body = container.New(layout.NewCustomPaddedVBoxLayout(sp12))
	head := f.Title
	if head == "" && len(f.Fields) == 1 {
		head = f.Fields[0].Label
	}
	if head != "" {
		body.Add(heading(head))
	}
	if f.Detail != "" {
		body.Add(paragraph(f.Detail))
	}
	for _, fd := range f.Fields {
		fd := fd
		item := container.New(layout.NewCustomPaddedVBoxLayout(sp4))
		if !(len(f.Fields) == 1 && f.Title == "" && fd.Label == head) {
			item.Add(txt(fd.Label, theme.ColorNameForeground, 14, true))
		}
		if fd.Detail != "" {
			item.Add(para(prose(fd.Detail), colMuted, theme.SizeNameCaptionText))
		}
		switch fd.Kind {
		case core.FieldChoice:
			pick := fd.Default
			if pick == "" && len(fd.Options) > 0 {
				pick = fd.Options[0].Value
			}
			item.Add(choiceList(fd.Options, &pick))
			ctls = append(ctls, control{fd, func() string { return pick }})
		case core.FieldYesNo:
			yes, no := fd.Yes, fd.No
			if yes == "" {
				yes = "Yes"
			}
			if no == "" {
				no = "No"
			}
			cur := yes
			if fd.Default == "no" {
				cur = no
			}
			seg := container.NewStack()
			var redraw func()
			redraw = func() {
				seg.Objects = []fyne.CanvasObject{segmented([]string{yes, no}, cur, func(s string) { cur = s; redraw() })}
				seg.Refresh()
			}
			redraw()
			item.Add(container.NewHBox(seg))
			ctls = append(ctls, control{fd, func() string {
				if cur == no {
					return "no"
				}
				return "yes"
			}})
		default:
			var e *widget.Entry
			if fd.Kind == core.FieldSecret {
				e = widget.NewPasswordEntry()
			} else {
				e = widget.NewEntry()
			}
			e.SetText(fd.Default)
			entries = append(entries, e)
			if focus == nil {
				focus = e
			}
			item.Add(e)
			ctls = append(ctls, control{fd, func() string { return strings.TrimSpace(e.Text) }})
		}
		body.Add(item)
	}
	return body, entries, focus, ctls
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// File asks for a path, with Browse buttons for a file chooser and a folder chooser.
func (p dialogPrompter) File(ctx context.Context, r core.FileRequest) (string, error) {
	var entry *widget.Entry
	var body *fyne.Container
	onUI(func() { entry, body = p.fileBody(r) })
	err := p.askValidated(ctx, "OK", 520, body, []*widget.Entry{entry}, entry, func() error {
		if strings.TrimSpace(entry.Text) == "" {
			return errors.New("choose or paste a path")
		}
		if r.Validate != nil {
			return r.Validate(strings.TrimSpace(entry.Text))
		}
		return nil
	})
	return strings.Trim(entry.Text, `"' `), err
}

// fileBody is the question's content: the path field and its Browse buttons.
func (p dialogPrompter) fileBody(r core.FileRequest) (*widget.Entry, *fyne.Container) {
	entry := widget.NewEntry()
	entry.TextStyle = fyne.TextStyle{Monospace: true}
	entry.SetPlaceHolder("Paste a path, or browse")
	pickInto := func(path string) {
		if path != "" {
			entry.SetText(path)
		}
	}
	var browse []fyne.CanvasObject
	if r.File {
		browse = append(browse, button("Choose file...", false, func() {
			fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
				if err == nil && rc != nil {
					pickInto(rc.URI().Path())
					_ = rc.Close()
				}
			}, p.win)
			if len(r.Exts) > 0 {
				fd.SetFilter(storage.NewExtensionFileFilter(r.Exts))
			}
			fd.Show()
		}))
	}
	if r.Folder {
		browse = append(browse, button("Choose folder...", false, func() {
			dialog.ShowFolderOpen(func(u fyne.ListableURI, err error) {
				if err == nil && u != nil {
					pickInto(u.Path())
				}
			}, p.win)
		}))
	}
	body := container.New(layout.NewCustomPaddedVBoxLayout(sp12), heading(r.Title))
	if r.Detail != "" {
		body.Add(paragraph(r.Detail))
	}
	body.Add(entry)
	body.Add(container.New(layout.NewCustomPaddedHBoxLayout(sp8), browse...))
	return entry, body
}

// infoBody is the content of Show and Wait: text, numbered steps, a code, a link button.
func (p dialogPrompter) infoBody(i core.Info) *fyne.Container {
	body := container.New(layout.NewCustomPaddedVBoxLayout(sp12))
	if i.Title != "" {
		// "Heading · a quieter second part" (the terminal prints it that way too)
		head, rest, _ := strings.Cut(i.Title, " · ")
		body.Add(heading(head))
		if rest != "" {
			body.Add(para(capital(rest)+".", colMuted, theme.SizeNameCaptionText))
		}
	}
	if i.Detail != "" && i.Code == "" {
		body.Add(paragraph(i.Detail))
	}
	if len(i.Steps) > 0 {
		steps := container.New(layout.NewCustomPaddedVBoxLayout(sp8))
		for n, s := range i.Steps {
			num := txt(string(rune('1'+n))+".", colMuted, 14, true)
			steps.Add(container.NewBorder(nil, nil, sized(20, 20, num), nil, para(s, theme.ColorNameForeground, theme.SizeNameText)))
		}
		body.Add(steps)
	}
	if i.Code != "" {
		code := widget.NewLabelWithStyle(i.Code, fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Monospace: true})
		code.SizeName = theme.SizeNameSubHeadingText
		copyBtn := button("Copy code", false, func() { fyne.CurrentApp().Clipboard().SetContent(i.Code) })
		box := container.New(layout.NewCustomPaddedVBoxLayout(sp4), txt("Your code", colMuted, 13, true),
			container.NewBorder(nil, nil, nil, container.NewCenter(copyBtn), code))
		if i.Detail != "" {
			box.Add(para(capital(i.Detail)+".", colMuted, theme.SizeNameCaptionText))
		}
		body.Add(box)
	}
	if i.Link != "" {
		label := i.LinkLabel
		if label == "" {
			label = "Open in browser"
		}
		body.Add(container.NewHBox(button(label, false, func() { _ = openURL(i.Link) })))
	}
	return body
}

// Show tells the person something to read, with its link.
func (p dialogPrompter) Show(ctx context.Context, i core.Info) error {
	_, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
		d := dialog.NewCustomConfirm("", "OK", "Close", p.infoBody(i), answer, p.win)
		d.Resize(fyne.NewSize(p.dialogWidth(480), d.MinSize().Height))
		return d
	})
	return err
}

// Wait shows i with a Cancel button while f runs. Cancel ends f's context and comes back
// as core.ErrBack; so does f finishing by itself with the dialog closed.
func (p dialogPrompter) Wait(ctx context.Context, i core.Info, f func(context.Context) error) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var d dialog.Dialog
	cancelled := make(chan struct{})
	onUI(func() {
		body := p.infoBody(i)
		if i.Waiting != "" {
			body.Add(container.New(layout.NewCustomPaddedVBoxLayout(sp8), widget.NewProgressBarInfinite(), para(i.Waiting, colMuted, theme.SizeNameCaptionText)))
		}
		body.Add(container.NewHBox(layout.NewSpacer(), button("Cancel", false, func() {
			select {
			case <-cancelled:
			default:
				close(cancelled)
			}
			cancel()
		})))
		d = dialog.NewCustomWithoutButtons("", body, p.win)
		d.Resize(fyne.NewSize(p.dialogWidth(480), d.MinSize().Height))
		d.Show()
	})
	defer onUI(d.Hide)
	err := f(cctx)
	select {
	case <-cancelled:
		if ctx.Err() == nil {
			return core.ErrBack
		}
	default:
	}
	return err
}

func (p dialogPrompter) Notice(title, detail string) {
	msg := title
	if detail != "" {
		msg += "\n\n" + detail
	}
	fyne.Do(func() { dialog.ShowInformation("", msg, p.win) })
}

func (p dialogPrompter) Working(ctx context.Context, title string, f func(context.Context) error) error {
	var d dialog.Dialog
	onUI(func() {
		bar := widget.NewProgressBarInfinite()
		d = dialog.NewCustomWithoutButtons("", container.NewVBox(wrapped(title), bar), p.win)
		d.Show()
	})
	defer onUI(d.Hide)
	return f(ctx)
}
