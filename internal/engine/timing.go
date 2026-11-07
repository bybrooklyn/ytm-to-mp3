package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
)

// timings collects per-track stage durations for the MFY_DEBUG report, so a slow run can be
// pinned on a stage (looking up, waiting, downloading, converting) or an exit.
type timings struct {
	on   bool
	mu   sync.Mutex
	rows []trackTiming
}

type trackTiming struct {
	exit                          string
	via                           string
	resolve, queued, fetch, encod time.Duration
	bytes                         int64
}

func newTimings() *timings { return &timings{on: debugOn()} }

// debugOn: MFY_DEBUG is set, so per-exit detail is shown.
func debugOn() bool { return appname.Getenv("DEBUG") != "" }

// record notes a finished track. bytes is the size of the downloaded source.
func (t *timings) record(ts *tstate, exit string, bytes int64, end time.Time) {
	if !t.on || ts == nil || ts.resolvedAt.IsZero() || ts.fetchStart.IsZero() || ts.encodeStart.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = append(t.rows, trackTiming{
		exit: exit, via: lookupName(ts.lookup) + ts.via, bytes: bytes,
		resolve: ts.resolvedAt.Sub(ts.started),
		queued:  ts.fetchStart.Sub(ts.resolvedAt),
		fetch:   ts.encodeStart.Sub(ts.fetchStart),
		encod:   end.Sub(ts.encodeStart),
	})
}

// lookupName prefixes the download path with how the track was looked up.
func lookupName(l string) string {
	if l == "go" {
		return "go lookup, "
	}
	return ""
}

// report is the summary printed at the end of a run (empty when off or nothing finished).
func (t *timings) report() []string {
	if !t.on {
		return nil
	}
	t.mu.Lock()
	rows := append([]trackTiming(nil), t.rows...)
	t.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	pct := func(ds []time.Duration, p float64) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[min(len(ds)-1, int(float64(len(ds))*p))]
	}
	col := func(f func(trackTiming) time.Duration, rs []trackTiming) string {
		ds := make([]time.Duration, len(rs))
		for i, r := range rs {
			ds[i] = f(r)
		}
		return fmt.Sprintf("%6.1fs /%6.1fs", pct(ds, 0.5).Seconds(), pct(ds, 0.9).Seconds())
	}
	line := func(name string, rs []trackTiming) string {
		var b int64
		var ft time.Duration
		for _, r := range rs {
			b += r.bytes
			ft += r.fetch
		}
		rate := 0.0
		if ft > 0 {
			rate = float64(b) / ft.Seconds()
		}
		return fmt.Sprintf("  %-18s %4d  %s  %s  %s  %s  %9s/s",
			name, len(rs),
			col(func(r trackTiming) time.Duration { return r.resolve }, rs),
			col(func(r trackTiming) time.Duration { return r.queued }, rs),
			col(func(r trackTiming) time.Duration { return r.fetch }, rs),
			col(func(r trackTiming) time.Duration { return r.encod }, rs),
			HumanBytes(int64(rate)))
	}
	out := []string{
		"",
		"timing (median / p90 per track)",
		fmt.Sprintf("  %-18s %4s  %-15s  %-15s  %-15s  %-15s  %11s", "", "n", "look up", "queued", "download", "convert", "per download"),
		line("all", rows),
	}
	group := func(key func(trackTiming) string) {
		by := map[string][]trackTiming{}
		for _, r := range rows {
			by[key(r)] = append(by[key(r)], r)
		}
		keys := make([]string, 0, len(by))
		for k := range by {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, line(k, by[k]))
		}
	}
	group(func(r trackTiming) string { return "via " + r.via })
	group(func(r trackTiming) string { return strings.TrimSpace(r.exit) })
	return out
}
