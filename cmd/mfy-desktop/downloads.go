//go:build cgo

package main

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

// attachDownloads connects the Downloads page: the model it shows and acts on, the text
// area, and dropped files. The page redraws when the model says it changed.
func (v *view) attachDownloads(dl *screen.Downloads) {
	v.dl = dl
	v.entry = widget.NewMultiLineEntry()
	v.entry.Wrapping = fyne.TextWrapOff
	v.entry.SetMinRowsVisible(5)
	v.entry.SetPlaceHolder("Paste links here, one per line: YouTube, Apple Music, Spotify, Tidal, SoundCloud or Bandcamp.\nA Takeout .zip, a .csv or a .txt list works too: drop it on this window, or use Browse.")
	v.entry.OnChanged = dl.SetInput
	dl.OnChange = func() {
		uiPost(func() {
			if v.host.View().Screen.Downloads != nil {
				v.draw(v.host.View())
			}
		})
	}
	v.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		for _, u := range uris {
			dl.AddInput(u.Path())
		}
		v.draw(v.host.View())
	})
	v.draw(v.host.View())
}

// ---- pieces ---------------------------------------------------------------------------

// primary is the one filled button of a section: the action the page is for.
func primary(label string, disabled bool, onTap func()) fyne.CanvasObject {
	if disabled {
		return button(label, true, func() {})
	}
	fill := pal(theme.ColorNamePrimary)
	body := container.NewStack(padded(container.NewCenter(txt(label, colOnAccent, 14, true)), sp8, sp16))
	c := newClick(body, fill, fill, true, onTap)
	return c
}

// link is a text in the accent colour that acts like a button.
func link(label string, onTap func()) fyne.CanvasObject {
	return newClick(padded(txt(label, theme.ColorNamePrimary, 13, true), sp4, sp4), color.Transparent, pal(colHover), true, onTap)
}

// barLayout lays a fill over a track, the fill a fraction of the width.
type barLayout struct{ frac float32 }

func (l barLayout) Layout(o []fyne.CanvasObject, s fyne.Size) {
	o[0].Move(fyne.NewPos(0, 0))
	o[0].Resize(s)
	o[1].Move(fyne.NewPos(0, 0))
	o[1].Resize(fyne.NewSize(s.Width*min(1, max(0, l.frac)), s.Height))
}
func (barLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(96, 6) }

// bar is a flat progress bar: a track and an accent fill, no gradient.
func bar(frac float32) fyne.CanvasObject {
	track, fill := rect(colBorder), rect(theme.ColorNamePrimary)
	track.CornerRadius, fill.CornerRadius = 3, 3
	return container.New(barLayout{frac}, track, fill)
}

// statusWord is a dot and a word in the colour of the state: never colour alone.
func statusWord(word string, dot fyne.ThemeColorName, hollow bool) fyne.CanvasObject {
	c := canvas.NewCircle(pal(dot))
	text := fyne.ThemeColorName(theme.ColorNameForeground)
	if hollow {
		c.FillColor = color.Transparent
		c.StrokeColor, c.StrokeWidth = pal(colMuted), 1.5
		text = colMuted
	}
	return container.New(layout.NewCustomPaddedHBoxLayout(sp8), container.NewCenter(sized(8, 8, c)), txt(word, text, 13, false))
}

func humanBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.0f KB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// ---- the page -------------------------------------------------------------------------

// drawDownloads fills the scrolling area with the Downloads page.
func (v *view) drawDownloads(d screen.DownloadsView) {
	if v.entry.Text != d.Input {
		v.entry.SetText(d.Input)
	}
	running := d.State == "running" || d.State == "stopping"
	var secs []fyne.CanvasObject

	// Add music
	browse := button("Browse...", running, func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err == nil && r != nil {
				v.dl.AddInput(r.URI().Path())
				_ = r.Close()
			}
		}, v.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".zip", ".csv", ".txt"}))
		fd.Show()
	})
	start := primary("Start", running, func() {
		if err := v.dl.Start(); err != nil {
			dialog.ShowInformation("", capital(err.Error())+".", v.win)
		}
	})
	where := fmt.Sprintf("Saving to %s as %s.", d.Settings.Folder, d.Settings.Format)
	if d.Settings.Extras != "" {
		where += " " + capital(strings.TrimSuffix(d.Settings.Extras, "  (change in settings)")) + "."
	}
	settings := link("Change in Settings", func() { v.openTab("settings") })
	add := container.New(layout.NewCustomPaddedVBoxLayout(sp12),
		v.entry,
		container.NewBorder(nil, nil, browse, start, gap(1)),
		container.New(layout.NewCustomPaddedVBoxLayout(sp4), para(where, colMuted, theme.SizeNameCaptionText), settings),
	)
	secs = append(secs, v.section("Add music", []fyne.CanvasObject{padded(add, sp12, sp16)}))

	if d.Error != "" {
		secs = append(secs, notice(d.Error))
	}

	// The summary comes first once a run is over: it is what you came to read. The queue
	// that produced it stays below.
	if d.Summary != nil {
		secs = append(secs, v.summarySection(d))
	}
	if d.Counts.Total > 0 || running {
		secs = append(secs, v.queueSection(d, running))
	}

	page := container.New(layout.NewCustomPaddedVBoxLayout(sp24), secs...)
	v.scroll.Content = container.New(column{contentMax}, container.NewVBox(page, gap(sp16)))
	v.scroll.Refresh()
}

