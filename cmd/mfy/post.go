package main

import (
	"context"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// postRunner does the steps that follow a download (MusicBrainz tags, lyrics) for songs as
// they arrive, while the next ones are still downloading, instead of one long pass after the
// whole batch. MusicBrainz allows a request a second, so for a big batch that pass is the
// longest part of a run, and it uses none of what the downloads use (VPN lanes, YouTube).
// Finish does whatever is left and returns when it is all done.
type postRunner struct {
	ctx   context.Context
	u     *ui.UI
	o     opts
	st    *store.Store
	since time.Time

	hook func(files []string) error // replaces the real steps (tests)

	mu   sync.Mutex
	done map[string]bool // files already handled
	stop chan struct{}
	end  chan struct{}
}

// postBatch is how many new songs wait before a pass starts mid-run (a few at a time would
// print a heading for every song); Finish handles the remainder.
const postBatch = 10

// postTick is how often a running download is looked at for new songs.
var postTick = 3 * time.Second

// startPost begins following the downloads. It does nothing (and costs nothing) when both
// steps are switched off.
func startPost(ctx context.Context, u *ui.UI, o opts, st *store.Store, since time.Time, hook ...func([]string) error) *postRunner {
	p := &postRunner{ctx: ctx, u: u, o: o, st: st, since: since, done: map[string]bool{},
		stop: make(chan struct{}), end: make(chan struct{})}
	if len(hook) > 0 {
		p.hook = hook[0]
	}
	if !p.wanted() {
		close(p.end)
		return p
	}
	go func() {
		defer close(p.end)
		tick := time.NewTicker(postTick)
		defer tick.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if files := p.pending(); len(files) >= postBatch {
					p.handle(files)
				}
			}
		}
	}()
	return p
}

func (p *postRunner) wanted() bool {
	return p.hook != nil || (p.o.tagAfter && !config.Load().Private) || p.o.lyricsAfter
}

// handle runs the steps on files (tests replace them through hook).
func (p *postRunner) handle(files []string) error {
	if p.hook != nil {
		p.mu.Lock()
		for _, f := range files {
			p.done[f] = true
		}
		p.mu.Unlock()
		return p.hook(files)
	}
	return p.process(files)
}

// pending are the files finished since the start that no pass has handled yet.
func (p *postRunner) pending() []string {
	var out []string
	for _, f := range p.st.DoneSince(p.since) {
		p.mu.Lock()
		seen := p.done[f]
		p.mu.Unlock()
		if !seen {
			out = append(out, f)
		}
	}
	return out
}

func (p *postRunner) process(files []string) error {
	p.mu.Lock()
	for _, f := range files {
		p.done[f] = true
	}
	p.mu.Unlock()
	u := p.u.Lane("tags")
	if p.o.tagAfter && !config.Load().Private {
		u.Head("Fixing the tags of %s with MusicBrainz · turn off in settings", count(len(files), "new song"))
		t := p.o
		t.apply, t.rename, t.undo, t.offline = true, false, false, false
		if err := runTag(p.ctx, u, t, p.o.out, files...); err != nil {
			if p.ctx.Err() != nil {
				return context.Canceled
			}
			u.Bad("tags: %v", err)
		}
	}
	if p.o.lyricsAfter {
		u.Head("Fetching lyrics for %s · turn off in settings", count(len(files), "new song"))
		if err := fetchLyrics(p.ctx, u, p.o.out, files); err != nil {
			if p.ctx.Err() != nil {
				return context.Canceled
			}
			u.Bad("lyrics: %v", err)
		}
	}
	return nil
}

// Stop stops following without handling what is left (the run failed or was stopped).
func (p *postRunner) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.end // a pass that is running finishes first
}

// Finish stops following, then handles every song not yet handled.
func (p *postRunner) Finish() error {
	p.Stop()
	if !p.wanted() || p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	if files := p.pending(); len(files) > 0 {
		return p.handle(files)
	}
	return nil
}
