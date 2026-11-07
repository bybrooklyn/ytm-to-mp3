package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// emit reports an event to the run's sinks (the ledger behind the summary, and the
// terminal). Safe on a run that has none.
func (r *runExits) emit(e jobs.Event) {
	if r != nil && r.sink != nil {
		jobs.Emit(r.sink, e)
	}
}

// record reports an event to the ledger only: for sources that print their own lines.
func (r *runExits) record(e jobs.Event) {
	if r != nil && r.led != nil {
		jobs.Emit(r.led, e)
	}
}

// recordYouTube files the outcome of a YouTube engine run into the ledger, from the state
// database (the engine prints its own lines).
func (r *runExits) recordYouTube(st *store.Store, todo []model.Track, upgraded map[string]bool) {
	if r == nil || r.led == nil || st == nil {
		return
	}
	for _, t := range todo {
		label := t.Label()
		if label == "" {
			label = t.ID
		}
		status, perm, text := st.TrackOutcome(t.ID)
		switch status {
		case "done":
			_, path := st.TrackStatus(t.ID)
			k := jobs.Done
			if upgraded[t.ID] {
				k = jobs.Upgraded
			}
			r.record(jobs.Event{Kind: k, ID: t.ID, Label: label, Source: "youtube", Path: path})
		case "failed":
			class := jobs.Classify(fmt.Errorf("%s", text))
			r.record(jobs.Event{Kind: jobs.Failed, ID: t.ID, Label: label, Source: "youtube", Reason: text,
				Class: class, Retryable: !perm && class.Retryable()})
		default: // never reached (the run stopped first)
			r.record(jobs.Event{Kind: jobs.Queued, ID: t.ID, Label: label, Source: "youtube"})
		}
	}
}

// summaryWorthPrinting: the one-screen summary is for runs that mixed sources or had
// something to explain; a plain YouTube run keeps its own summary.
func summaryWorthPrinting(led *jobs.Ledger) bool {
	rs := led.Results()
	if len(rs) == 0 {
		return false
	}
	sources := map[string]bool{}
	for _, r := range rs {
		sources[r.Source] = true
	}
	c := led.Counts()
	return len(sources) > 1 || (len(sources) == 1 && !sources["youtube"]) || c.Upgraded > 0
}

// printRunSummary says what the run did, per outcome: downloaded (by source), upgraded,
// already held, and every failure grouped by reason with the songs named.
func printRunSummary(u *ui.UI, led *jobs.Ledger, interrupted bool) {
	if !summaryWorthPrinting(led) {
		if interrupted && len(led.Results()) > 0 {
			u.Note("stopped early · what finished is kept; run ytm again with the same links to continue")
		}
		return
	}
	rs := led.Results()
	c := led.Counts()
	bySource := map[string]int{}
	for _, r := range rs {
		if r.Kind == jobs.Done || r.Kind == jobs.Upgraded {
			bySource[r.Source]++
		}
	}
	var srcs []string
	for s, n := range bySource {
		srcs = append(srcs, fmt.Sprintf("%d %s", n, sourceName(s)))
	}
	sort.Strings(srcs)

	u.Head("Summary")
	got := c.Done + c.Upgraded
	line := fmt.Sprintf("%s %s downloaded", ui.OK("✓"), ui.Bold(fmt.Sprint(got)))
	if len(srcs) > 0 {
		line += ui.Dim(" · " + strings.Join(srcs, ", "))
	}
	u.Styled("  " + line)
	if c.Upgraded > 0 {
		u.Note("%d of them replaced a lower-quality copy (the old file is in .mfetchy/replaced)", c.Upgraded)
	}
	if h := led.Held(); h > 0 {
		u.Note("%s already in your library, left alone", count(h, "song"))
	}
	if c.Failed > 0 {
		u.Styled(fmt.Sprintf("  %s %s", ui.Fail("✗"), ui.Red(fmt.Sprintf("%d failed", c.Failed))))
		for _, l := range resultLines(rs, 20) {
			u.Styled(l)
		}
		if c.Retryable > 0 {
			u.Hint("%d of those may work on another try", c.Retryable)
		}
	}
	if c.Unfinished > 0 {
		u.Note("%s not started or not finished", count(c.Unfinished, "song"))
	}
	if interrupted {
		u.Note("stopped early · run ytm again with the same links to continue where it left off")
	}
}

func sourceName(s string) string {
	switch s {
	case "youtube":
		return "YouTube"
	case "apple":
		return "Apple Music"
	case "tidal":
		return "Tidal"
	case "soundcloud":
		return "SoundCloud"
	case "bandcamp":
		return "Bandcamp"
	}
	return s
}

// resultLines lists the failed songs grouped by reason, commonest first.
func resultLines(rs []jobs.Result, limit int) []string {
	byWhy := map[string][]string{}
	var whys []string
	for _, r := range rs {
		if r.Kind != jobs.Failed {
			continue
		}
		why := ui.Sanitize(r.Reason)
		if why == "" {
			why = "unknown error"
		}
		if _, ok := byWhy[why]; !ok {
			whys = append(whys, why)
		}
		byWhy[why] = append(byWhy[why], ui.Sanitize(firstNonEmpty(r.Label, r.ID)))
	}
	sort.SliceStable(whys, func(i, j int) bool { return len(byWhy[whys[i]]) > len(byWhy[whys[j]]) })
	var out []string
	named := 0
	for _, why := range whys {
		songs := byWhy[why]
		out = append(out, fmt.Sprintf("    %s %s", why, ui.Dim(fmt.Sprintf("(%d)", len(songs)))))
		for i, s := range songs {
			if named >= limit {
				out = append(out, ui.Dim(fmt.Sprintf("        …and %d more", len(songs)-i)))
				break
			}
			out = append(out, "        "+s)
			named++
		}
	}
	return out
}
