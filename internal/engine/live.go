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
	stFetch                // downloading it
	stEncode               // converting and tagging
)

// tstate is one in-flight track as the live display sees it.
type tstate struct {
	label   string
	started time.Time
	stage   atomic.Int32
	got     atomic.Int64
	total   atomic.Int64 // 0 = unknown (yt-dlp is downloading it)
	exit    atomic.Value // string: which exit it's using
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

// snapshot returns the in-flight tracks, downloading ones first, then oldest first.
func (s *liveSet) snapshot() []*tstate {
	s.mu.Lock()
	out := make([]*tstate, 0, len(s.m))
	for _, ts := range s.m {
		out = append(out, ts)
	}
	s.mu.Unlock()
	rank := func(t *tstate) int {
		switch t.stage.Load() {
		case stFetch:
			return 0
		case stEncode:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].started.Before(out[j].started)
	})
	return out
}

const (
	dim   = "\x1b[2m"
	bold  = "\x1b[1m"
	green = "\x1b[32m"
	red   = "\x1b[31m"
	cyan  = "\x1b[36m"
	yel   = "\x1b[33m"
	reset = "\x1b[0m"
)

// bar draws a progress bar of the given width at fraction f (0..1).
func bar(f float64, width int, color string) string {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	full := int(f * float64(width))
	return color + strings.Repeat("█", full) + dim + strings.Repeat("░", width-full) + reset
}

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

	// overall
	frac := 0.0
	if e.total > 0 {
		frac = float64(finished) / float64(e.total)
	}
	eta := time.Duration(0)
	if elapsed := now.Sub(began); finished >= 3 && elapsed > 5*time.Second {
		perTrack := elapsed / time.Duration(finished)
		eta = perTrack * time.Duration(e.total-finished)
	}
	barW := min(30, max(10, width-60))
	head := fmt.Sprintf("  %s %s%d/%d%s  %3.0f%%   %s%s/s%s   ETA %s",
		bar(frac, barW, green), bold, finished, e.total, reset, frac*100,
		cyan, HumanBytes(int64(speed)), reset, humanETA(eta))
	if failed > 0 {
		head += fmt.Sprintf("   %s✗ %d%s", red, failed, reset)
	}
	rows := []string{head}

	// exits (only worth a row when there's more than one, or one is struggling)
	lanes := e.snap()
	if len(lanes) > 1 {
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
	} else if c := lanes[0].gov.Cooling(); c > 0 {
		rows = append(rows, fmt.Sprintf("  %srate limited, pausing %ds%s", yel, int(c.Seconds())+1, reset))
	}

	// tracks in flight: as many as fit, leaving room for scrolling output above
	room := min(8, max(0, height-len(rows)-6))
	snap := e.live.snapshot()
	shown := snap
	if len(shown) > room {
		shown = shown[:room]
	}
	tw := max(20, width-52)
	for _, ts := range shown {
		label := ansi.Truncate(ts.label, tw, "…")
		pad := strings.Repeat(" ", max(0, tw-ansi.StringWidth(label)))
		var state string
		switch ts.stage.Load() {
		case stFetch:
			got, total := ts.got.Load(), ts.total.Load()
			if total > 0 {
				state = fmt.Sprintf("%s %s/%s", bar(float64(got)/float64(total), 12, cyan), HumanBytes(got), HumanBytes(total))
			} else {
				state = dim + "downloading..." + reset
			}
			rows = append(rows, fmt.Sprintf("  %s↓%s %s%s  %s", cyan, reset, label, pad, state))
		case stEncode:
			rows = append(rows, fmt.Sprintf("  %s♪%s %s%s  %sconverting%s", yel, reset, label, pad, dim, reset))
		default:
			rows = append(rows, fmt.Sprintf("  %s%s%s %s%s  %slooking up%s", dim, spin(now), reset, label, pad, dim, reset))
		}
	}
	if extra := len(snap) - len(shown); extra > 0 {
		rows = append(rows, fmt.Sprintf("  %s+ %d more in progress%s", dim, extra, reset))
	}
	return rows
}
