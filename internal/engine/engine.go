// Package engine runs the download pipeline: resolve -> fetch -> encode, each
// stage with its own worker pool, throttled by an adaptive Governor.
package engine

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// Options configures a run.
type Options struct {
	Out      string
	Format   string
	Jobs     int // 0 = automatic
	Cookies  string
	Tools    deps.Tools
	Store    *store.Store
	UI       *ui.UI
	CacheDir string
}

// Summary is the result of a run.
type Summary struct {
	Done, Failed int
	Bytes        int64
	Elapsed      time.Duration
	Interrupted  bool
}

type job struct {
	tr     model.Track
	res    *Resolved
	src    string // downloaded source file
	client int
}

type engine struct {
	Options
	yt     ytdlp
	gov    *Governor
	tmp    string
	bytes  atomic.Int64
	done   atomic.Int64
	failed atomic.Int64
	active atomic.Int64
	total  int
}

const (
	maxTransient = 3
	maxThrottled = 8
)

// Run downloads every track in todo and records results in the store.
func Run(ctx context.Context, o Options, todo []model.Track) Summary {
	e := &engine{Options: o, total: len(todo)}
	e.yt = ytdlp{tools: o.Tools, cookies: o.Cookies, cache: o.CacheDir}
	start, maxC, gap := 3, 8, 600*time.Millisecond
	if o.Cookies != "" {
		start, maxC, gap = 4, 12, 400*time.Millisecond
	}
	if o.Jobs > 0 {
		maxC, start = o.Jobs, min(start, o.Jobs)
	}
	e.gov = NewGovernor(start, maxC, gap)
	e.tmp = filepath.Join(o.Out, ".ytm", "tmp")
	_ = os.RemoveAll(e.tmp)
	_ = os.MkdirAll(e.tmp, 0o755)
	defer os.RemoveAll(e.tmp)

	began := time.Now()
	resolveCh := make(chan model.Track)
	fetchCh := make(chan *job, 32)
	encodeCh := make(chan *job, 32)

	var rw, fw, ew sync.WaitGroup
	for i := 0; i < maxC; i++ {
		rw.Add(1)
		go func(slot int) { defer rw.Done(); e.resolveWorker(ctx, slot, resolveCh, fetchCh) }(i)
	}
	for i := 0; i < min(maxC, 6); i++ {
		fw.Add(1)
		go func() { defer fw.Done(); e.fetchWorker(ctx, fetchCh, encodeCh) }()
	}
	for i := 0; i < min(runtime.NumCPU(), 8); i++ {
		ew.Add(1)
		go func() { defer ew.Done(); e.encodeWorker(ctx, encodeCh) }()
	}

	tick := time.NewTicker(250 * time.Millisecond)
	stopStatus := make(chan struct{})
	go func() {
		var lastB int64
		lastT := time.Now()
		var speed float64
		for {
			select {
			case <-tick.C:
				now := time.Now()
				b := e.bytes.Load()
				inst := float64(b-lastB) / now.Sub(lastT).Seconds()
				speed = speed*0.7 + inst*0.3
				lastB, lastT = b, now
				e.status(speed)
			case <-stopStatus:
				return
			}
		}
	}()

feed:
	for _, t := range todo {
		select {
		case resolveCh <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(resolveCh)
	rw.Wait()
	close(fetchCh)
	fw.Wait()
	close(encodeCh)
	ew.Wait()
	tick.Stop()
	close(stopStatus)
	o.UI.ClearStatus()

	return Summary{
		Done: int(e.done.Load()), Failed: int(e.failed.Load()), Bytes: e.bytes.Load(),
		Elapsed: time.Since(began), Interrupted: ctx.Err() != nil,
	}
}

func (e *engine) status(speed float64) {
	finished := int(e.done.Load() + e.failed.Load())
	line := fmt.Sprintf("[%d/%d] %s/s  active %d  limit %d", finished, e.total, HumanBytes(int64(speed)), e.active.Load(), e.gov.Limit())
	if c := e.gov.Cooling(); c > 0 {
		line += fmt.Sprintf("  (rate limited, pausing %ds)", int(c.Seconds())+1)
	}
	e.UI.Status("%s", line)
}

// HumanBytes formats n as KB/MB/GB.
func HumanBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.0f KB", f/k)
	}
	return strconv.FormatInt(n, 10) + " B"
}

func (e *engine) fail(t model.Track, err error) {
	if ctxErr(err) {
		return
	}
	perm := kindOf(err) == kindPermanent
	_ = e.Store.MarkFailed(t.ID, err.Error(), perm)
	e.failed.Add(1)
	e.UI.Printf("✗ %s  (%s)", t.Label(), err)
}

func ctxErr(err error) bool { return err == context.Canceled || err == context.DeadlineExceeded }

// sleep waits d (plus jitter) or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	d += time.Duration(rand.Int63n(int64(d)/2 + 1))
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func (e *engine) resolveWorker(ctx context.Context, slot int, in <-chan model.Track, out chan<- *job) {
	for t := range in {
		if ctx.Err() != nil {
			continue
		}
		res, client, err := e.resolveWithRetry(ctx, t)
		if err != nil {
			e.fail(t, err)
			continue
		}
		e.active.Add(1)
		select {
		case out <- &job{tr: t, res: res, client: client}:
		case <-ctx.Done():
			e.active.Add(-1)
		}
	}
}

