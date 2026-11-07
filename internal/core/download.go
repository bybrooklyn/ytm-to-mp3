// Package core is what mfetchy does, without a screen of its own: the download pipeline
// (links, files and pasted blocks in, tagged songs out), the sources behind it (YouTube,
// Apple Music, Tidal, SoundCloud, Bandcamp) and the steps that follow (tags, lyrics, cover
// and .nfo files).
//
// A caller supplies three things in Deps: where text output goes (UI), how questions are
// asked (Prompter) and who hears about each song (Sink, see package jobs). The terminal
// program passes its own terminal and forms; the desktop window passes dialogs and a sink
// that fills its queue list. Neither has a pipeline of its own.
package core

import (
	"context"
	"fmt"
	"io"

	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// Deps are the caller's side of a run.
type Deps struct {
	// UI receives the text a run prints (progress lines, notes, warnings). Nil discards it.
	UI *ui.UI
	// Prompter answers the questions a run asks. Nil answers every question with its default.
	Prompter Prompter
	// Sink hears every event of every song, as it happens. Nil is fine.
	Sink jobs.Sink
}

// Result is how a run ended.
type Result struct {
	Results []jobs.Result // every song's final state, in the order they were first seen
	Counts  jobs.Counts
	Held    int // songs left alone because the library already had them
	Rounds  int // 1, or more when failed songs were tried again
	Stopped bool
}

// Download runs one batch of inputs of any mix: YouTube, Apple Music, Spotify, Tidal,
// SoundCloud and Bandcamp links (songs, albums, playlists), a Takeout .zip, folder or .csv,
// a text file of links, or a pasted block of any of these. Inputs are parsed and deduplicated,
// the VPN is connected once, each source runs in its own lane, the steps that follow run
// (tags, lyrics, sidecars), and a summary is printed to d.UI.
//
// When songs failed for a reason that may pass (a connection drop, a rate limit) it asks
// d.Prompter whether to try just those again, up to twice: the state database already holds
// what arrived, so a second pass only redoes the rest.
//
// Download returns when the run is over or ctx ends (then the error is ctx's, and what
// finished is kept). The Result is valid even with an error.
func Download(ctx context.Context, inputs []string, o Options, d Deps) (Result, error) {
	if d.UI == nil {
		d.UI = ui.NewWriter(io.Discard)
	}
	if d.Prompter == nil {
		d.Prompter = AutoPrompter{}
	}
	return runDownload(ctx, d.UI, o.internal(d.Prompter), d.Sink, inputs)
}

// runDownload is Download with the options already in the pipeline's form.
func runDownload(ctx context.Context, u *ui.UI, op opts, sink jobs.Sink, inputs []string) (Result, error) {
	if sink != nil {
		op.sink = sink
	}
	sink = op.sink
	d := Deps{UI: u, Prompter: op.prompt, Sink: sink}
	var res Result
	for round := 0; ; round++ {
		led := &jobs.Ledger{}
		err := downloadOnceFn(ctx, d.UI, op, led, d.Sink, inputs)
		res = Result{Results: led.Results(), Counts: led.Counts(), Held: led.Held(), Rounds: round + 1, Stopped: ctx.Err() != nil}
		c := res.Counts
		if ctx.Err() != nil || c.Retryable == 0 || round >= 2 {
			return res, err
		}
		again, perr := d.Prompter.Confirm(ctx, Question{
			Title:  fmt.Sprintf("%s failed for a reason that may pass. Try those again now?", count(c.Retryable, "song")),
			Detail: "The ones that arrived stay; only the rest is attempted again.",
		})
		if perr != nil || !again {
			return res, err
		}
	}
}

// Seams for tests: where the tools come from, and one round of a run.
var (
	ensureTools    = deps.Ensure
	downloadOnceFn = downloadOnce
)