// openTab switches to the tab with this name.
func (v *view) openTab(name string) {
	for i, t := range v.host.View().Tabs {
		if t == name {
			v.send(screen.Event{Tab: &i})
			return
		}
	}
}

func (v *view) queueSection(d screen.DownloadsView, running bool) fyne.CanvasObject {
	c := d.Counts
	title, sub := "Downloading", fmt.Sprintf("%d of %d", c.Finished(), c.Total)
	switch d.State {
	case "stopping":
		title = "Stopping"
	case "stopped":
		title = "Stopped"
	case "done":
		title = "Finished"
	}
	if c.Total == 0 && d.Task != "" && d.State == "running" {
		title, sub = d.Task, "" // an import that has not found songs yet
	}
	var bits []string
	if c.Working > 0 {
		bits = append(bits, fmt.Sprintf("%d working", c.Working))
	}
	if n := c.Done + c.Upgraded; n > 0 {
		bits = append(bits, fmt.Sprintf("%d done", n))
	}
	if c.Skipped > 0 {
		bits = append(bits, fmt.Sprintf("%d already held", c.Skipped))
	}
	if c.Failed > 0 {
		bits = append(bits, fmt.Sprintf("%d failed", c.Failed))
	}
	if running && d.Speed > 0 {
		bits = append(bits, humanBytes(int64(d.Speed))+"/s")
	}
	if d.Elapsed > 0 {
		bits = append(bits, humanDuration(d.Elapsed))
	}
	left := container.New(layout.NewCustomPaddedVBoxLayout(sp4),
		container.New(layout.NewCustomPaddedHBoxLayout(sp8), txt(title, theme.ColorNameForeground, 14, true), txt(sub, colMuted, 14, false)),
		para(strings.Join(bits, "  ·  "), colMuted, theme.SizeNameCaptionText))
	var act fyne.CanvasObject = gap(0)
	switch d.State {
	case "running":
		act = button("Stop", false, v.dl.Stop)
	case "stopping":
		act = button("Stopping...", true, func() {})
	case "stopped":
		act = button("Resume", false, func() { _ = v.dl.Resume() })
	}
	var frac float32
	if c.Total > 0 {
		part := 0.0
		for _, g := range d.Groups {
			for _, it := range g.Items {
				if it.State == screen.DLWorking && it.Total > 0 {
					part += float64(it.Bytes) / float64(it.Total)
				}
			}
		}
		frac = float32((float64(c.Finished()) + part*0.9) / float64(c.Total))
	}
	rows := []fyne.CanvasObject{
		padded(container.New(layout.NewCustomPaddedVBoxLayout(sp12),
			container.NewBorder(nil, nil, nil, container.NewCenter(act), left), bar(frac)), sp12, sp16),
	}
	for _, g := range d.Groups {
		rows = append(rows, v.groupHead(g))
		for _, it := range g.Items {
			rows = append(rows, v.songRow(it))
		}
	}
	if d.Hidden > 0 {
		rows = append(rows, padded(para(fmt.Sprintf("%d more songs are not listed. They are counted above and kept in the library as they finish.", d.Hidden),
			colMuted, theme.SizeNameCaptionText), sp8, sp16))
	}
	return v.section("Queue", rows)
}

func (v *view) groupHead(g screen.DLGroup) fyne.CanvasObject {
	c := g.Counts
	right := fmt.Sprintf("%d of %d", c.Finished(), c.Total)
	row := container.NewBorder(nil, nil, nil, txt(right, colMuted, 13, false), txt(g.Label, colMuted, 13, true))
	return container.NewStack(rect(colHover), padded(row, sp8, sp16))
}

