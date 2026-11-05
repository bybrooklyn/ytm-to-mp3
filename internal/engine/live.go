package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// What a track is doing right now, for the live display.
const (
	stResolve int32 = iota // asking YouTube for the stream
	stQueued               // resolved, waiting for a download worker
	stFetch                // downloading it
	stEncode               // converting and tagging
	stMusic                // the normal route failed; trying through YouTube Music
)

// tstate is one in-flight track as the live display sees it.
type tstate struct {
	label   string
	started time.Time
	stage   atomic.Int32
	got     atomic.Int64
	total   atomic.Int64 // 0 = unknown (yt-dlp is downloading it)
	music   atomic.Bool  // found by a YouTube Music search (its row gets a "ytm" badge)

	// Stage timestamps for the YTM_DEBUG timing report. Each is written by the one stage
	// that owns the track at that moment; channel hand-offs order them.
	resolvedAt, fetchStart, encodeStart time.Time
	via                                 string // "direct", "yt-dlp", or with " (YT Music)"
	lookup                              string // "go" when the native player API resolved it
}

// liveSet is the registry of in-flight tracks.
type liveSet struct {
	mu sync.Mutex
	m  map[string]*tstate
}

func (s *liveSet) add(t model.Track) *tstate {
	ts := &tstate{label: ui.Sanitize(t.Label()), started: time.Now()}
	s.mu.Lock()
	if s.m == nil {
		s.m = map[string]*tstate{}
	}
	s.m[t.ID] = ts
	s.mu.Unlock()
	return ts
}

func (s *liveSet) remove(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// snapshot returns the in-flight tracks: ones being worked on (downloading, converting)
// first, then ones waiting, each oldest first. Moving from downloading to converting keeps a
// row in place, so rows don't jump around between frames.
func (s *liveSet) snapshot() []*tstate {
	s.mu.Lock()
	out := make([]*tstate, 0, len(s.m))
	for _, ts := range s.m {
		out = append(out, ts)
	}
	s.mu.Unlock()
	rank := func(t *tstate) int {
		switch t.stage.Load() {
		case stFetch, stEncode, stMusic:
			return 0
		}
		return 1
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].started.Before(out[j].started)
	})
	return out
}

// The palette is ytm's one (internal/ui); these short names keep the rows readable.
var (
	dim   = ui.CDim
	bold  = ui.CBold
	green = ui.CGreen
	red   = ui.CRed
	cyan  = ui.CCyan
	yel   = ui.CYellow
	reset = ui.CReset
)

func spin(now time.Time) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[int(now.UnixMilli()/100)%len(frames)]
}

