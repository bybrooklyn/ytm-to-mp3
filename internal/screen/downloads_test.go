package screen

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
)

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDownloadsRunProgressAndSummary(t *testing.T) {
	release := make(chan struct{})
	var got []string
	d := NewDownloads(func(ctx context.Context, inputs []string, sink jobs.Sink) (RunResult, error) {
		got = inputs
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "a", Label: "A - One", Source: "youtube"})
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "b", Label: "B - Two", Source: "youtube"})
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "c", Label: "C - Three", Source: "apple"})
		sink.Emit(jobs.Event{Kind: jobs.Started, ID: "a"})
		sink.Emit(jobs.Event{Kind: jobs.Progress, ID: "a", Bytes: 500, Total: 1000})
		<-release
		sink.Emit(jobs.Event{Kind: jobs.Done, ID: "a", Path: "/m/a.opus", Quality: "opus"})
		sink.Emit(jobs.Event{Kind: jobs.Failed, ID: "b", Reason: "connection reset", Class: jobs.ClassNetwork, Retryable: true})
		sink.Emit(jobs.Event{Kind: jobs.Upgraded, ID: "c", Path: "/m/c.m4a", Quality: "256 kbps AAC"})
		return RunResult{Held: 4, Results: []jobs.Result{
			{ID: "a", Label: "A - One", Kind: jobs.Done},
			{ID: "b", Label: "B - Two", Kind: jobs.Failed, Reason: "connection reset", Retryable: true},
			{ID: "c", Label: "C - Three", Kind: jobs.Upgraded},
		}}, nil
	}, func() DownloadSettings { return DownloadSettings{Folder: "~/Music", Format: "opus"} })
	var pings atomic.Int32
	d.OnChange = func() { pings.Add(1) }

	if err := d.Start(); !errors.Is(err, ErrNothingToDo) {
		t.Fatalf("empty input: %v", err)
	}
	d.SetInput("https://youtu.be/aaaaaaaaaaa")
	d.AddInput("/home/me/library.csv")
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(); err == nil {
		t.Error("a second start while running should be refused")
	}
	waitFor(t, "progress", func() bool {
		v := d.Snapshot()
		return v.Counts.Total == 3 && v.Counts.Working == 1
	})
	v := d.Snapshot()
	if v.State != "running" || len(v.Groups) != 2 || v.Groups[0].Label != "YouTube" || v.Groups[1].Label != "Apple Music" {
		t.Fatalf("view %+v", v)
	}
	if first := v.Groups[0].Items[0]; first.ID != "a" || first.Bytes != 500 || first.Total != 1000 || first.State != DLWorking {
		t.Errorf("working song should list first with its bytes: %+v", first)
	}
	if v.Settings.Folder != "~/Music" || v.Input == "" {
		t.Errorf("settings/input: %+v", v)
	}
	close(release)
	waitFor(t, "end", func() bool { return d.Snapshot().State == "done" })
	v = d.Snapshot()
	s := v.Summary
	if s == nil || s.Downloaded != 2 || s.Upgraded != 1 || s.Held != 4 || s.Failed != 1 || s.Retryable != 1 {
		t.Fatalf("summary %+v", s)
	}
	if len(s.Failures) != 1 || s.Failures[0].Reason != "connection reset" || s.Failures[0].Songs[0] != "B - Two" {
		t.Errorf("failures %+v", s.Failures)
	}
	if len(got) != 1 || got[0] != "https://youtu.be/aaaaaaaaaaa\n/home/me/library.csv" {
		t.Errorf("inputs %q", got)
	}
	if pings.Load() < 3 {
		t.Errorf("the window was told only %d times", pings.Load())
	}
	// Retry runs the same inputs again with a clean queue
	release2 := make(chan struct{})
	d2got := 0
	d.run = func(ctx context.Context, inputs []string, sink jobs.Sink) (RunResult, error) {
		d2got = len(inputs)
		<-release2
		return RunResult{}, nil
	}
	if err := d.RetryFailed(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "retry running", func() bool { return d.Snapshot().State == "running" && d.Snapshot().Counts.Total == 0 })
	close(release2)
	waitFor(t, "retry done", func() bool { return d.Snapshot().State == "done" })
	if d2got != 1 {
		t.Errorf("retry got %d inputs", d2got)
	}
}

func TestDownloadsStop(t *testing.T) {
	d := NewDownloads(func(ctx context.Context, inputs []string, sink jobs.Sink) (RunResult, error) {
		sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "a", Label: "A", Source: "youtube"})
		sink.Emit(jobs.Event{Kind: jobs.Progress, ID: "a", Bytes: 1, Total: 10})
		<-ctx.Done()
		return RunResult{Results: []jobs.Result{{ID: "a", Kind: jobs.Queued}}}, ctx.Err()
	}, nil)
	d.SetInput("x")
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { return d.Snapshot().Counts.Working == 1 })
	d.Stop()
	if st := d.Snapshot().State; st != "stopping" && st != "stopped" {
		t.Errorf("state after stop: %q", st)
	}
	waitFor(t, "stopped", func() bool { return d.Snapshot().State == "stopped" })
	v := d.Snapshot()
	if v.Error != "" || v.Summary == nil || v.Summary.Unfinished != 1 {
		t.Errorf("a stop is not an error: %+v", v)
	}
	if err := d.Resume(); err != nil {
		t.Errorf("resume: %v", err)
	}
	d.Stop()
	waitFor(t, "stopped again", func() bool { return d.Snapshot().State == "stopped" })
}

func TestDownloadsFailureOfTheWholeRunIsShown(t *testing.T) {
	d := NewDownloads(func(context.Context, []string, jobs.Sink) (RunResult, error) {
		return RunResult{}, errors.New("nothing to download")
	}, nil)
	d.SetInput("x")
	_ = d.Start()
	waitFor(t, "end", func() bool { return d.Snapshot().State == "done" })
	if d.Snapshot().Error != "nothing to download" {
		t.Errorf("error %q", d.Snapshot().Error)
	}
}

func TestDownloadsListIsBounded(t *testing.T) {
	d := NewDownloads(nil, nil)
	for i := 0; i < 500; i++ {
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		d.Emit(jobs.Event{Kind: jobs.Queued, ID: id, Label: id, Source: "youtube"})
	}
	d.Emit(jobs.Event{Kind: jobs.Progress, ID: "Aa", Bytes: 1, Total: 2})
	v := d.Snapshot()
	if len(v.Groups[0].Items) != maxListed || v.Hidden != 500-maxListed || v.Counts.Total != 500 {
		t.Fatalf("listed %d hidden %d total %d", len(v.Groups[0].Items), v.Hidden, v.Counts.Total)
	}
}

func TestDownloadsAFailureNeverUndoesASuccess(t *testing.T) {
	d := NewDownloads(nil, nil)
	d.Emit(jobs.Event{Kind: jobs.Done, ID: "a", Label: "A", Source: "youtube"})
	d.Emit(jobs.Event{Kind: jobs.Failed, ID: "a", Reason: "second source gave up"})
	if it := d.Snapshot().Groups[0].Items[0]; it.State != DLDone || it.Reason != "" {
		t.Errorf("%+v", it)
	}
}

func TestDownloadsIsNotShownInTheTerminal(t *testing.T) {
	d := NewDownloads(nil, nil)
	s := d.Screen()
	if s.Downloads == nil || len(s.Rows) != 0 || d.Editing() {
		t.Errorf("%+v", s)
	}
}
