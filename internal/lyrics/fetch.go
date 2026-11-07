package lyrics

import (
	"context"
	"errors"
	"time"
)

// Result is what a lookup found. Kind "" means nothing (cached or fresh).
type Result struct {
	Kind   Kind
	Source string // provider name, for the credit
	Header Header // the record's own title, artist and album
	Synced []Line
	Plain  string
	Cached bool // answered from the cache: no request was made
}

// Found reports whether there are lyrics to store.
func (r Result) Found() bool { return r.Kind == KindSynced || r.Kind == KindPlain }

// Fetcher looks lyrics up, cache first, then the providers in order.
type Fetcher struct {
	Providers []Provider // in order of preference
	Cache     *Cache     // optional
	// Pace waits this long before every provider request after the first (polite pacing
	// when many songs are done in a row); 0 = none.
	Pace time.Duration
}

// Lookup returns the lyrics for q. The cache is read first; a song with a cached answer
// (lyrics, instrumental, or a "none" still within its retry-after) makes no request.
//
// Providers are asked in order and the first to offer synced lyrics that Choose accepts
// ends the search, so later (less trusted) providers are not even contacted for songs
// the first knows. If nothing was found and some provider was unreachable, the error is
// returned and nothing is cached, so the next run tries again.
func (f *Fetcher) Lookup(ctx context.Context, q Query) (Result, error) {
	if r, ok := f.Cache.Get(q); ok {
		return r, nil
	}
	var cands []Candidate
	var lastErr error
	for i, p := range f.Providers {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if i > 0 && f.Pace > 0 {
			select {
			case <-time.After(f.Pace):
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		got, err := p.Fetch(ctx, q)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			lastErr = err
		}
		for _, c := range got {
			c.Rank = i
			cands = append(cands, c)
		}
		if pk, ok := Choose(cands, q); ok && pk.Kind == KindSynced {
			break
		}
	}
	pk, ok := Choose(cands, q)
	if !ok {
		if lastErr != nil {
			return Result{}, lastErr
		}
		r := Result{}
		_ = f.Cache.Put(q, r, 0)
		return r, nil
	}
	r := Result{Kind: pk.Kind, Source: pk.Provider, Synced: pk.Synced, Plain: pk.Plain,
		Header: Header{Title: pk.Title, Artist: pk.Artist, Album: pk.Album}}
	_ = f.Cache.Put(q, r, pk.Duration)
	return r, nil
}

// IsOutage reports whether err means a provider could not be reached (try again later).
func IsOutage(err error) bool { return errors.Is(err, ErrUnavailable) }