func (v *view) songRow(it screen.DLItem) fyne.CanvasObject {
	name := fyne.ThemeColorName(theme.ColorNameForeground)
	label := it.Label
	if label == "" {
		label = it.ID
	}
	var caption string
	var right fyne.CanvasObject
	switch it.State {
	case screen.DLWorking:
		stage := map[string]string{"resolving": "Looking it up", "downloading": "Downloading", "tagging": "Adding tags and cover"}[it.Stage]
		if stage == "" {
			stage = "Working"
		}
		caption = stage
		if it.Stage == "downloading" && it.Total > 0 {
			caption = fmt.Sprintf("Downloading  %s of %s", humanBytes(it.Bytes), humanBytes(it.Total))
			right = container.New(layout.NewCustomPaddedHBoxLayout(sp8), container.NewCenter(sized(96, 6, bar(float32(it.Bytes)/float32(it.Total)))),
				container.NewCenter(sized(36, 18, container.NewCenter(txt(fmt.Sprintf("%d%%", int(100*it.Bytes/it.Total)), theme.ColorNameForeground, 13, true)))))
		} else {
			right = statusWord(stage, theme.ColorNamePrimary, false)
			caption = ""
		}
	case screen.DLDone, screen.DLUpgraded:
		word := "Done"
		if it.State == screen.DLUpgraded {
			word = "Upgraded"
		}
		caption = it.Quality
		right = statusWord(word, colSuccess, false)
	case screen.DLSkipped:
		name = colMuted
		caption = it.Reason
		right = statusWord("Already held", colMuted, true)
	case screen.DLFailed:
		caption = it.Reason
		right = statusWord("Failed", colProblem, false)
	default:
		name = colMuted
		right = statusWord("Waiting", colMuted, true)
	}
	left := container.New(layout.NewCustomPaddedVBoxLayout(sp4), truncated(label, name))
	if caption != "" {
		c := fyne.ThemeColorName(colMuted)
		if it.State == screen.DLFailed {
			c = colProblem
		}
		left.Add(para(caption, c, theme.SizeNameCaptionText))
	}
	return padded(container.NewBorder(nil, nil, nil, container.NewCenter(right), left), sp8, sp16)
}

// truncated is one line of text that ends in an ellipsis instead of widening the window.
func truncated(s string, c fyne.ThemeColorName) fyne.CanvasObject {
	rt := widget.NewRichText(&widget.TextSegment{Text: s, Style: widget.RichTextStyle{ColorName: c, SizeName: theme.SizeNameText}})
	rt.Truncation = fyne.TextTruncateEllipsis
	in := theme.InnerPadding()
	return container.New(layout.NewCustomPaddedLayout(-in, -in, -in, -in), rt)
}

func (v *view) summarySection(d screen.DownloadsView) fyne.CanvasObject {
	s := d.Summary
	row := func(label, value string, c fyne.ThemeColorName) fyne.CanvasObject {
		return padded(container.NewBorder(nil, nil, nil, txt(value, c, 14, true), txt(label, theme.ColorNameForeground, 14, false)), sp12, sp16)
	}
	fg := fyne.ThemeColorName(theme.ColorNameForeground)
	rows := []fyne.CanvasObject{
		row("Downloaded", fmt.Sprint(s.Downloaded), fg),
	}
	if s.Upgraded > 0 {
		rows = append(rows, row("Upgraded to a better copy", fmt.Sprint(s.Upgraded), fg))
	}
	rows = append(rows, row("Already in your library", fmt.Sprint(s.Held), fg))
	if s.Failed > 0 {
		rows = append(rows, row("Failed", fmt.Sprint(s.Failed), colProblem))
	}
	if s.Unfinished > 0 {
		rows = append(rows, row("Not started or not finished", fmt.Sprint(s.Unfinished), fg))
	}
	for _, f := range s.Failures {
		list := container.New(layout.NewCustomPaddedVBoxLayout(sp4),
			txt(fmt.Sprintf("%s  (%d)", f.Reason, len(f.Songs)), colProblem, 13, true))
		for i, song := range f.Songs {
			if i == 6 {
				list.Add(txt(fmt.Sprintf("and %d more", len(f.Songs)-i), colMuted, 12, false))
				break
			}
			list.Add(txt(song, colMuted, 12, false))
		}
		rows = append(rows, padded(list, sp12, sp16))
	}
	if s.Retryable > 0 && d.State == "done" {
		rows = append(rows, padded(container.NewBorder(nil, nil,
			para(fmt.Sprintf("%s may work on another try.", plural(s.Retryable, "song")), colMuted, theme.SizeNameCaptionText),
			container.NewCenter(button("Retry failed", false, func() { _ = v.dl.RetryFailed() })), gap(1)), sp12, sp16))
	}
	heading := "Summary"
	if d.State == "stopped" {
		heading = "Stopped early"
		rows = append(rows, padded(para("What finished is kept. Resume carries on where it left off.", colMuted, theme.SizeNameCaptionText), sp12, sp16))
	}
	return v.section(heading, rows)
}
