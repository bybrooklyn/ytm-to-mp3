package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		{context.Canceled, ClassCancelled},
		{&StatusError{Status: 401}, ClassAuth},
		{&StatusError{Status: 429}, ClassThrottle},
		{&StatusError{Status: 403, Msg: "forbidden"}, ClassThrottle},
		{&StatusError{Status: 404}, ClassUnavailable},
		{&StatusError{Status: 503}, ClassNetwork},
		{fmt.Errorf("wrapped: %w", io.ErrUnexpectedEOF), ClassNetwork},
		{&CorruptError{Msg: "x"}, ClassCorrupt},
		{errors.New("write /x: no space left on device"), ClassDisk},
		{errors.New("Sign in again, token expired"), ClassAuth},
		{errors.New("something odd"), ClassOther},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.err, got, c.want)
		}
	}
	if Human(&StatusError{Status: 429}) == "" {
		t.Error("no human text")
	}
}

func TestBreakerOpensOnceOnConsecutiveSourceFailures(t *testing.T) {
	b := NewBreaker(3)
	throttle := &StatusError{Status: 429}
	if b.Record(throttle) || b.Record(errors.New("not found")) || b.Record(throttle) {
		t.Fatal("opened too early (a song-specific failure must not count)")
	}
	if !b.Record(throttle) || !b.Open() {
		t.Fatal("should open on the third source-wide failure in a row")
	}
	if b.Record(throttle) {
		t.Error("the opening is reported once")
	}
	b2 := NewBreaker(2)
	b2.Record(throttle)
	b2.Record(nil)
	if b2.Record(throttle) {
		t.Error("a success resets the streak")
	}
}

func TestLedger(t *testing.T) {
	var l Ledger
	s := Multi{&l, nil}
	s.Emit(Event{Kind: Queued, ID: "a", Label: "A"})
	s.Emit(Event{Kind: Failed, ID: "a", Source: "tidal", Reason: "x", Retryable: true})
	s.Emit(Event{Kind: Done, ID: "a", Source: "youtube", Path: "/m/a"})
	s.Emit(Event{Kind: Failed, ID: "a", Source: "apple"}) // late failure must not undo the success
	s.Emit(Event{Kind: Failed, ID: "b", Label: "B", Retryable: true})
	s.Emit(Event{Kind: Skipped, ID: "c"})
	s.Emit(Event{Kind: Upgraded, ID: "d", Path: "/m/d"})
	s.Emit(Event{Kind: Progress, ID: "e"}) // not a result
	l.NoteHeld(2)
	c := l.Counts()
	if c.Done != 1 || c.Failed != 1 || c.Retryable != 1 || c.Skipped != 1 || c.Upgraded != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if l.Held() != 3 {
		t.Errorf("held = %d", l.Held())
	}
	if f := l.Files(); len(f) != 2 {
		t.Errorf("files = %v", f)
	}
}

func TestLedgerConcurrent(t *testing.T) {
	var l Ledger
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.Emit(Event{Kind: Done, ID: fmt.Sprint(i % 10)}); _ = l.Counts() }()
	}
	wg.Wait()
	if l.Counts().Done != 10 {
		t.Errorf("done = %d", l.Counts().Done)
	}
}

func TestRetry(t *testing.T) {
	n := 0
	err := Retry(context.Background(), 4, time.Millisecond, func() error {
		n++
		if n < 3 {
			return &StatusError{Status: 503}
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v n=%d", err, n)
	}
	n = 0
	err = Retry(context.Background(), 4, time.Millisecond, func() error { n++; return &StatusError{Status: 404} })
	if err == nil || n != 1 {
		t.Errorf("a permanent failure must not be retried: n=%d", n)
	}
}