func humanETA(d time.Duration) string {
	switch {
	case d <= 0:
		return "--"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// liveRows is the pinned block: an overall bar, the exits, and one row per track being
// worked on. It adapts to the terminal's width and height.
func (e *engine) liveRows(speed float64, began time.Time) []string {
	now := time.Now()
	width, height := e.UI.Width(), e.UI.Height()
	done, failed := int(e.done.Load()), int(e.failed.Load())
	finished := done + failed

	// overall: finished tracks plus the downloaded share of the ones downloading now, so the
	// bar creeps forward between completions instead of jumping
	snap := e.live.snapshot()
	frac := 0.0
	if e.total > 0 {
		part := 0.0
		for _, ts := range snap {
			if ts.stage.Load() == stEncode {
				part += 1
			} else if got, total := ts.got.Load(), ts.total.Load(); total > 0 {
				part += min(1, float64(got)/float64(total))
			}
		}
		frac = min(1, (float64(finished)+part*0.9)/float64(e.total))
	}
	eta := time.Duration(0)
	if elapsed := now.Sub(began); finished >= 3 && elapsed > 5*time.Second {
		perTrack := elapsed / time.Duration(finished)
		eta = perTrack * time.Duration(e.total-finished)
	}
	barW := min(30, max(10, width-74))
	left := "working out the time left"
	if eta > 0 {
		left = "about " + humanETA(eta) + " left"
	}
	// ♪ Downloading ████░░░░  12 of 40  ✓ 11 ✗ 1  2.3 MB/s · about 3m 05s left
	tally := ui.OK("✓") + " " + fmt.Sprint(done)
	if failed > 0 {
		tally += "  " + ui.Fail("✗") + " " + red + fmt.Sprint(failed) + reset
	}
	head := fmt.Sprintf("  %s %sDownloading%s %s  %s%d of %d%s  %s  %s%s/s%s %s· %s%s",
		ui.Accent("♪"), bold, reset, ui.Bar(frac, barW, green), bold, finished, e.total, reset,
		tally, cyan, HumanBytes(int64(speed)), reset, dim, left, reset)
	rows := []string{head}

	lanes := e.snap()
	if debugOn() && len(lanes) > 1 { // which exit is doing what: debugging detail
		var parts []string
		for _, l := range lanes {
			name := ui.Sanitize(l.exitName())
			switch {
			case l.swapping.Load():
				parts = append(parts, fmt.Sprintf("%s%s swapping%s", yel, name, reset))
			case l.gov.Cooling() > 0:
				parts = append(parts, fmt.Sprintf("%s%s resting %ds%s", yel, name, int(l.gov.Cooling().Seconds())+1, reset))
			default:
				parts = append(parts, fmt.Sprintf("%s %s%d%s", name, dim, l.done.Load(), reset))
			}
		}
		rows = append(rows, "  "+dim+"exits "+reset+strings.Join(parts, dim+" · "+reset))
	} else if allResting(lanes) {
		rows = append(rows, fmt.Sprintf("  %sYouTube is slowing us down; waiting a moment before carrying on%s", yel, reset))
	}

	// Songs being downloaded or converted get a row each; the ones still being looked up or
	// waiting their turn are just counted, so the screen doesn't churn.
	var active []*tstate
	waiting := 0
	for _, ts := range snap {
		switch ts.stage.Load() {
		case stFetch, stEncode, stMusic:
			active = append(active, ts)
		default:
			waiting++
		}
	}
	room := min(6, max(0, height-len(rows)-6))
	shown := active
	if len(shown) > room {
		shown = shown[:room]
	}
	// "  ↓ ytm   Artist - Title …  ████░░░░░░░░  42%  1.2 MB/3.4 MB": the label column
	// takes what the rest leaves.
	tw := max(20, width-60)
	for _, ts := range shown {
		label := ansi.Truncate(ts.label, tw, "…")
		pad := strings.Repeat(" ", max(0, tw-ansi.StringWidth(label)))
		badge := ui.Badge("yt")
		if ts.music.Load() {
			badge = ui.Badge("ytm")
		}
		var mark, state string
		switch ts.stage.Load() {
		case stFetch:
			mark = ui.Cyan("↓")
			got, total := ts.got.Load(), ts.total.Load()
			if total > 0 {
				f := float64(got) / float64(total)
				state = fmt.Sprintf("%s %s%3.0f%%%s  %s%s/%s%s", ui.Bar(f, 12, ""), bold, f*100, reset,
					dim, HumanBytes(got), HumanBytes(total), reset)
			} else {
				state = dim + "downloading" + strings.Repeat(".", 1+int(now.UnixMilli()/400)%3) + reset
			}
		case stEncode:
			mark, state = ui.Yellow("♪"), dim+"saving with tags and cover"+reset
		case stMusic:
			mark, badge, state = ui.Yellow(spin(now)), ui.Badge("ytm"), dim+"searching YouTube Music"+reset
		}
		rows = append(rows, fmt.Sprintf("  %s %s %s%s  %s", mark, badge, label, pad, state))
	}
	more := len(active) - len(shown)
	if more > 0 || waiting > 0 {
		parts := []string{}
		if more > 0 {
			parts = append(parts, fmt.Sprintf("%d more downloading", more))
		}
		if waiting > 0 {
			parts = append(parts, fmt.Sprintf("%d being looked up", waiting))
		}
		rows = append(rows, fmt.Sprintf("  %s %s%s%s", ui.Accent(spin(now)), dim, strings.Join(parts, " · "), reset))
	}
	return rows
}

// allResting: every exit is backing off (YouTube is pushing back everywhere).
func allResting(lanes []*lane) bool {
	if len(lanes) == 0 {
		return false
	}
	for _, l := range lanes {
		if l.gov.Cooling() <= 0 && !l.swapping.Load() {
			return false
		}
	}
	return true
}