func (e *engine) resolveWithRetry(ctx context.Context, t model.Track) (*Resolved, int, error) {
	var err error
	transient, throttled := 0, 0
	for client := 0; ; client++ {
		if err = e.gov.Acquire(ctx); err != nil {
			return nil, 0, err
		}
		var res *Resolved
		res, err = e.yt.Resolve(ctx, t.ID, client)
		switch {
		case err == nil:
			e.gov.Release(OK)
			return res, client, nil
		case ctx.Err() != nil:
			e.gov.Release(Neutral)
			return nil, 0, ctx.Err()
		case kindOf(err) == kindPermanent:
			e.gov.Release(Neutral)
			return nil, 0, err
		case kindOf(err) == kindThrottled:
			e.gov.Release(Throttled)
			if throttled++; throttled >= maxThrottled {
				return nil, 0, err
			}
		default:
			e.gov.Release(Neutral)
			if transient++; transient >= maxTransient {
				return nil, 0, err
			}
			sleep(ctx, time.Duration(transient)*2*time.Second)
		}
	}
}

func (e *engine) fetchWorker(ctx context.Context, in <-chan *job, out chan<- *job) {
	for j := range in {
		err := e.fetchJob(ctx, j)
		if err != nil {
			e.active.Add(-1)
			e.fail(j.tr, err)
			continue
		}
		select {
		case out <- j:
		case <-ctx.Done():
			e.active.Add(-1)
			_ = os.Remove(j.src)
		}
	}
}

func (e *engine) fetchJob(ctx context.Context, j *job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if canFetch(j.res) {
		dst := filepath.Join(e.tmp, j.tr.ID+".src")
		err := fetch(ctx, j.res, dst, func(n int64) { e.bytes.Add(n) }, func() { e.gov.Observe(Throttled) })
		if err == nil {
			j.src = dst
			e.gov.Observe(OK)
			return nil
		}
		_ = os.Remove(dst)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Fall through: let yt-dlp download it the slow, robust way.
	}
	var err error
	for attempt := 0; attempt < maxTransient; attempt++ {
		if err = e.gov.Acquire(ctx); err != nil {
			return err
		}
		var p string
		p, err = e.yt.Download(ctx, j.tr.ID, e.tmp, j.client+attempt)
		if err == nil {
			e.gov.Release(OK)
			if st, serr := os.Stat(p); serr == nil {
				e.bytes.Add(st.Size())
			}
			j.src = p
			return nil
		}
		switch kindOf(err) {
		case kindPermanent:
			e.gov.Release(Neutral)
			return err
		case kindThrottled:
			e.gov.Release(Throttled)
		default:
			e.gov.Release(Neutral)
			sleep(ctx, 2*time.Second)
		}
	}
	return err
}

func (e *engine) encodeWorker(ctx context.Context, in <-chan *job) {
	for j := range in {
		path, err := e.encodeJob(ctx, j)
		e.active.Add(-1)
		_ = os.Remove(j.src)
		if err != nil {
			e.fail(j.tr, err)
			continue
		}
		_ = e.Store.MarkDone(j.tr.ID, path)
		e.done.Add(1)
		e.UI.Printf("✓ %s", j.tr.Label())
	}
}

// outputPath places a track at Artist/Album/Title [id].ext (Album omitted when unknown).
func outputPath(root string, t model.Track, r *Resolved, format string) (path string, meta map[string]string) {
	title := firstNonEmpty(t.Title, r.Title, t.ID)
	artists := t.Artists
	if len(artists) == 0 {
		artists = r.Artists
	}
	album := firstNonEmpty(t.Album, r.Album)
	parts := []string{root, names.Part(firstString(artists), "Unknown Artist")}
	if album != "" {
		parts = append(parts, names.Part(album, "Unknown Album"))
	}
	parts = append(parts, names.Part(title, t.ID)+" ["+t.ID+"]."+format)
	meta = map[string]string{
		"title":        title,
		"artist":       joinArtists(artists),
		"album_artist": firstString(artists),
		"album":        album,
		"comment":      "https://music.youtube.com/watch?v=" + t.ID,
	}
	// Keep everything YouTube knows; empty values are dropped by encodeArgs.
	meta["date"] = firstNonEmpty(r.Date, yearString(r.Year))
	meta["genre"] = r.Genre
	if r.Track > 0 {
		meta["track"] = strconv.Itoa(r.Track)
	}
	return filepath.Join(parts...), meta
}

func yearString(y int) string {
	if y <= 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func firstString(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func joinArtists(a []string) string {
	out := ""
	for i, s := range a {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

func (e *engine) encodeJob(ctx context.Context, j *job) (string, error) {
	final, meta := outputPath(e.Out, j.tr, j.res, e.Format)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", err
	}
	var cover string
	if j.res.Thumb != "" && e.Format != "opus" {
		c := filepath.Join(e.tmp, j.tr.ID+".cover")
		if err := getFile(ctx, j.res.Thumb, c); err == nil {
			cover = c
			defer os.Remove(c)
		}
	}
	tmpOut := filepath.Join(e.tmp, j.tr.ID+".out."+e.Format)
	defer os.Remove(tmpOut)
	err := runFFmpeg(ctx, e.Tools.FFmpeg, encodeArgs(j.src, cover, tmpOut, e.Format, j.res.Acodec, meta))
	if err != nil && cover != "" && ctx.Err() == nil { // bad artwork shouldn't lose the song
		err = runFFmpeg(ctx, e.Tools.FFmpeg, encodeArgs(j.src, "", tmpOut, e.Format, j.res.Acodec, meta))
	}
	if err != nil {
		return "", err
	}
	_ = os.Remove(final)
	if err := os.Rename(tmpOut, final); err != nil {
		return "", err
	}
	return final, nil
}
