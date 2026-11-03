// Package engine runs the download pipeline: resolve -> fetch -> encode, each
// stage with its own worker pool and bounded queue, throttled by an adaptive
// Governor per exit.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

// Options configures a run.
type Options struct {
	Out      string
	Format   string
	Jobs     int // 0 = automatic
	Tools    deps.Tools
	Store    *store.Store
	UI       *ui.UI
	CacheDir string
	// Exits are the network paths to spread work over (VPNs / proxies). Empty = the
	// normal connection. Each gets its own rate-limit governor.
	Exits []vpn.Exit
	// Replace, if set, swaps a rate-limited exit for one on a different server.
	Replace func(ctx context.Context, old vpn.Exit) (vpn.Exit, error)
	// Healthy, if set, is polled to catch tunnels that died silently.
	Healthy func(vpn.Exit) bool
	// OnBlocked, if set, is told when an exit is judged blocked (throttled, bot-checked,
	// refusing videos others play, or dead) just before it is swapped. reason is short:
	// "rate limited", "bot check", "unavailable", "stopped responding", ...
	OnBlocked func(ex vpn.Exit, reason string)
	// OnGood, if set, is told about exits that work: on an exit's first finished track and
	// then about every 30s with the exit's recent throughput in bytes/sec.
	OnGood func(ex vpn.Exit, bytesPerSec float64)
	// NewExits, if set, delivers more exits while the run is going (tunnels still
	// connecting in the background); each gets a lane as soon as it arrives. The sender
	// must stop sending when the run's context ends.
	NewExits <-chan vpn.Exit
	// BatchSize is how many videos one yt-dlp process resolves at a time
	// (0 = automatic, 1 = no batching).
	BatchSize int
}

// Summary is the result of a run.
type Summary struct {
	Done, Failed int
	Bytes        int64
	Elapsed      time.Duration
	Interrupted  bool
	PerExit      []ExitStat
	Blocked      int // times an exit was judged blocked
	Swaps        int // times a blocked/dead exit was replaced by another server
}

// ExitStat is what one exit contributed.
type ExitStat struct {
	Name    string
	Done    int
	Bytes   int64
	Blocked int // times this lane's exit was judged blocked
	Swaps   int // times this lane moved to a different server
}

type job struct {
	tr     model.Track
	res    *Resolved
	src    string // downloaded source file
	cover  string // cover art, fetched alongside the audio ("" = none)
	client int
	lane   *lane // the exit this track was resolved on; stream URLs are tied to its IP
	epoch  int64 // lane.epoch when resolved; if it changed, the stream URL is for an old IP
	ts     *tstate
}

// lane is one exit with its own governor, HTTP client and yt-dlp settings.
type lane struct {
	mu       sync.Mutex
	exit     vpn.Exit
	gov      *Governor
	client   *http.Client
	yt       ytdlp
	done     atomic.Int64
	bytes    atomic.Int64
	blocked  atomic.Int64
	swaps    atomic.Int64
	fetching atomic.Int32 // direct fetches in flight (so an idle lane doesn't look slow)
	bad      atomic.Int32 // consecutive throttles / failures
	swapping atomic.Bool
	epoch    atomic.Int64 // bumps on every exit swap; stream URLs are valid for one epoch only
	good     atomic.Bool  // OnGood has been told about this exit

	rmu   sync.Mutex
	rate  float64 // recent bytes/sec while busy
	lastB int64
	lastT time.Time
}

func (l *lane) http() *http.Client { l.mu.Lock(); defer l.mu.Unlock(); return l.client }
func (l *lane) ytdlp() ytdlp       { l.mu.Lock(); defer l.mu.Unlock(); return l.yt }

// sample folds the bytes moved since the last call into the lane's throughput estimate.
// Idle windows are ignored so a quiet lane isn't mistaken for a slow one.
func (l *lane) sample(now time.Time) {
	l.rmu.Lock()
	defer l.rmu.Unlock()
	b := l.bytes.Load()
	if l.lastT.IsZero() {
		l.lastB, l.lastT = b, now
		return
	}
	dt := now.Sub(l.lastT).Seconds()
	if dt <= 0 {
		return
	}
	d := b - l.lastB
	l.lastB, l.lastT = b, now
	if d == 0 && l.fetching.Load() == 0 {
		return
	}
	inst := float64(d) / dt
	if l.rate == 0 {
		l.rate = inst
	} else {
		l.rate = l.rate*0.8 + inst*0.2
	}
}

// speed is the lane's recent throughput in bytes/sec (0 = not measured yet).
func (l *lane) speed() float64 { l.rmu.Lock(); defer l.rmu.Unlock(); return l.rate }

