package screen

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
)

func TestMenuRowsPressAndKeys(t *testing.T) {
	var pressed []string
	m := NewMenu("library", "", LibraryItems(), func(id string) { pressed = append(pressed, id) })
	sc := m.Screen()
	if sc.Title != "library" || len(sc.Rows) != 5 {
		t.Fatalf("screen = %+v", sc)
	}
	for _, r := range sc.Rows {
		if r.Kind != KindAction || r.Button == "" || r.Help == "" || r.Label == "" {
			t.Errorf("every row is a labelled button with its explanation: %+v", r)
		}
	}
	groups := 0
	for _, r := range sc.Rows {
		if r.Group != "" {
			groups++
		}
	}
	if groups != 3 || sc.Rows[0].Group != "All at once" || sc.Rows[4].Group != "Apple Music" {
		t.Errorf("sections: %+v", sc.Rows)
	}
	m.Key("down")
	m.Key("enter")
	if len(pressed) != 1 || pressed[0] != LibYouTube {
		t.Fatalf("pressed %v", pressed)
	}
	m.SelectIndex(4)
	m.Key("space")
	if pressed[1] != LibAppleMatch {
		t.Fatalf("pressed %v", pressed)
	}
	m.Key("up")
	if !m.Screen().Rows[3].Selected {
		t.Error("up moves the cursor")
	}
	// the host's own click path: Set{Press} -> keys
	h := NewHost(&Tab{Name: "library", Keep: true, Make: func() Model { return m }})
	h.Do(Event{Set: &Set{Row: 2, Press: true}})
	if pressed[len(pressed)-1] != LibBandcamp {
		t.Errorf("pressed %v", pressed)
	}
}

func TestMenuTaskLogStatesAndCancel(t *testing.T) {
	release := make(chan struct{})
	var changes atomic.Int32
	m := NewMenu("tools", "", ToolItems(), nil)
	m.OnChange = func() { changes.Add(1) }
	if err := m.StartTask("Check that everything works", func(ctx context.Context, log io.Writer) error {
		io.WriteString(log, "  ✓ yt-dlp\n")
		<-release
		io.WriteString(log, "  ✗ network\n")
		return errors.New("1 problem(s) found")
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "log line", func() bool { return strings.Contains(taskOf(m).Log, "yt-dlp") })
	if tv := taskOf(m); tv.State != "running" || tv.Title != "Check that everything works" {
		t.Fatalf("task = %+v", tv)
	}
	if !m.Running() || !m.Screen().Rows[0].Dim {
		t.Error("while a task runs the buttons are dimmed")
	}
	if err := m.StartTask("another", func(context.Context, io.Writer) error { return nil }); err == nil {
		t.Error("one task at a time")
	}
	m.Key("enter") // pressing while busy does nothing (and the press func is nil here anyway)
	close(release)
	waitFor(t, "failed", func() bool { return taskOf(m).State == "failed" })
	if tv := taskOf(m); tv.Error != "1 problem(s) found" || !strings.Contains(tv.Log, "network") {
		t.Fatalf("task = %+v", tv)
	}
	if changes.Load() < 3 {
		t.Errorf("the window should hear about the start, the log and the end: %d", changes.Load())
	}
	m.Dismiss()
	if m.Screen().Task != nil {
		t.Error("dismissed")
	}

	// cancel ends the run's context; the page says cancelled, not failed
	started := make(chan struct{})
	_ = m.StartTask("Update", func(ctx context.Context, log io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	m.Cancel()
	waitFor(t, "cancelled", func() bool { return taskOf(m).State == "cancelled" })
	if taskOf(m).Error != "" {
		t.Error("a cancelled run is not a failure")
	}

	// backing out of its first question leaves nothing behind
	_ = m.StartTask("Fix tags", func(context.Context, io.Writer) error { return ErrBackedOut })
	waitFor(t, "cleared", func() bool { return m.Screen().Task == nil })
}

func taskOf(m *Menu) TaskView {
	if t := m.Screen().Task; t != nil {
		return *t
	}
	return TaskView{}
}

func TestStartFlowQueueSummaryBackAndResume(t *testing.T) {
	release := make(chan struct{})
	var runs atomic.Int32
	d := NewDownloads(nil, func() DownloadSettings { return DownloadSettings{} })
	flow := func(ctx context.Context, sink jobs.Sink) (RunResult, error) {
		runs.Add(1)
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "a", Label: "A - One", Source: "youtube"})
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "b", Label: "B - Two", Source: "youtube"})
		<-release
		sink.Emit(jobs.Event{Kind: jobs.Done, ID: "a", Path: "/m/a.opus", Quality: "opus"})
		// the YouTube run learns afterwards that the song replaced a worse copy
		sink.Emit(jobs.Event{Kind: jobs.Upgraded, ID: "a", Path: "/m/a.opus", Quality: "opus"})
		sink.Emit(jobs.Event{Kind: jobs.Failed, ID: "b", Reason: "Video unavailable"})
		return RunResult{}, nil // a flow that reports only through events
	}
	if err := d.StartFlow("Reading your YouTube Music library", flow); err != nil {
		t.Fatal(err)
	}
	if v := d.Snapshot(); v.Task != "Reading your YouTube Music library" || v.State != "running" {
		t.Fatalf("view %+v", v)
	}
	if err := d.StartFlow("again", flow); err == nil {
		t.Error("a second start while running is refused")
	}
	close(release)
	waitFor(t, "done", func() bool { return d.Snapshot().State == "done" })
	v := d.Snapshot()
	if v.Summary == nil || v.Summary.Downloaded != 1 || v.Summary.Upgraded != 1 || v.Summary.Failed != 1 {
		t.Fatalf("a flow with no ledger still gets a summary from its events: %+v", v.Summary)
	}
	for _, x := range v.Groups[0].Items {
		if x.ID == "a" && x.State != DLUpgraded {
			t.Errorf("an upgrade shows as Upgraded in the queue: %+v", x)
		}
	}

	// Resume runs the same flow again
	if err := d.Resume(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second run", func() bool { return runs.Load() == 2 && d.Snapshot().State == "done" })

	// Back at the first question: no error, no summary, back to idle
	back := NewDownloads(nil, nil)
	_ = back.StartFlow("x", func(context.Context, jobs.Sink) (RunResult, error) { return RunResult{}, ErrBackedOut })
	waitFor(t, "idle", func() bool { return back.Snapshot().State == "" })
	if s := back.Snapshot(); s.Error != "" || s.Summary != nil || s.Task != "" {
		t.Errorf("snapshot after backing out: %+v", s)
	}
}
