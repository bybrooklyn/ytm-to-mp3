package ui

import "github.com/bybrooklyn/mfetchy/internal/jobs"

// Sink returns a jobs.Sink that shows events the way ytm always has: a finished song is a
// "✓ Artist - Title · detail" line, a failed one "✗ Artist - Title · reason". Other events
// (queued, progress, skipped, ...) are counted by the run summary rather than printed one
// by one, so they print nothing here.
func (u *UI) Sink() jobs.Sink { return uiSink{u} }

type uiSink struct{ u *UI }

func (s uiSink) Emit(e jobs.Event) {
	switch e.Kind {
	case jobs.Done, jobs.Upgraded:
		if e.Quality != "" {
			s.u.Good("%s · %s", e.Label, e.Quality)
		} else {
			s.u.Good("%s", e.Label)
		}
	case jobs.Failed:
		s.u.Bad("%s · %s", e.Label, e.Reason)
	}
}
