package jobs

import "sync"

// Breaker stops a run using a source that keeps failing for source-wide reasons (login
// refused, rate limited) so the remaining songs go to the next best source and the user
// sees one message, not one error per song. Any success resets the count.
type Breaker struct {
	mu     sync.Mutex
	limit  int
	streak int
	open   bool
	why    string
}

// NewBreaker opens after limit consecutive tripping failures (limit < 1 means 3).
func NewBreaker(limit int) *Breaker {
	if limit < 1 {
		limit = 3
	}
	return &Breaker{limit: limit}
}

// Record notes the outcome of one song (err nil = it worked) and reports whether this
// call is the one that opened the breaker (the caller prints the single message then).
func (b *Breaker) Record(err error) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.streak = 0
		return false
	}
	if !Classify(err).Trips() {
		return false // a song-specific failure says nothing about the source
	}
	b.streak++
	if !b.open && b.streak >= b.limit {
		b.open, b.why = true, Human(err)
		return true
	}
	return false
}

// Open reports whether the source should no longer be used in this run.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// Why is the human reason the breaker opened.
func (b *Breaker) Why() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.why
}
