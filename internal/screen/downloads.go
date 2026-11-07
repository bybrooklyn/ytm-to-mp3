package screen

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/jobs"
)

// Downloads is the state of the window's Downloads page: what was typed or dropped, the run
// under way, a live queue of songs and, at the end, a summary. It draws nothing. A graphical
// host reads Snapshot and sends the person's actions to Start, Stop, Resume and RetryFailed.
// The terminal never shows it (a terminal download is a command, not a page).
//
// The run itself is injected (RunFunc), so this package knows nothing of the pipeline: the
// window passes a function that calls core.Download, and Downloads is the jobs.Sink it
// reports to.
type Downloads struct {
	mu       sync.Mutex
	run      RunFunc
	settings func() DownloadSettings

	input string // the text area

	state   string // "", "running", "stopping", "done", "stopped"
	cancel  context.CancelFunc
	inputs  []string // what the last run was given, for Resume and Retry
	began   time.Time
	ended   time.Time
	err     string
	items   map[string]*DLItem
	order   []string // ids in first-seen order
	speed   float64  // bytes per second, smoothed
	lastAt  time.Time
	pending int64 // bytes seen since lastAt
	result  *DLSummary

	// OnChange, if set, is called after the state changes (from any goroutine). Progress
	// events are coalesced: it is called at most about four times a second for them.
	OnChange func()
	lastPing time.Time
	trailing bool // a delayed OnChange is already scheduled
}

// RunFunc runs one download: inputs as typed or dropped, events to sink. It returns how the
// run ended. The window passes a function that calls core.Download.
type RunFunc func(ctx context.Context, inputs []string, sink jobs.Sink) (RunResult, error)

// RunResult is how a run ended.
type RunResult struct {
	Results []jobs.Result
	Held    int // songs left alone because the library already had them
}

// DownloadSettings is the line under the input: where songs go and how.
type DownloadSettings struct {
	Folder string // "~/Music"
	Format string // "mp3"
	Extras string // "also: complete albums, fix tags (change in settings)"
}

// Per-song states.
const (
	DLQueued   = "queued"
	DLWorking  = "working"
	DLDone     = "done"
	DLUpgraded = "upgraded"
	DLSkipped  = "skipped"
	DLFailed   = "failed"
)

// DLItem is one song in the queue.
type DLItem struct {
	ID, Label, Source string
	State             string
	Stage             string // "resolving", "downloading", "tagging" while working
	Bytes, Total      int64
	Quality, Path     string
	Reason            string
	Retryable         bool
}

// DLGroup is the songs of one source.
type DLGroup struct {
	Source string // "youtube"
	Label  string // "YouTube"
	Items  []DLItem
	Counts DLCounts
}

// DLCounts tallies songs.
type DLCounts struct {
	Total, Queued, Working, Done, Upgraded, Skipped, Failed int
}

// Finished is songs that reached an end (done, upgraded, skipped, failed).
func (c DLCounts) Finished() int { return c.Done + c.Upgraded + c.Skipped + c.Failed }

// DLSummary is the end of a run.
type DLSummary struct {
	Downloaded, Upgraded, Held, Failed, Unfinished int
	Failures                                       []DLFailure // grouped by reason, commonest first
	Retryable                                      int
	Elapsed                                        time.Duration
}

// DLFailure is a reason and the songs that failed for it.
type DLFailure struct {
	Reason string
	Songs  []string
}

// DownloadsView is what a window draws.
type DownloadsView struct {
	Input    string
	Settings DownloadSettings
	State    string // "" (nothing yet), "running", "stopping", "done", "stopped"
	Error    string // the run failed as a whole
	Groups   []DLGroup
	Counts   DLCounts
	Speed    float64 // bytes per second
	Elapsed  time.Duration
	Summary  *DLSummary // set when the run is over
	// Hidden is songs not listed because there are too many to show (finished ones first).
	Hidden int
}