func (l *lane) setSpeed(v float64) { l.rmu.Lock(); l.rate = v; l.rmu.Unlock() }

// sour records a bad outcome; after several in a row the exit is swapped for a fresh
// server (a different IP usually clears a rate limit).
func (e *engine) sour(ctx context.Context, l *lane, epoch int64) {
	e.sourWhy(ctx, l, epoch, "kept failing", 1)
}

// sourWhy is sour with a reason for the VPN layer and a weight (a bot check counts double).
func (e *engine) sourWhy(ctx context.Context, l *lane, epoch int64, reason string, weight int32) {
	if l.epoch.Load() != epoch {
		return // that failure belongs to an exit we've already replaced
	}
	if l.bad.Add(weight) < 4 {
		return
	}
	e.swap(ctx, l, reason)
}

func (e *engine) swap(ctx context.Context, l *lane, why string) {
	if !l.swapping.CompareAndSwap(false, true) {
		return
	}
	defer l.swapping.Store(false)
	l.bad.Store(0)
	l.mu.Lock()
	old := l.exit
	l.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	l.blocked.Add(1)
	e.nBlocked.Add(1)
	if e.OnBlocked != nil {
		e.OnBlocked(old, why)
	}
	if e.Replace == nil {
		return
	}
	ex, err := e.Replace(ctx, old)
	if err != nil {
		return
	}
	cl, err := vpn.NewClient(ex.Proxy)
	if err != nil {
		return
	}
	l.mu.Lock()
	oldClient := l.client
	l.exit, l.client = ex, cl
	l.yt.proxy = ex.Proxy
	l.mu.Unlock()
	l.epoch.Add(1)
	l.swaps.Add(1)
	e.nSwaps.Add(1)
	l.good.Store(false)
	l.setSpeed(0)
	l.gov.Reset(e.startLimit) // a fresh IP starts with a clean slate
	oldClient.CloseIdleConnections()
	if ctx.Err() == nil {
		e.UI.Line(yel+"↻"+reset, fmt.Sprintf("%s blocked (%s), switched to %s", ui.Sanitize(old.Name), why, ui.Sanitize(ex.Name)))
	}
}

// snap returns the current lanes. The slice is append-only, so the returned header stays
// valid (and race-free) while lanes are added concurrently.
func (e *engine) snap() []*lane {
	e.lmu.RLock()
	defer e.lmu.RUnlock()
	return e.lanes
}

// pick chooses the best exit that isn't cooling down, avoiding the `avoid` lanes if
// possible. Score = load (in-flight / limit) plus a penalty for exits that measure slower
// than the fastest one, so idle fast exits win ties and slow exits get less new work.
func (e *engine) pick(avoid ...*lane) *lane {
	lanes := e.snap()
	var maxRate float64
	for _, l := range lanes {
		maxRate = max(maxRate, l.speed())
	}
	var best *lane
	var bestScore float64
	for _, l := range lanes {
		score := l.gov.Load()
		if r := l.speed(); r > 0 && maxRate > 0 {
			score += 0.5 * (1 - r/maxRate)
		}
		if c := l.gov.Cooling(); c > 0 {
			score += 10 + c.Seconds()
		}
		if l.swapping.Load() {
			score += 100 // being replaced right now
		}
		if len(lanes) > 1 {
			for _, a := range avoid {
				if l == a {
					score += 5
					break
				}
			}
		}
		if best == nil || score < bestScore {
			best, bestScore = l, score
		}
	}
	return best
}

type engine struct {
	Options
	lmu        sync.RWMutex
	lanes      []*lane
	tmp        string
	bytes      atomic.Int64
	done       atomic.Int64
	failed     atomic.Int64
	active     atomic.Int64
	nBlocked   atomic.Int64
	nSwaps     atomic.Int64
	total      int
	live       liveSet      // in-flight tracks, for the live display
	goodClient atomic.Int32 // the player client that last gave a fetchable URL

	startLimit int            // initial per-exit concurrency, restored when an exit is swapped
	laneMax    int            // per-exit concurrency ceiling
	bg         sync.WaitGroup // health probes / swaps / hooks; joined before Run returns
	began      time.Time

	pending atomic.Int64 // tracks not yet picked up by a resolver
	slots   atomic.Int64 // resolve workers running

	// pipeline plumbing, kept so lanes can be added while the run is going
	pctx      context.Context
	resolveCh chan model.Track
	fetchCh   chan *job
	encodeCh  chan *job
	rw, fw    sync.WaitGroup
	gmu       sync.Mutex

	resolversLive, fetchersLive int // guarded by gmu
}

// laneGap is the minimum spacing between request starts on one exit (a var for tests).
var laneGap = 600 * time.Millisecond

