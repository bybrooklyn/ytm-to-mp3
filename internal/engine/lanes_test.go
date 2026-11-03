package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

func testEngine(n int) *engine {
	e := &engine{Options: Options{UI: ui.New()}}
	for i := 0; i < n; i++ {
		e.lanes = append(e.lanes, &lane{exit: vpn.Exit{Name: string(rune('a' + i)), Proxy: "socks5h://x:y@127.0.0.1:1"},
			gov: NewGovernor(2, 4, 0)})
	}
	return e
}

func TestPickAvoidsCoolingAndPrevious(t *testing.T) {
	e := testEngine(3)
	e.lanes[0].gov.Observe(Throttled) // lane a is resting
	for i := 0; i < 20; i++ {
		if got := e.pick(e.lanes[1]); got != e.lanes[2] {
			t.Fatalf("picked %s, want c", got.exitName())
		}
	}
	// if everything else is busy the previous lane is still allowed
	e.lanes[2].gov.Observe(Throttled)
	if got := e.pick(e.lanes[1]); got == nil {
		t.Fatal("no lane")
	}
}

func TestSourSwapsAfterRepeatedTrouble(t *testing.T) {
	e := testEngine(2)
	calls := 0
	e.Replace = func(ctx context.Context, old vpn.Exit) (vpn.Exit, error) {
		calls++
		return vpn.Exit{Name: "new", Proxy: "socks5h://x:y@127.0.0.1:2"}, nil
	}
	l := e.lanes[0]
	for i := 0; i < 3; i++ {
		e.sour(context.Background(), l)
	}
	if calls != 0 {
		t.Fatal("swapped too early")
	}
	e.sour(context.Background(), l)
	if calls != 1 || l.exitName() != "new" || l.ytdlp().proxy != "socks5h://x:y@127.0.0.1:2" {
		t.Fatalf("calls=%d exit=%s", calls, l.exitName())
	}
	// failing replacement leaves the lane alone
	e.Replace = func(context.Context, vpn.Exit) (vpn.Exit, error) { return vpn.Exit{}, errors.New("none") }
	for i := 0; i < 4; i++ {
		e.sour(context.Background(), l)
	}
	if l.exitName() != "new" {
		t.Fatal("lane changed despite failed replace")
	}
}

func TestLooksLikeNetwork(t *testing.T) {
	if !looksLikeNetwork(errors.New("Unable to connect to proxy")) || looksLikeNetwork(errors.New("no audio formats")) {
		t.Fatal("classification wrong")
	}
	_ = time.Second
}
