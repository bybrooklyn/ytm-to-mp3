//go:build cgo

package main

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

// maxLogLines is how much of a tool's output the page keeps on screen.
const maxLogLines = 400

// taskSection is the run a tool started: what it is, whether it is going, the output it
// printed and, while it runs, a Cancel button.
func (v *view) taskSection(t screen.TaskView) fyne.CanvasObject {
	var word fyne.CanvasObject
	switch t.State {
	case "running":
		word = statusWord("Running", theme.ColorNamePrimary, false)
	case "done":
		word = statusWord("Finished", colSuccess, false)
	case "failed":
		word = statusWord("Stopped with a problem", colProblem, false)
	default:
		word = statusWord("Cancelled", colMuted, true)
	}
	act := button("Dismiss", false, func() { v.tools.Dismiss() })
	if t.State == "running" {
		act = button("Cancel", false, v.tools.Cancel)
	}
	left := container.New(layout.NewCustomPaddedVBoxLayout(sp4), txt(t.Title, theme.ColorNameForeground, 14, true), word)
	head := container.NewBorder(nil, nil, nil, container.NewCenter(act), left)

	rows := []fyne.CanvasObject{padded(head, sp12, sp16)}
	if t.State == "running" {
		rows = append(rows, padded(widget.NewProgressBarInfinite(), sp8, sp16))
	}
	rows = append(rows, padded(logView(t.Log), sp12, sp16))
	if t.Error != "" {
		rows = append(rows, padded(para(capital(t.Error), colProblem, theme.SizeNameCaptionText), sp8, sp16))
	}
	return v.section("Output", rows)
}

// logView is the printed output in monospace, scrolled to its end.
func logView(log string) fyne.CanvasObject {
	log = strings.Trim(log, "\n")
	if log == "" {
		return para("Nothing has been printed yet.", colMuted, theme.SizeNameCaptionText)
	}
	lines := strings.Split(log, "\n")
	if len(lines) > maxLogLines {
		lines = append([]string{"..."}, lines[len(lines)-maxLogLines:]...)
	}
	l := widget.NewLabelWithStyle(strings.Join(lines, "\n"), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	sc := container.NewScroll(l)
	sc.SetMinSize(fyne.NewSize(0, 220))
	sc.ScrollToBottom()
	return sc
}