// NewDownloads builds the page. settings is asked each time the page is drawn, so it
// follows changes made in Settings.
func NewDownloads(run RunFunc, settings func() DownloadSettings) *Downloads {
	return &Downloads{run: run, settings: settings, items: map[string]*DLItem{}}
}

// ---- Model (the page is only drawn by graphical hosts) -------------------------------

func (d *Downloads) Key(string)        {}
func (d *Downloads) Type(string)       {}
func (d *Downloads) SelectIndex(int)   {}
func (d *Downloads) Editing() bool     { return false }
func (d *Downloads) Finished() bool    { return false }
func (d *Downloads) Action() string    { return "" }
func (d *Downloads) Paste(text string) { d.AddInput(text) }

// Screen is the page's title and its snapshot.
func (d *Downloads) Screen() Screen {
	v := d.Snapshot()
	return Screen{Title: "downloads", Downloads: &v}
}

var _ Model = (*Downloads)(nil)

// ---- what the person does ------------------------------------------------------------

// SetInput replaces the text area's text.
func (d *Downloads) SetInput(s string) {
	d.mu.Lock()
	d.input = s
	d.mu.Unlock()
}

// AddInput adds text (a paste, a dropped file's path) on a line of its own.
func (d *Downloads) AddInput(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	d.mu.Lock()
	if d.input != "" && !strings.HasSuffix(d.input, "\n") {
		d.input += "\n"
	}
	d.input += s
	d.mu.Unlock()
	d.changed(true)
}

// Running reports whether a run is under way.
func (d *Downloads) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state == "running" || d.state == "stopping"
}

// ErrNothingToDo is Start's answer to an empty text area.
var ErrNothingToDo = errors.New("paste a link, or add a file, first")

// Start begins a run with what is in the text area. It returns at once; the run goes on in the
// background and reports through the state.
func (d *Downloads) Start() error {
	d.mu.Lock()
	in := strings.TrimSpace(d.input)
	d.mu.Unlock()
	if in == "" {
		return ErrNothingToDo
	}
	return d.begin([]string{in})
}

// Resume runs the last inputs again (after Stop, or to retry what failed): songs that arrived
// are already in the library and are left alone.
func (d *Downloads) Resume() error {
	d.mu.Lock()
	in := append([]string(nil), d.inputs...)
	d.mu.Unlock()
	if len(in) == 0 {
		return ErrNothingToDo
	}
	return d.begin(in)
}

// RetryFailed is Resume under the name the summary's button uses.
func (d *Downloads) RetryFailed() error { return d.Resume() }

