package engine

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Outcome tells the governor how a YouTube request went.
type Outcome int

const (
	OK        Outcome = iota
	Throttled         // 429 / bot check / rate limit: back off hard
	Neutral           // failed for an unrelated reason: say nothing
)

// Governor adapts how hard we hit YouTube. It is AIMD: concurrency creeps up
// while requests succeed and halves on throttling, with a jittered cooldown
// that doubles on repeated throttling and decays after sustained success.
type Governor struct {
	mu       sync.Mutex
	cond     *sync.Cond
	limit    int
	min, max int
	inflight int
	streak   int
	cooldown time.Duration // current backoff step
	until    time.Time     // no new requests before this
	spacing  time.Duration // minimum gap between request starts
	last     time.Time
	baseGap  time.Duration
	now      func() time.Time
	rnd      *rand.Rand
}

// NewGovernor starts at start concurrent requests, never exceeding max.
func NewGovernor(start, max int, gap time.Duration) *Governor {
	if start > max {
		start = max
	}
	g := &Governor{limit: start, min: 1, max: max, spacing: gap, baseGap: gap,
		now: time.Now, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Limit reports the current concurrency target.
func (g *Governor) Limit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

// Acquire blocks until a request may start.
func (g *Governor) Acquire(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { g.mu.Lock(); g.cond.Broadcast(); g.mu.Unlock() })
	defer stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := g.now()
		wait := time.Duration(0)
		switch {
		case g.inflight >= g.limit:
			g.cond.Wait()
			continue
		case now.Before(g.until):
			wait = g.until.Sub(now)
		case now.Sub(g.last) < g.spacing:
			wait = g.spacing - now.Sub(g.last)
		}
		if wait > 0 {
			d := wait + time.Duration(g.rnd.Int63n(int64(wait)/4+1)) // g.rnd needs the lock
			g.mu.Unlock()
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
			}
			g.mu.Lock()
			continue
		}
		g.inflight++
		g.last = now
		return nil
	}
}

// Release reports a finished request.
func (g *Governor) Release(o Outcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	g.observeLocked(o)
	g.cond.Broadcast()
}

// Observe reports an outcome from a request that did not hold a slot (e.g. a download).
func (g *Governor) Observe(o Outcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observeLocked(o)
}

func (g *Governor) observeLocked(o Outcome) {
	switch o {
	case OK:
		g.streak++
		if g.streak%20 == 0 {
			if g.limit < g.max {
				g.limit++
			}
			if g.cooldown > 0 { // calm again: shrink the backoff
				g.cooldown /= 2
				if g.cooldown < 2*time.Second {
					g.cooldown = 0
				}
			}
			if g.spacing > g.baseGap {
				g.spacing = max(g.baseGap, g.spacing*3/4)
			}
		}
	case Throttled:
		g.streak = 0
		if g.now().Before(g.until) {
			// Already backing off: the rest of a burst of 429s is the same episode and
			// must not escalate the cooldown again.
			return
		}
		g.limit = max(g.min, g.limit/2)
		if g.cooldown == 0 {
			g.cooldown = 10 * time.Second
		} else if g.cooldown < 5*time.Minute {
			g.cooldown *= 2
		}
		jitter := time.Duration(g.rnd.Int63n(int64(g.cooldown)/2 + 1))
		if t := g.now().Add(g.cooldown + jitter); t.After(g.until) {
			g.until = t
		}
		g.spacing = min(g.spacing*2+time.Second, 10*time.Second)
	}
}

// Cooling returns how long new requests are paused, if at all.
func (g *Governor) Cooling() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d := g.until.Sub(g.now()); d > 0 {
		return d
	}
	return 0
}

// Reset forgets all backoff, for a lane that just moved to a fresh IP.
func (g *Governor) Reset(start int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.limit = min(max(start, g.min), g.max)
	g.cooldown, g.until, g.streak = 0, time.Time{}, 0
	g.spacing = g.baseGap
	g.cond.Broadcast()
}

// Load is how full the governor is (in-flight requests / limit).
func (g *Governor) Load() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return float64(g.inflight) / float64(max(1, g.limit))
}