const (
	maxTransient = 3
	maxThrottled = 8
	maxBatch     = 6
)

// Run downloads every track in todo and records results in the store.
func Run(ctx context.Context, o Options, todo []model.Track) Summary {
	// Ids end up in URLs, file names and yt-dlp templates: nothing malformed gets past here.
	valid := make([]model.Track, 0, len(todo))
	dropped := 0
	for _, t := range todo {
		if model.ValidID(t.ID) {
			valid = append(valid, t)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		o.UI.Printf("warning: ignoring %d track(s) with an invalid video id", dropped)
	}
	todo = valid
	if len(todo) == 0 {
		return Summary{}
	}
	e := &engine{Options: o, total: len(todo), pctx: ctx}
	exits := o.Exits
	if len(exits) == 0 && o.NewExits == nil {
		exits = []vpn.Exit{vpn.Direct}
	}
	start, laneMax := 3, 8
	if o.Jobs > 0 {
		laneMax = max(1, (o.Jobs+max(1, len(exits))-1)/max(1, len(exits)))
		start = min(start, laneMax)
	}
	e.startLimit, e.laneMax = start, laneMax
	for _, x := range exits {
		if _, err := e.newLane(x); err != nil {
			o.UI.Printf("skipping exit %s: %v", x.Name, err)
		}
	}
	// Exits still connecting: wait for the first one rather than starting with nothing.
	for len(e.snap()) == 0 && o.NewExits != nil {
		select {
		case x, ok := <-o.NewExits:
			if !ok {
				return Summary{Failed: len(todo)}
			}
			if _, err := e.newLane(x); err != nil {
				o.UI.Printf("skipping exit %s: %v", x.Name, err)
			}
		case <-ctx.Done():
			return Summary{Failed: len(todo), Interrupted: true}
		}
	}
	if len(e.snap()) == 0 {
		return Summary{Failed: len(todo)}
	}
	// A unique private directory per run: never delete another running instance's files.
	tmp, err := store.NewTempDir(o.Out)
	if err != nil {
		o.UI.Printf("cannot create a temp directory in %s: %v", o.Out, err)
		return Summary{Failed: len(todo)}
	}
	e.tmp = tmp
	defer os.RemoveAll(e.tmp)

	e.began = time.Now()
	began := e.began
	e.pending.Store(int64(len(todo)))
	// Bounded queues between the stages: resolvers run ahead of the fetchers by at most
	// the queue depth (so URLs stay fresh), and a full queue backs the resolvers off.
	e.resolveCh = make(chan model.Track, 256)
	e.fetchCh = make(chan *job, 64)
	e.encodeCh = make(chan *job, 64)

	n0 := len(e.snap())
	maxC := n0 * laneMax
	e.gmu.Lock()
	for i := 0; i < maxC; i++ {
		e.startResolver()
	}
	for i := 0; i < min(maxC, 6*n0+2); i++ {
		e.startFetcher()
	}
	e.gmu.Unlock()
	var ew sync.WaitGroup
	for i := 0; i < max(2, min(runtime.GOMAXPROCS(0), 8)); i++ {
		ew.Add(1)
		go func() { defer ew.Done(); e.encodeWorker(ctx, e.encodeCh) }()
	}

	// Exits that arrive while we run get their own lane and workers.
	stopGrow := make(chan struct{})
	growDone := make(chan struct{})
	go func() {
		defer close(growDone)
		if o.NewExits == nil {
			return
		}
		for {
			select {
			case x, ok := <-o.NewExits:
				if !ok {
					return
				}
				e.addLane(x)
			case <-stopGrow:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(250 * time.Millisecond)
	stopStatus := make(chan struct{})
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		var lastB int64
		lastCheck := time.Now().Add(30 * time.Second) // give new tunnels time to handshake
		lastGood := time.Now()
		lastT := time.Now()
		var speed float64
		for {
			select {
			case now := <-tick.C:
				lanes := e.snap()
				for _, l := range lanes {
					l.sample(now)
				}
				if e.Healthy != nil && time.Since(lastCheck) > 20*time.Second {
					lastCheck = time.Now()
					for _, l := range lanes {
						e.probeAsync(ctx, l)
					}
				}
				if e.OnGood != nil && time.Since(lastGood) > 30*time.Second {
					lastGood = time.Now()
					for _, l := range lanes {
						if r := l.speed(); r > 0 && l.fetching.Load() > 0 && !l.swapping.Load() {
							e.tellGood(l, r)
						}
					}
				}
				b := e.bytes.Load()
				inst := float64(b-lastB) / now.Sub(lastT).Seconds()
				speed = speed*0.7 + inst*0.3
				lastB, lastT = b, now
				e.status(speed, began)
			case <-stopStatus:
				return
			}
		}
	}()

feed:
	for _, t := range todo {
		select {
		case e.resolveCh <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(e.resolveCh)
	e.rw.Wait()
	close(e.fetchCh)
	e.fw.Wait()
	close(e.encodeCh)
	ew.Wait()
	close(stopGrow)
	<-growDone
	tick.Stop()
	close(stopStatus)
	<-statusDone
	e.bg.Wait() // health probes / swaps must not outlive Run (the tunnels are closed after it)
	o.UI.ClearStatus()
	lanes := e.snap()
	for _, l := range lanes {
		l.http().CloseIdleConnections()
	}

	var per []ExitStat
	for _, l := range lanes {
		per = append(per, ExitStat{Name: l.exitName(), Done: int(l.done.Load()), Bytes: l.bytes.Load(),
			Blocked: int(l.blocked.Load()), Swaps: int(l.swaps.Load())})
	}
	return Summary{
		PerExit: per,
		Done:    int(e.done.Load()), Failed: int(e.failed.Load()), Bytes: e.bytes.Load(),
		Elapsed: time.Since(began), Interrupted: ctx.Err() != nil,
		Blocked: int(e.nBlocked.Load()), Swaps: int(e.nSwaps.Load()),
	}
}

// newLane builds a lane for x and registers it.
func (e *engine) newLane(x vpn.Exit) (*lane, error) {
	cl, err := vpn.NewClient(x.Proxy)
	if err != nil {
		return nil, err
	}
	l := &lane{
		exit: x, gov: NewGovernor(e.startLimit, e.laneMax, laneGap), client: cl,
		yt: ytdlp{tools: e.Tools, cache: e.CacheDir, proxy: x.Proxy},
	}
	e.lmu.Lock()
	e.lanes = append(e.lanes, l)
	e.lmu.Unlock()
	return l, nil
}

// addLane adds an exit that showed up mid-run and starts workers to use it.
func (e *engine) addLane(x vpn.Exit) {
	for _, l := range e.snap() {
		if x.Proxy != "" && l.exitCopy().Proxy == x.Proxy {
			return // already have it
		}
	}
	if _, err := e.newLane(x); err != nil {
		e.UI.Printf("skipping exit %s: %v", x.Name, err)
		return
	}
	e.gmu.Lock()
	// Workers are only added to a stage that still has live workers: adding to a
	// WaitGroup that may already be at zero while Wait runs is not allowed.
	if e.resolversLive > 0 {
		for i := 0; i < e.laneMax; i++ {
			e.startResolver()
		}
	}
	if e.fetchersLive > 0 {
		for i := 0; i < min(e.laneMax, 6); i++ {
			e.startFetcher()
		}
	}
	e.gmu.Unlock()
	if e.pctx.Err() == nil {
		e.UI.Line(cyan+"+"+reset, "new exit "+ui.Sanitize(x.Name))
	}
}

// startResolver / startFetcher add a worker; the caller holds e.gmu.
func (e *engine) startResolver() {
	e.rw.Add(1)
	e.slots.Add(1)
	e.resolversLive++
	go func() {
		defer e.rw.Done()
		defer func() { e.gmu.Lock(); e.resolversLive--; e.gmu.Unlock(); e.slots.Add(-1) }()
		e.resolveWorker(e.pctx, e.resolveCh, e.fetchCh)
	}()
}

func (e *engine) startFetcher() {
	e.fw.Add(1)
	e.fetchersLive++
	go func() {
		defer e.fw.Done()
		defer func() { e.gmu.Lock(); e.fetchersLive--; e.gmu.Unlock() }()
		e.fetchWorker(e.pctx, e.fetchCh, e.encodeCh)
	}()
}

// tellGood reports a working exit to the VPN layer without blocking the caller.
func (e *engine) tellGood(l *lane, rate float64) {
	if e.OnGood == nil {
		return
	}
	ex := l.exitCopy()
	e.bg.Add(1)
	go func() { defer e.bg.Done(); e.OnGood(ex, rate) }()
}

func (e *engine) status(speed float64, began time.Time) {
	e.UI.Block(e.liveRows(speed, began))
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
	e.live.remove(t.ID)
	if ctxErr(err) {
		return
	}
	perm := kindOf(err) == kindPermanent
	if serr := e.Store.MarkFailed(t.ID, names.Clean(err.Error()), perm); serr != nil {
		e.UI.Printf("warning: couldn't record failure for %s: %v", t.ID, serr)
	}
	e.failed.Add(1)
	e.UI.Line(red+"✗"+reset, t.Label()+"  ("+names.Clean(err.Error())+")")
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

// batchSize is how many tracks the next resolve should take: up to maxBatch, fewer near
// the end of the run so the tail spreads over all resolvers.
func (e *engine) batchSize() int {
	cap := maxBatch
	if e.BatchSize > 0 {
		cap = e.BatchSize
	}
	if cap <= 1 {
		return 1
	}
	// Parallelism that can actually run now: the exits' current limits, not the idle workers.
	var conc int64
	for _, l := range e.snap() {
		conc += int64(l.gov.Limit())
	}
	slots := max(1, min(conc, e.slots.Load()))
	n := int((e.pending.Load() + slots - 1) / slots)
	return min(cap, max(1, n))
}

func (e *engine) resolveWorker(ctx context.Context, in <-chan model.Track, out chan<- *job) {
	for t := range in {
		if ctx.Err() != nil {
			e.pending.Add(-1)
			continue
		}
		batch := []model.Track{t}
		for want := e.batchSize(); len(batch) < want; {
			select {
			case t2, ok := <-in:
				if !ok {
					want = 0
					break
				}
				batch = append(batch, t2)
				continue
			default:
			}
			break
		}
		e.pending.Add(-int64(len(batch)))
		e.resolveBatch(ctx, batch, out)
	}
}

// attempt is a resolve outcome already obtained (by a batch) and accounted for.
type attempt struct {
	ln    *lane
	epoch int64
	err   error
}

// resolveBatch resolves tracks with one yt-dlp process, hands finished ones on, and sends
// the ones that failed (or got no recognisable output) through the per-track retry path,
// which moves them to other exits.
func (e *engine) resolveBatch(ctx context.Context, batch []model.Track, out chan<- *job) {
	type state struct {
		t  model.Track
		ts *tstate
	}
	sts := make([]state, len(batch))
	for i, t := range batch {
		sts[i] = state{t, e.live.add(t)}
	}
	emit := func(s state, res *Resolved, client int, ln *lane, epoch int64) {
		s.ts.stage.Store(stFetch)
		if canFetch(res) {
			s.ts.total.Store(contentLength(res))
		}
		e.active.Add(1)
		select {
		case out <- &job{tr: s.t, res: res, client: client, lane: ln, epoch: epoch, ts: s.ts}:
		case <-ctx.Done():
			e.live.remove(s.t.ID)
			e.active.Add(-1)
		}
	}
	single := func(s state, first *attempt) {
		res, client, ln, epoch, err := e.resolveWithRetry(ctx, s.t, first)
		if err != nil {
			e.fail(s.t, err)
			return
		}
		emit(s, res, client, ln, epoch)
	}
	if len(batch) == 1 {
		single(sts[0], nil)
		return
	}

	ln := e.pick()
	if err := ln.gov.Acquire(ctx); err != nil {
		for _, s := range sts {
			e.live.remove(s.t.ID)
		}
		return
	}
	epoch := ln.epoch.Load()
	idx := int(e.goodClient.Load())
	ids := make([]string, len(batch))
	for i, t := range batch {
		ids[i] = t.ID
	}
	results, berr := ln.ytdlp().ResolveBatch(ctx, ids, idx)
	if berr != nil || ctx.Err() != nil {
		ln.gov.Release(Neutral)
		if ctx.Err() != nil {
			for _, s := range sts {
				e.live.remove(s.t.ID)
			}
			return
		}
		for _, s := range sts {
			single(s, nil)
		}
		return
	}
	// One governor verdict for the whole process.
	okN, throttled := 0, 0
	bot := false
	for _, r := range results {
		switch {
		case r.err == nil:
			okN++
		case kindOf(r.err) == kindThrottled:
			throttled++
			bot = bot || strings.Contains(strings.ToLower(r.err.Error()), "not a bot")
		}
	}
	switch {
	case okN > 0:
		ln.gov.Release(OK)
		ln.bad.Store(0)
	case throttled > 0:
		ln.gov.Release(Throttled)
	default:
		ln.gov.Release(Neutral)
	}
	if throttled > 0 && okN == 0 {
		e.sourWhy(ctx, ln, epoch, throttleReason(bot), throttleWeight(bot))
	}
	var retry []state
	var retryA []*attempt
	for _, s := range sts {
		r, ok := results[s.t.ID]
		switch {
		case !ok:
			retry = append(retry, s) // no recognisable output: resolve it alone
			retryA = append(retryA, nil)
		case r.err == nil:
			emit(s, r.res, idx, ln, epoch)
		default:
			retry = append(retry, s)
			retryA = append(retryA, &attempt{ln, epoch, r.err})
		}
	}
	for i, s := range retry {
		single(s, retryA[i])
	}
}

func throttleReason(bot bool) string {
	if bot {
		return "bot check"
	}
	return "rate limited"
}

func throttleWeight(bot bool) int32 {
	if bot {
		return 2
	}
	return 1
}

type suspect struct {
	l     *lane
	epoch int64
}

// resolveWithRetry resolves one track, moving it between exits as they fail. first, if
// non-nil, is an outcome the caller already obtained and accounted for (a batch member).
// A block (throttle / bot check / "unavailable") on an exit sends the track to a different
// exit at once without spending its retry budget; the budget only counts once every exit
// has had its turn.
func (e *engine) resolveWithRetry(ctx context.Context, t model.Track, first *attempt) (*Resolved, int, *lane, int64, error) {
	var err error
	var avoid []*lane
	tried := map[*lane]bool{}
	transient, throttled, netFails, permanent := 0, 0, 0, 0
	var suspects []suspect // exits that said "unavailable" for this track
	for client := 0; ; client++ {
		var ln *lane
		var epoch int64
		var res *Resolved
		idx := int(e.goodClient.Load()) + client
		pre := first != nil // outcome already booked by the batch (governor, souring)
		if pre {
			ln, epoch, err = first.ln, first.epoch, first.err
			first = nil
		} else {
			ln = e.pick(avoid...)
			if err = ln.gov.Acquire(ctx); err != nil {
				return nil, 0, nil, 0, err
			}
			epoch = ln.epoch.Load()
			res, err = ln.ytdlp().Resolve(ctx, t.ID, idx)
		}
		release := func(o Outcome) {
			if !pre {
				ln.gov.Release(o)
			}
		}
		// A lane is "fresh" for this track if it hasn't blocked it yet; moving to a fresh
		// lane is free, only a repeat visit spends budget.
		fresh := len(e.snap()) > 1 && !tried[ln]
		tried[ln] = true
		switch {
		case err == nil:
			release(OK)
			ln.bad.Store(0)
			// Another exit played what these refused: they're being blocked, not the video
			// being gone. Two strikes each and they're replaced with a fresh server.
			for _, sp := range suspects {
				if sp.l != ln {
					e.sourWhy(ctx, sp.l, sp.epoch, "unavailable", 2)
				}
			}
			return res, idx, ln, epoch, nil
		case ctx.Err() != nil:
			release(Neutral)
			return nil, 0, nil, 0, ctx.Err()
		case kindOf(err) == kindPermanent:
			release(Neutral)
			// "Unavailable" is often just this exit's country or IP. Only believe it once
			// other exits (other countries) agree, so a track isn't written off for good
			// on one server's say-so.
			if len(e.snap()) > 1 && permanent < 2 {
				permanent++
				suspects = append(suspects, suspect{ln, epoch})
				avoid = append(avoid, ln)
				client-- // same player client on the next exit: this isn't a client problem
				continue
			}
			return nil, 0, nil, 0, err
		case kindOf(err) == kindThrottled:
			bot := strings.Contains(strings.ToLower(err.Error()), "not a bot")
			if !pre {
				ln.gov.Release(Throttled)
				e.sourWhy(ctx, ln, epoch, throttleReason(bot), throttleWeight(bot))
			}
			avoid = append(avoid, ln) // try another exit next, right away
			if fresh {
				client--
				continue
			}
			if throttled++; throttled >= maxThrottled {
				return nil, 0, nil, 0, err
			}
		case looksLikeNetwork(err):
			// A dead tunnel is the exit's fault, not the track's: charge the exit, not the
			// track's retry budget, and try another exit (bounded, so we still give up).
			release(Neutral)
			if !pre {
				e.sour(ctx, ln, epoch)
			}
			avoid = append(avoid, ln)
			if netFails++; netFails >= 3*maxTransient {
				return nil, 0, nil, 0, err
			}
			sleep(ctx, 2*time.Second)
		default:
			release(Neutral)
			avoid = append(avoid, ln)
			if transient++; transient >= maxTransient {
				return nil, 0, nil, 0, err
			}
			sleep(ctx, time.Duration(transient)*2*time.Second)
		}
	}
}

func (e *engine) fetchWorker(ctx context.Context, in <-chan *job, out chan<- *job) {
	for j := range in {
		// Cover art comes down alongside the audio, not after it.
		coverDone := e.startCover(ctx, j)
		err := e.fetchJob(ctx, j)
		if coverDone != nil {
			<-coverDone
		}
		if err != nil {
			e.active.Add(-1)
			if j.cover != "" {
				_ = os.Remove(j.cover)
			}
			e.fail(j.tr, err)
			continue
		}
		select {
		case out <- j:
		case <-ctx.Done():
			e.active.Add(-1)
			_ = os.Remove(j.src)
			if j.cover != "" {
				_ = os.Remove(j.cover)
			}
		}
	}
}

// startCover begins fetching j's artwork in the background; the returned channel closes
// when it is done (j.cover is set on success). nil = nothing to fetch.
func (e *engine) startCover(ctx context.Context, j *job) <-chan struct{} {
	if j.res.Thumb == "" || e.Format == "opus" {
		return nil
	}
	done := make(chan struct{})
	client, thumb := j.lane.http(), j.res.Thumb
	c := filepath.Join(e.tmp, j.tr.ID+".cover")
	go func() {
		defer close(done)
		if getFile(ctx, client, thumb, c) == nil {
			j.cover = c
		} else {
			_ = os.Remove(c)
		}
	}()
	return done
}

func (e *engine) fetchJob(ctx context.Context, j *job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A stream URL is only valid from the IP it was resolved on; if the exit was swapped
	// since, skip straight to the yt-dlp path (which resolves afresh).
	if canFetch(j.res) && j.lane.epoch.Load() == j.epoch {
		for hop := 0; ; hop++ {
			err := e.tryDirect(ctx, j)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !errors.Is(err, errStalled) || hop >= 2 || !e.moveToAnotherExit(ctx, j) {
				break
			}
		}
		// Fall through: let yt-dlp download it the slow, robust way.
	}
	j.ts.total.Store(0) // size unknown while yt-dlp does the download
	j.ts.got.Store(0)
	var err error
	for attempt := 0; attempt < maxTransient; attempt++ {
		// yt-dlp resolves from scratch, so any healthy exit will do; don't sit in the
		// queue of a resting one while other exits are idle.
		ln := j.lane
		if ln.gov.Cooling() > 0 || ln.swapping.Load() || attempt > 0 {
			ln = e.pick(j.lane)
		}
		j.lane = ln
		if err = ln.gov.Acquire(ctx); err != nil {
			return err
		}
		var p string
		p, err = ln.ytdlp().Download(ctx, j.tr.ID, e.tmp, j.client+attempt)
		if err == nil {
			ln.gov.Release(OK)
			if st, serr := os.Stat(p); serr == nil {
				e.bytes.Add(st.Size())
				ln.bytes.Add(st.Size())
			}
			j.src = p
			// yt-dlp chose its own format, which may differ from the one resolved earlier;
			// the encoder must copy only what the file really holds.
			r := *j.res
			r.Acodec = codecFromExt(p)
			j.res = &r
			return nil
		}
		switch kindOf(err) {
		case kindPermanent:
			ln.gov.Release(Neutral)
			return err
		case kindThrottled:
			ln.gov.Release(Throttled)
		default:
			ln.gov.Release(Neutral)
			sleep(ctx, 2*time.Second)
		}
	}
	return err
}

// altClients is how many other YouTube player clients to try when Google refuses to serve
// a stream URL to a plain HTTP client (which clients hand out fetchable URLs changes with
// yt-dlp versions, so ytm learns it instead of hard-coding it).
const altClients = 2

// tryDirect downloads j.res itself. On a refusal (403/404) it re-resolves with the next
// player client and tries again, remembering which client worked for the following tracks.
func (e *engine) tryDirect(ctx context.Context, j *job) error {
	dst := filepath.Join(e.tmp, j.tr.ID+".src")
	res, cur := j.res, j.client // j itself keeps its original client for the yt-dlp fallback
	var err error
	for k := 0; k <= altClients; k++ {
		if k > 0 {
			ln := j.lane
			if aerr := ln.gov.Acquire(ctx); aerr != nil {
				return aerr
			}
			cur++
			r2, rerr := ln.ytdlp().Resolve(ctx, j.tr.ID, cur)
			ln.gov.Release(Neutral)
			if rerr != nil || !canFetch(r2) || ln.epoch.Load() != j.epoch {
				continue // this client can't serve it; try the next
			}
			res = r2
			j.ts.total.Store(contentLength(res))
		}
		j.ts.got.Store(0)
		ln := j.lane
		ln.fetching.Add(1)
		err = fetch(ctx, ln.http(), res, dst, func(n int64) { e.bytes.Add(n); ln.bytes.Add(n); j.ts.got.Add(n) }, func() { ln.gov.Observe(Throttled) })
		ln.fetching.Add(-1)
		if err == nil {
			j.src, j.res = dst, res // the encoder needs the codec of the stream actually fetched
			ln.gov.Observe(OK)
			e.goodClient.Store(int32(cur))
			return nil
		}
		_ = os.Remove(dst)
		if os.Getenv("YTM_DEBUG") != "" {
			e.UI.Printf("debug: direct fetch of %s with client %d failed: %v", j.tr.ID, cur, err)
		}
		if errors.Is(err, errStalled) {
			// This exit isn't delivering data from Google's CDN. Count it hard against the
			// exit (two of these swap it) and let the caller move the track elsewhere.
			e.sourWhy(ctx, ln, j.epoch, "stalled", 2)
			return err
		}
		if !errors.Is(err, errUnfetchable) || ctx.Err() != nil {
			return err // only a refusal is worth another client
		}
	}
	if err == nil {
		err = errUnfetchable // every alternate client failed to resolve
	}
	return err
}

// moveToAnotherExit re-resolves j on a different exit (stream URLs are bound to the IP they
// were resolved from) so its direct fetch can be retried there. False if there is no other
// exit or it couldn't resolve.
func (e *engine) moveToAnotherExit(ctx context.Context, j *job) bool {
	ln := e.pick(j.lane)
	if ln == nil || ln == j.lane {
		return false
	}
	if err := ln.gov.Acquire(ctx); err != nil {
		return false
	}
	epoch := ln.epoch.Load()
	idx := int(e.goodClient.Load())
	res, err := ln.ytdlp().Resolve(ctx, j.tr.ID, idx)
	if err != nil {
		ln.gov.Release(Neutral)
		return false
	}
	ln.gov.Release(OK)
	if !canFetch(res) {
		return false
	}
	j.lane, j.res, j.epoch, j.client = ln, res, epoch, idx
	j.ts.total.Store(contentLength(res))
	return true
}

func (e *engine) encodeWorker(ctx context.Context, in <-chan *job) {
	for j := range in {
		j.ts.stage.Store(stEncode)
		path, err := e.encodeJob(ctx, j)
		e.live.remove(j.tr.ID)
		e.active.Add(-1)
		_ = os.Remove(j.src)
		if err != nil {
			e.fail(j.tr, err)
			continue
		}
		if serr := e.Store.MarkDone(j.tr.ID, path); serr != nil {
			e.UI.Printf("warning: couldn't record %s as done (it may download again next run): %v", j.tr.ID, serr)
		}
		e.done.Add(1)
		j.lane.done.Add(1)
		e.UI.Line(green+"✓"+reset, j.tr.Label())
		if e.OnGood != nil && j.lane.good.CompareAndSwap(false, true) {
			rate := j.lane.speed()
			if rate == 0 {
				if s := time.Since(e.began).Seconds(); s > 0 {
					rate = float64(j.lane.bytes.Load()) / s
				}
			}
			e.tellGood(j.lane, rate)
		}
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
	parts = append(parts, names.PartWithSuffix(title, t.ID, " ["+t.ID+"]."+format))
	meta = map[string]string{
		"title":        names.StripControl(title),
		"artist":       names.StripControl(joinArtists(artists)),
		"album_artist": names.StripControl(firstString(artists)),
		"album":        names.StripControl(album),
		"comment":      "https://music.youtube.com/watch?v=" + t.ID,
	}
	// Keep everything YouTube knows; empty values are dropped by encodeArgs.
	meta["date"] = firstNonEmpty(r.Date, yearString(r.Year))
	meta["genre"] = names.StripControl(r.Genre)
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
	cover := j.cover // fetched while the audio downloaded
	if cover != "" {
		defer os.Remove(cover)
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
	if err := store.Replace(tmpOut, final); err != nil { // atomic replace on unix
		return "", err
	}
	return final, nil
}

func (l *lane) exitName() string { l.mu.Lock(); defer l.mu.Unlock(); return l.exit.Name }

// probeAsync checks a lane's tunnel off the status goroutine and swaps it if it died.
// Run waits for these (e.bg) before returning, so none outlives the tunnels.
func (e *engine) probeAsync(ctx context.Context, l *lane) {
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		if ctx.Err() == nil && !e.Healthy(l.exitCopy()) {
			e.swap(ctx, l, "stopped responding")
		}
	}()
}

func (l *lane) exitCopy() vpn.Exit { l.mu.Lock(); defer l.mu.Unlock(); return l.exit }

// looksLikeNetwork spots failures that point at a dead tunnel/proxy rather than a bad video.
func looksLikeNetwork(err error) bool {
	m := strings.ToLower(err.Error())
	for _, k := range []string{"proxy", "socks", "timed out", "timeout", "connection", "unreachable", "network", "eof", "tls"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// codecFromExt guesses the audio codec yt-dlp downloaded from the file extension, for the
// common YouTube audio containers. "" means unknown (the encoder then re-encodes).
func codecFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".webm", ".opus", ".ogg":
		return "opus"
	case ".m4a", ".mp4":
		return "mp4a"
	}
	return ""
}