func (d *Downloads) begin(inputs []string) error {
	d.mu.Lock()
	if d.state == "running" || d.state == "stopping" {
		d.mu.Unlock()
		return errors.New("a download is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.state, d.err, d.result = "running", "", nil
	d.inputs = inputs
	d.items, d.order = map[string]*DLItem{}, nil
	d.speed, d.lastAt, d.pending = 0, time.Time{}, 0
	d.began, d.ended = time.Now(), time.Time{}
	d.mu.Unlock()
	d.changed(true)
	go func() {
		res, err := d.run(ctx, inputs, d)
		d.finish(ctx, res, err)
		cancel()
	}()
	return nil
}

// Stop cancels the run: what is in flight is cleaned up and what finished is kept.
func (d *Downloads) Stop() {
	d.mu.Lock()
	if d.state == "running" && d.cancel != nil {
		d.state = "stopping"
		d.cancel()
	}
	d.mu.Unlock()
	d.changed(true)
}

func (d *Downloads) finish(ctx context.Context, res RunResult, err error) {
	d.mu.Lock()
	d.ended = time.Now()
	stopped := ctx.Err() != nil
	switch {
	case stopped:
		d.state = "stopped"
	default:
		d.state = "done"
	}
	if err != nil && !stopped {
		d.err = err.Error()
	}
	d.result = summarize(res, d.ended.Sub(d.began))
	// What the run's own ledger says wins over what events showed (a song that failed and
	// then arrived from another source is done).
	for _, r := range res.Results {
		it := d.items[r.ID]
		if it == nil {
			continue
		}
		switch r.Kind {
		case jobs.Done, jobs.Upgraded, jobs.Skipped, jobs.Failed:
			it.State, it.Reason, it.Retryable = stateOf(r.Kind), r.Reason, r.Retryable
			if r.Path != "" {
				it.Path = r.Path
			}
		}
	}
	d.mu.Unlock()
	d.changed(true)
}

func stateOf(k jobs.Kind) string {
	switch k {
	case jobs.Done:
		return DLDone
	case jobs.Upgraded:
		return DLUpgraded
	case jobs.Skipped:
		return DLSkipped
	case jobs.Failed:
		return DLFailed
	}
	return DLQueued
}

func summarize(res RunResult, elapsed time.Duration) *DLSummary {
	s := &DLSummary{Held: res.Held, Elapsed: elapsed}
	byWhy := map[string][]string{}
	var whys []string
	for _, r := range res.Results {
		switch r.Kind {
		case jobs.Done:
			s.Downloaded++
		case jobs.Upgraded:
			s.Downloaded++
			s.Upgraded++
		case jobs.Skipped:
			s.Held++
		case jobs.Failed:
			s.Failed++
			if r.Retryable {
				s.Retryable++
			}
			why := r.Reason
			if why == "" {
				why = "unknown error"
			}
			if _, ok := byWhy[why]; !ok {
				whys = append(whys, why)
			}
			label := r.Label
			if label == "" {
				label = r.ID
			}
			byWhy[why] = append(byWhy[why], label)
		default:
			s.Unfinished++
		}
	}
	sort.SliceStable(whys, func(i, j int) bool { return len(byWhy[whys[i]]) > len(byWhy[whys[j]]) })
	for _, w := range whys {
		s.Failures = append(s.Failures, DLFailure{Reason: w, Songs: byWhy[w]})
	}
	return s
}

// ---- jobs.Sink -----------------------------------------------------------------------

// Emit applies one event of the running download. It is safe to call from any goroutine.
func (d *Downloads) Emit(e jobs.Event) {
	if e.ID == "" {
		return
	}
	d.mu.Lock()
	it := d.items[e.ID]
	if it == nil {
		it = &DLItem{ID: e.ID, State: DLQueued}
		d.items[e.ID] = it
		d.order = append(d.order, e.ID)
	}
	if e.Label != "" {
		it.Label = e.Label
	}
	if e.Source != "" {
		it.Source = e.Source
	}
	final := false
	switch e.Kind {
	case jobs.Queued:
	case jobs.Started:
		if it.State == DLQueued || it.State == DLFailed {
			it.State, it.Reason = DLWorking, ""
		}
	case jobs.Stage:
		if it.State != DLDone && it.State != DLUpgraded {
			it.State, it.Stage = DLWorking, e.Stage
		}
	case jobs.Progress:
		if it.State != DLDone && it.State != DLUpgraded {
			it.State = DLWorking
			if it.Stage == "" {
				it.Stage = "downloading"
			}
			if e.Bytes > it.Bytes {
				d.addBytes(e.Bytes - it.Bytes)
			}
			it.Bytes, it.Total = e.Bytes, e.Total
		}
	case jobs.Done, jobs.Upgraded:
		it.State, it.Stage, it.Path, it.Quality, it.Reason = stateOf(e.Kind), "", e.Path, e.Quality, ""
		if it.Total > 0 {
			it.Bytes = it.Total
		}
		final = true
	case jobs.Skipped:
		it.State, it.Stage, it.Reason = DLSkipped, "", e.Reason
		final = true
	case jobs.Failed:
		// a later failure never undoes a success (a second source giving up)
		if it.State != DLDone && it.State != DLUpgraded {
			it.State, it.Stage, it.Reason, it.Retryable = DLFailed, "", e.Reason, e.Retryable
		}
		final = true
	}
	d.mu.Unlock()
	d.changed(final || e.Kind == jobs.Queued || e.Kind == jobs.Started)
}

// addBytes feeds the speed estimate (called with d.mu held).
func (d *Downloads) addBytes(n int64) {
	now := time.Now()
	if d.lastAt.IsZero() {
		d.lastAt = now
	}
	d.pending += n
	if dt := now.Sub(d.lastAt); dt >= 500*time.Millisecond {
		inst := float64(d.pending) / dt.Seconds()
		if d.speed == 0 {
			d.speed = inst
		} else {
			d.speed = d.speed*0.6 + inst*0.4
		}
		d.lastAt, d.pending = now, 0
	}
}

// changed tells the window. Progress-only changes are coalesced: one that comes too soon
// after the last is sent a moment later instead of being dropped.
func (d *Downloads) changed(now bool) {
	d.mu.Lock()
	f := d.OnChange
	if f == nil {
		d.mu.Unlock()
		return
	}
	if wait := 250*time.Millisecond - time.Since(d.lastPing); !now && wait > 0 {
		if !d.trailing {
			d.trailing = true
			time.AfterFunc(wait, func() {
				d.mu.Lock()
				d.trailing = false
				d.mu.Unlock()
				d.changed(true)
			})
		}
		d.mu.Unlock()
		return
	}
	d.lastPing = time.Now()
	d.mu.Unlock()
	f()
}

// ---- the view ------------------------------------------------------------------------

// maxListed is how many songs a group lists; the rest are counted.
const maxListed = 40

// Snapshot is a copy of the state for drawing.
func (d *Downloads) Snapshot() DownloadsView {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := DownloadsView{Input: d.input, State: d.state, Error: d.err, Speed: d.speed, Summary: d.result}
	if d.settings != nil {
		v.Settings = d.settings()
	}
	if !d.began.IsZero() {
		end := d.ended
		if end.IsZero() {
			end = time.Now()
		}
		v.Elapsed = end.Sub(d.began)
	}
	if d.state != "running" && d.state != "stopping" {
		v.Speed = 0
	}
	bySource := map[string]*DLGroup{}
	var sources []string
	for _, id := range d.order {
		it := d.items[id]
		g := bySource[it.Source]
		if g == nil {
			g = &DLGroup{Source: it.Source, Label: SourceLabel(it.Source)}
			bySource[it.Source] = g
			sources = append(sources, it.Source)
		}
		g.Counts.Total++
		v.Counts.Total++
		switch it.State {
		case DLQueued:
			g.Counts.Queued++
			v.Counts.Queued++
		case DLWorking:
			g.Counts.Working++
			v.Counts.Working++
		case DLDone:
			g.Counts.Done++
			v.Counts.Done++
		case DLUpgraded:
			g.Counts.Upgraded++
			v.Counts.Upgraded++
		case DLSkipped:
			g.Counts.Skipped++
			v.Counts.Skipped++
		case DLFailed:
			g.Counts.Failed++
			v.Counts.Failed++
		}
		g.Items = append(g.Items, *it)
	}
	for _, s := range sources {
		g := bySource[s]
		// Working first, then failed, then waiting, then finished; at most maxListed shown.
		rank := func(st string) int {
			switch st {
			case DLWorking:
				return 0
			case DLFailed:
				return 1
			case DLQueued:
				return 2
			}
			return 3
		}
		sort.SliceStable(g.Items, func(i, j int) bool { return rank(g.Items[i].State) < rank(g.Items[j].State) })
		if len(g.Items) > maxListed {
			v.Hidden += len(g.Items) - maxListed
			g.Items = g.Items[:maxListed]
		}
		v.Groups = append(v.Groups, *g)
	}
	return v
}

// SourceLabel is a source's name for people.
func SourceLabel(s string) string {
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
	case "":
		return "Songs"
	}
	return s
}
