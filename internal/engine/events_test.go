package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
)

type recorder struct {
	mu sync.Mutex
	ev []jobs.Event
}

func (r *recorder) Emit(e jobs.Event) { r.mu.Lock(); r.ev = append(r.ev, e); r.mu.Unlock() }

func (r *recorder) of(id string) []jobs.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []jobs.Event
	for _, e := range r.ev {
		if e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

// A run with a Sink reports each song as it moves: started, the stages in order, byte
// progress while it downloads, then done; and a song that cannot be had is failed with a
// reason. The fake yt-dlp prints progress lines for the download.
func TestRunEmitsEvents(t *testing.T) {
	o, _ := fakeOptions(t, exitN("a"))
	t.Setenv("FAKE_PROG", "1")
	t.Setenv("FAKE_GONE", tid(1))
	t.Setenv("MFY_RESOLVER", "yt-dlp")
	rec := &recorder{}
	o.Sink = rec
	sum := Run(context.Background(), o, tracks(2))
	if sum.Done != 1 || sum.Failed != 1 {
		t.Fatalf("done=%d failed=%d", sum.Done, sum.Failed)
	}

	ok := rec.of(tid(0))
	var kinds []string
	var stages []string
	var lastGot int64
	progress := 0
	for _, e := range ok {
		if e.Source != SourceName {
			t.Errorf("source %q on %+v", e.Source, e)
		}
		switch e.Kind {
		case jobs.Stage:
			stages = append(stages, e.Stage)
		case jobs.Progress:
			progress++
			if e.Bytes < lastGot {
				t.Errorf("progress went backwards: %d after %d", e.Bytes, lastGot)
			}
			lastGot = e.Bytes
			if e.Total != 4000 {
				t.Errorf("total %d, want 4000", e.Total)
			}
		}
		if e.Kind != jobs.Progress && e.Kind != jobs.Stage {
			kinds = append(kinds, string(e.Kind))
		}
	}
	if got, want := join(kinds), "started done"; got != want {
		t.Errorf("kinds %q, want %q (%+v)", got, want, ok)
	}
	if got, want := join(stages), "resolving downloading tagging"; got != want {
		t.Errorf("stages %q, want %q", got, want)
	}
	if progress == 0 {
		t.Errorf("no progress events: %+v", ok)
	}
	if last := ok[len(ok)-1]; last.Kind != jobs.Done || last.Path == "" {
		t.Errorf("last event should be done with a path: %+v", last)
	}

	bad := rec.of(tid(1))
	last := bad[len(bad)-1]
	if last.Kind != jobs.Failed || last.Reason == "" || last.Retryable {
		t.Errorf("an unavailable video should fail for good with a reason: %+v", last)
	}
}

// No Sink, no events and no trouble.
func TestRunWithoutSink(t *testing.T) {
	o, _ := fakeOptions(t, exitN("a"))
	if sum := Run(context.Background(), o, tracks(2)); sum.Done != 2 {
		t.Fatalf("done=%d", sum.Done)
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
