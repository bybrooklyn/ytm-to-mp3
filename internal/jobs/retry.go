package jobs

import (
	"context"
	"math/rand"
	"time"
)

// Retry runs f up to attempts times, waiting a jittered, doubling delay (starting at base)
// between tries, for as long as the failure may pass (see Class.Retryable). It stops at once
// on a failure retrying cannot fix (login refused, not available, disk full) and on
// cancellation. This is the one retry policy for every source's small requests.
func Retry(ctx context.Context, attempts int, base time.Duration, f func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	if base <= 0 {
		base = time.Second
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil {
			return nil
		}
		c := Classify(err)
		if ctx.Err() != nil || c == ClassCancelled || !c.Retryable() || c == ClassOther || i == attempts-1 {
			return err
		}
		d := base << i
		d = d/2 + time.Duration(rand.Int63n(int64(d)))
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
