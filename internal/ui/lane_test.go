package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
)

// Lanes stack their rows; clearing one leaves the others; with none left the block is gone.
func TestLanesStackAndClearIndependently(t *testing.T) {
	var buf bytes.Buffer
	u := &UI{out: &buf, tty: true}
	a, b := u.Lane("a"), u.Lane("b")
	a.Block([]string{"A1", "A2"})
	b.Block([]string{"B1"})
	if got := strings.Join(u.block, "|"); got != "A1|A2|B1" {
		t.Fatalf("block = %q", got)
	}
	a.Block([]string{"A1x"})
	if got := strings.Join(u.block, "|"); got != "A1x|B1" {
		t.Fatalf("after update: %q", got)
	}
	a.ClearStatus()
	if got := strings.Join(u.block, "|"); got != "B1" {
		t.Fatalf("after clearing a: %q", got)
	}
	b.ClearStatus()
	if len(u.block) != 0 {
		t.Fatalf("block should be gone: %q", u.block)
	}
}

// One lane behaves exactly like the plain UI did.
func TestSingleLaneMatchesPlainStatus(t *testing.T) {
	var plain, laned bytes.Buffer
	p := &UI{out: &plain, tty: true}
	l := &UI{out: &laned, tty: true}
	p.Block([]string{"row"})
	l.Lane("x").Block([]string{"row"})
	if plain.String() != laned.String() {
		t.Errorf("plain %q != laned %q", plain.String(), laned.String())
	}
}

func TestLanesConcurrent(t *testing.T) {
	var buf bytes.Buffer
	u := &UI{out: &buf, tty: true}
	var wg sync.WaitGroup
	for _, n := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := u.Lane(n)
			for i := 0; i < 100; i++ {
				l.Status("%s %d", n, i)
				l.Good("line")
			}
			l.ClearStatus()
		}()
	}
	wg.Wait()
	if len(u.block) != 0 {
		t.Errorf("block left: %q", u.block)
	}
}

// The terminal sink prints finished and failed songs the way ytm always has.
func TestSinkLines(t *testing.T) {
	var buf bytes.Buffer
	u := &UI{out: &buf}
	s := u.Sink()
	s.Emit(jobs.Event{Kind: jobs.Done, Label: "A - B", Quality: "Tidal FLAC"})
	s.Emit(jobs.Event{Kind: jobs.Failed, Label: "C - D", Reason: "nope"})
	s.Emit(jobs.Event{Kind: jobs.Progress, Label: "ignored"})
	s.Emit(jobs.Event{Kind: jobs.Skipped, Label: "ignored"})
	want := "  ✓ A - B · Tidal FLAC\n  ✗ C - D · nope\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}
