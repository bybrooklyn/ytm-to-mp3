package engine

import (
	"context"
	"testing"
	"time"
)

func TestAIMD(t *testing.T) {
	g := NewGovernor(4, 8, 0)
	for i := 0; i < 20*10; i++ {
		g.Observe(OK)
	}
	if g.Limit() != 8 {
		t.Fatalf("should grow to max, got %d", g.Limit())
	}
	g.Observe(Throttled)
	if g.Limit() != 4 {
		t.Fatalf("should halve, got %d", g.Limit())
	}
	if g.Cooling() <= 0 {
		t.Fatal("should be cooling down")
	}
	// the rest of a burst of 429s is the same episode: no further escalation
	for i := 0; i < 5; i++ {
		g.Observe(Throttled)
	}
	if g.Limit() != 4 {
		t.Fatalf("burst must not escalate, got %d", g.Limit())
	}
	// once the cooldown has passed, a new throttle halves again, down to the floor of 1
	now := time.Now()
	g.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		now = now.Add(time.Hour)
		g.Observe(Throttled)
	}
	if g.Limit() != 1 {
		t.Fatalf("floor is 1, got %d", g.Limit())
	}
	g.Reset(3)
	if g.Limit() != 3 || g.Cooling() != 0 {
		t.Fatalf("reset should clear backoff, limit %d cooling %v", g.Limit(), g.Cooling())
	}
}

func TestAcquireRespectsLimitAndContext(t *testing.T) {
	g := NewGovernor(1, 1, 0)
	ctx := context.Background()
	if err := g.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	c2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := g.Acquire(c2); err == nil {
		t.Fatal("second acquire should block until context ends")
	}
	g.Release(OK)
	if err := g.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
}
