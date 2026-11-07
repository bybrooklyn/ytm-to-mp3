package engine

import (
	"fmt"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/names"
)

// SourceName is the Source of every event the engine emits.
const SourceName = "youtube"

// emit sends an event to Options.Sink, if there is one. It is what a window shows live; the
// terminal keeps drawing from the live display, so nothing changes there.
func (e *engine) emit(ev jobs.Event) {
	if e.Sink == nil {
		return
	}
	ev.Source = SourceName
	jobs.Emit(e.Sink, ev)
}

// stageName is the jobs vocabulary for a live-display stage; "" means no event.
func stageName(s int32) string {
	switch s {
	case stResolve, stMusic:
		return "resolving"
	case stFetch:
		return "downloading"
	case stEncode:
		return "tagging"
	}
	return ""
}

// setStage moves a track to a new stage and reports it.
func (e *engine) setStage(ts *tstate, s int32) {
	if ts == nil {
		return
	}
	old := ts.stage.Swap(s)
	if old == s {
		return
	}
	if name := stageName(s); name != "" && name != stageName(old) {
		e.emit(jobs.Event{Kind: jobs.Stage, ID: ts.id, Label: ts.label, Stage: name})
	}
}

// emitProgress reports, for every track being downloaded, how many bytes have arrived since
// the last call. It runs on the display's tick, so a song is reported at most four times a
// second however fast its bytes come.
func (e *engine) emitProgress() {
	if e.Sink == nil {
		return
	}
	for _, ts := range e.live.snapshot() {
		if ts.stage.Load() != stFetch {
			continue
		}
		got := ts.got.Load()
		if got == ts.sent.Swap(got) {
			continue
		}
		e.emit(jobs.Event{Kind: jobs.Progress, ID: ts.id, Label: ts.label, Bytes: got, Total: ts.total.Load()})
	}
}

// emitDone reports a song that is in the library.
func (e *engine) emitDone(t model.Track, label, path, quality string) {
	e.emit(jobs.Event{Kind: jobs.Done, ID: t.ID, Label: label, Path: path, Quality: quality})
}

// emitFailed reports a song that did not arrive.
func (e *engine) emitFailed(t model.Track, err error, perm bool) {
	reason := names.Clean(err.Error())
	class := jobs.Classify(fmt.Errorf("%s", reason))
	e.emit(jobs.Event{Kind: jobs.Failed, ID: t.ID, Label: t.Label(), Reason: reason,
		Class: class, Retryable: !perm && class.Retryable()})
}
