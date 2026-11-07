//go:build cgo

package main

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/core"
)

// dialogPrompter asks a run's questions as dialogs over the window. A run asks from its own
// goroutine; each method shows its dialog on the UI thread and waits for the answer (or for
// the run to be stopped, which closes the dialog).
type dialogPrompter struct{ win fyne.Window }

// ask shows a dialog built by build on the UI thread and waits for send to be called, or for
// ctx to end (then the dialog is closed).
func ask[T any](ctx context.Context, p dialogPrompter, def T, build func(answer func(T)) dialog.Dialog) (T, error) {
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

func wrapped(s string) fyne.CanvasObject {
	l := widget.NewLabel(s)
	l.Wrapping = fyne.TextWrapWord
	return l
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
			content = container.NewVBox(wrapped(q.Title), wrapped(q.Detail))
		}
		d := dialog.NewCustomConfirm("", yes, no, content, answer, p.win)
		d.Resize(fyne.NewSize(420, d.MinSize().Height))
		return d
	})
}

func (p dialogPrompter) Select(ctx context.Context, title, detail string, options []core.Option, def string) (string, error) {
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.Label
	}
	pick := def
	got, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
		group := widget.NewRadioGroup(labels, func(s string) {
			for _, o := range options {
				if o.Label == s {
					pick = o.Value
				}
			}
		})
		for _, o := range options {
			if o.Value == def {
				group.SetSelected(o.Label)
			}
		}
		content := container.NewVBox(wrapped(title))
		if detail != "" {
			content.Add(wrapped(detail))
		}
		content.Add(group)
		d := dialog.NewCustomConfirm("", "Choose", "Cancel", content, answer, p.win)
		d.Resize(fyne.NewSize(460, d.MinSize().Height))
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
	entry := widget.NewEntry()
	entry.SetText(def)
	got, err := ask(ctx, p, false, func(answer func(bool)) dialog.Dialog {
		content := container.NewVBox(wrapped(title))
		if detail != "" {
			content.Add(wrapped(detail))
		}
		content.Add(entry)
		d := dialog.NewCustomConfirm("", "OK", "Cancel", content, answer, p.win)
		d.Resize(fyne.NewSize(460, d.MinSize().Height))
		return d
	})
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
