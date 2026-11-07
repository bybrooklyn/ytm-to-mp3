// Package jobs is the progress vocabulary of a download run, independent of any display.
//
// A run (a list of inputs, options, a context) reports what happens to each song as
// Events sent to a Sink. The terminal display, a desktop window and the end-of-run summary
// are all just Sinks. The API is small on purpose and meant to stay stable:
//
//	Event      one thing that happened to one song (queued, started, progress, ...)
//	Sink       Emit(Event); must be safe for concurrent use and must not block for long
//	Multi      fans one event out to several sinks
//	Ledger     a Sink that keeps the final state of every song (the run summary)
//	Classify   turns any error into a Class with a human message and a retry hint
//	Breaker    stops using a source that keeps failing the same way, with one message
package jobs

import (
	"fmt"
	"sync"
	"time"
)

// Kind is what happened.
type Kind string

const (
	Queued   Kind = "queued"   // the song is part of the run
	Started  Kind = "started"  // a source began working on it
	Progress Kind = "progress" // Bytes of Total received (Total 0 = unknown)
	Stage    Kind = "stage"    // a named step began: "resolving", "downloading", "tagging", "verifying"
	Done     Kind = "done"     // the song is in the library at Path
	Skipped  Kind = "skipped"  // nothing to do; Reason says why (already held, ...)
	Failed   Kind = "failed"   // it did not work; Reason is human text, Retryable says if trying again may help
	Upgraded Kind = "upgraded" // Done, and it replaced a worse copy
)

// Event is one thing that happened to one song. Zero fields are simply not set.
type Event struct {
	Kind   Kind
	ID     string // stable within a run; identifies the song across events
	Label  string // "Artist - Title", for people
	Source string // "youtube", "apple", "tidal", "soundcloud", "bandcamp"

	Bytes, Total int64  // Progress
	Stage        string // Stage
	Path         string // Done, Upgraded
	Quality      string // Done, Upgraded: "256 kbps AAC", "FLAC 16-bit", "opus 160k"

	Reason    string // Skipped, Failed
	Class     Class  // Failed: what kind of failure (see Classify)
	Retryable bool   // Failed: trying again later may work

	At time.Time
}

// Sink receives events. Implementations must be safe for concurrent use.
type Sink interface{ Emit(Event) }

// SinkFunc adapts a function to a Sink.
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }

// Discard drops every event.
var Discard Sink = SinkFunc(func(Event) {})

// Multi sends each event to every sink in order; nil sinks are skipped.
type Multi []Sink

func (m Multi) Emit(e Event) {
	for _, s := range m {
		if s != nil {
			s.Emit(e)
		}
	}
}

// Emit stamps the time and sends e to s (a nil s is fine).
func Emit(s Sink, e Event) {
	if s == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.Emit(e)
}

// Result is the final state of one song in a run.
type Result struct {
	ID, Label, Source string
	Kind              Kind // Done, Skipped, Failed or Upgraded
	Path, Quality     string
	Reason            string
	Class             Class
	Retryable         bool
}

// Ledger is a Sink that remembers the last terminal event (done, skipped, failed, upgraded)
// of every song, in the order songs were first seen. A song reported done after failing
// (retried from another source) counts as done.
type Ledger struct {
	mu    sync.Mutex
	order []string
	by    map[string]*Result
	anon  int
	held  int
}

func (l *Ledger) Emit(e Event) {
	if l == nil {
		return
	}
	switch e.Kind {
	case Queued, Done, Skipped, Failed, Upgraded:
	default:
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[string]*Result{}
	}
	id := e.ID
	if id == "" {
		l.anon++
		id = fmt.Sprintf("anon-%d", l.anon)
	}
	r, ok := l.by[id]
	if !ok {
		r = &Result{ID: id, Label: e.Label}
		l.by[id] = r
		l.order = append(l.order, id)
	}
	if e.Label != "" {
		r.Label = e.Label
	}
	if e.Source != "" {
		r.Source = e.Source
	}
	if e.Kind == Queued {
		return
	}
	// A later failure never overwrites a success (a second source failing after the first worked).
	if e.Kind == Failed && (r.Kind == Done || r.Kind == Upgraded) {
		return
	}
	r.Kind, r.Path, r.Quality = e.Kind, e.Path, e.Quality
	r.Reason, r.Class, r.Retryable = e.Reason, e.Class, e.Retryable
}

// Results returns every song's state, in first-seen order. Songs that were queued but never
// finished have Kind Queued.
func (l *Ledger) Results() []Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Result, 0, len(l.order))
	for _, id := range l.order {
		r := *l.by[id]
		if r.Kind == "" {
			r.Kind = Queued
		}
		out = append(out, r)
	}
	return out
}

// Counts is the tally of a run.
type Counts struct {
	Done, Upgraded, Skipped, Failed, Retryable, Unfinished int
}

// Counts tallies the results.
func (l *Ledger) Counts() Counts {
	var c Counts
	for _, r := range l.Results() {
		switch r.Kind {
		case Done:
			c.Done++
		case Upgraded:
			c.Upgraded++
		case Skipped:
			c.Skipped++
		case Failed:
			c.Failed++
			if r.Retryable {
				c.Retryable++
			}
		default:
			c.Unfinished++
		}
	}
	return c
}

// Files returns the paths of the songs that arrived (done or upgraded) in this run.
func (l *Ledger) Files() []string {
	var out []string
	for _, r := range l.Results() {
		if (r.Kind == Done || r.Kind == Upgraded) && r.Path != "" {
			out = append(out, r.Path)
		}
	}
	return out
}

// NoteHeld records n songs that were left alone because the library already holds them
// (planners know the count, not always the songs).
func (l *Ledger) NoteHeld(n int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.held += n
	l.mu.Unlock()
}

// Held is how many songs were left alone because the library already had them, counting
// both NoteHeld and Skipped events.
func (l *Ledger) Held() int {
	l.mu.Lock()
	n := l.held
	l.mu.Unlock()
	return n + l.Counts().Skipped
}
