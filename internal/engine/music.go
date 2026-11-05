package engine

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

// A track whose video id is dead ("Video unavailable") has usually been re-uploaded: labels
// replace releases (remasters, re-issues) and YouTube Music points the song at a new video.
// Asking for the old id again, even through music.youtube.com, fails the same way, so the
// rescue searches YouTube Music for the song by artist and title and downloads the match.

// musicSearchHits is how many search results are checked for a match.
const musicSearchHits = 5

var errNoMusicMatch = &Error{kindPermanent, "not found on YouTube Music either"}

// normTitle folds a title for comparison: lower case, letters and digits only, with
// bracketed parts ("(2023 Remaster)", "[Official Video]") and " - ..." suffixes dropped.
func normTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth = max(0, depth-1)
		case depth > 0:
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	if out := b.String(); out != "" {
		return out
	}
	// a title that is all brackets or symbols: compare it as is
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) }), " ")
}

// titleMatches reports whether a search result's title is the song we want. A " - " suffix
// (" - 2011 Remaster") on either side is ignored.
func titleMatches(want, got string) bool {
	cut := func(s string) string {
		if i := strings.Index(s, " - "); i > 0 {
			return s[:i]
		}
		return s
	}
	w, g := normTitle(want), normTitle(got)
	if w == "" || g == "" {
		return false
	}
	return w == g || normTitle(cut(want)) == normTitle(cut(got))
}

// fullTitle folds a title like normTitle but keeps bracketed parts, so "Time (2023
// Remaster)" and "Time" differ.
func fullTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// orderHits keeps the results that are the song we want, the exact version first (same
// title including "(2023 Remaster)" etc.), then other versions of the same song. The old
// id itself is dropped.
func orderHits(title, oldID string, hits []musicHit) []musicHit {
	var exact, other []musicHit
	want := fullTitle(title)
	for _, h := range hits {
		switch {
		case h.ID == oldID || !titleMatches(title, h.Title):
		case want != "" && fullTitle(h.Title) == want:
			exact = append(exact, h)
		default:
			other = append(other, h)
		}
	}
	return append(exact, other...)
}

// artistMatches reports whether any wanted artist is among the result's artists (either
// name containing the other, so "Tyler, The Creator" matches "Tyler The Creator").
func artistMatches(want, got []string) bool {
	if len(want) == 0 {
		return true // nothing to check against; the title had to match exactly
	}
	for _, w := range want {
		nw := normTitle(w)
		if nw == "" {
			continue
		}
		for _, g := range got {
			ng := normTitle(g)
			if ng != "" && (strings.Contains(ng, nw) || strings.Contains(nw, ng)) {
				return true
			}
		}
	}
	return false
}

// musicQuery is what to search YouTube Music for ("" if the track has no title to go on).
func musicQuery(t model.Track) string {
	if strings.TrimSpace(t.Title) == "" {
		return ""
	}
	return strings.TrimSpace(t.PrimaryArtist() + " " + t.Title)
}

// search is one YouTube Music search, possibly still running.
type search struct {
	done chan struct{}
	hits []musicHit
	err  error
}

// searchAhead starts the YouTube Music search for t in the background, the moment the first
// exit calls it unavailable: while other exits are asked for a second opinion (seconds
// each), the search is already under way, so a dead track costs no extra wait at the end.
// Calling it again for the same track does nothing.
func (e *engine) searchAhead(ctx context.Context, t model.Track) {
	if musicQuery(t) == "" {
		return
	}
	s := &search{done: make(chan struct{})}
	if _, loaded := e.searches.LoadOrStore(t.ID, s); loaded {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer close(s.done)
		s.hits, s.err = e.runSearch(ctx, musicQuery(t))
	}()
}

func (e *engine) runSearch(ctx context.Context, q string) ([]musicHit, error) {
	ln := e.pick()
	if err := ln.gov.Acquire(ctx); err != nil {
		return nil, err
	}
	hits, err := ln.ytdlp().SearchMusic(ctx, q, musicSearchHits)
	ln.gov.Release(outcome(err))
	return hits, err
}

// searchFor returns the search results for t: the one started ahead of time if there is
// one, otherwise a fresh search.
func (e *engine) searchFor(ctx context.Context, t model.Track) ([]musicHit, error) {
	if v, ok := e.searches.LoadAndDelete(t.ID); ok {
		s := v.(*search)
		select {
		case <-s.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if s.err == nil || ctx.Err() != nil {
			return s.hits, s.err
		}
		// the early search failed (a throttled exit, say): try once more now
	}
	return e.runSearch(ctx, musicQuery(t))
}

// findOnMusic finds t on YouTube Music: the replacement remembered from an earlier run if
// there is one, otherwise the first search result whose title and artist match (the exact
// version first). It returns a job ready for the fetch stage (music=true, video=new id).
func (e *engine) findOnMusic(ctx context.Context, t model.Track, ts *tstate) (*job, error) {
	if musicQuery(t) == "" && e.Replacements[t.ID] == "" {
		return nil, errNoMusicMatch
	}
	if ts != nil {
		ts.stage.Store(stMusic)
	}
	var cands []musicHit
	if r := e.Replacements[t.ID]; r != "" {
		cands = append(cands, musicHit{ID: r, Title: t.Title})
	}
	// A hit that can't be looked up because of the exit (throttled, a bot check) is tried
	// once more on another; if that is all that stood in the way, the error says so, so the
	// song is retried later instead of being written off as "not on YouTube Music".
	var transient error
	tryHits := func(hits []musicHit, check bool) (*job, error) {
		for _, h := range hits {
			for attempt := 0; attempt < 2; attempt++ {
				ln := e.pick()
				if err := ln.gov.Acquire(ctx); err != nil {
					return nil, err
				}
				epoch := ln.epoch.Load()
				res, err := ln.ytdlp().Resolve(ctx, h.ID)
				ln.gov.Release(outcome(err))
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil {
					if kindOf(err) == kindPermanent {
						break // this video is gone; on to the next hit
					}
					transient = err
					continue
				}
				if check && !artistMatches(t.Artists, res.Artists) {
					break
				}
				ts.music.Store(true)
				return &job{tr: t, res: res, lane: ln, epoch: epoch, ts: ts, music: true, video: h.ID}, nil
			}
		}
		return nil, nil
	}
	// a remembered match was checked when it was found
	if j, err := tryHits(cands, false); j != nil || err != nil {
		return j, err
	}
	if musicQuery(t) == "" {
		return nil, errNoMusicMatch
	}
	hits, err := e.searchFor(ctx, t)
	if err != nil {
		return nil, err
	}
	j, err := tryHits(orderHits(t.Title, t.ID, hits), true)
	if j != nil || err != nil {
		return j, err
	}
	if transient != nil {
		return nil, transient
	}
	return nil, errNoMusicMatch
}

// rescue is the last try for a track that failed: find it on YouTube Music. The store
// remembers the attempt, so a track that still isn't there is skipped on later runs
// (until --retry). The returned error explains both failures.
func (e *engine) rescue(ctx context.Context, t model.Track, ts *tstate, cause error) (*job, error) {
	j, err := e.findOnMusic(ctx, t, ts)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Only a real answer is remembered (a match, or "not there"): a search that failed on
	// the network says nothing about the song, so the next run tries again.
	if err == nil || errors.Is(err, errNoMusicMatch) {
		found := ""
		if err == nil {
			found = j.video // remembered, so an interrupted run doesn't search for it again
		}
		if serr := e.Store.MarkMusicTried(t.ID, found); serr != nil && !errors.Is(serr, context.Canceled) {
			e.UI.Printf("warning: couldn't record the YouTube Music attempt for %s: %v", t.ID, serr)
		}
	}
	switch {
	case err == nil:
		return j, nil
	case !model.ValidID(t.ID):
		// A song imported from Apple Music or Spotify: it never had a video of its own, so
		// the YouTube Music search is the whole story.
		if errors.Is(err, errNoMusicMatch) {
			return nil, &Error{kindPermanent, "not found on YouTube Music"}
		}
		return nil, &Error{kindTransient, "YouTube Music search failed: " + err.Error()}
	case errors.Is(err, errNoMusicMatch):
		return nil, &Error{kindOf(cause), cause.Error() + "; not found on YouTube Music either"}
	default:
		return nil, &Error{kindTransient, cause.Error() + "; the YouTube Music search failed (" + err.Error() + "), will retry"}
	}
}
