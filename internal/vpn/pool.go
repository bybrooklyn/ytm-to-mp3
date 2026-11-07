package vpn

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/proton"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider"
)

// Options tunes OpenWith. The zero value behaves like Open.
type Options struct {
	// Countries limits servers to these country codes ("US", "NL", ...). Empty = automatic.
	// If none of the listed countries has a usable server, all countries are used.
	Countries []string
	// MinReady makes OpenWith return as soon as this many tunnels are up while the rest
	// keep connecting in the background (new exits then appear in Pool.Exits and are
	// announced through OnExit). 0 (or >= n) waits for all n, like Open.
	MinReady int
	// OnExit is called for every exit that becomes ready after OpenWith has returned
	// (background connects only; not for replacements, which Replace returns). It may be
	// called concurrently with the caller's first Exits(), so de-duplicate by Exit.Name.
	OnExit func(Exit)
	// Spares is how many already-handshaken standby tunnels to keep so Replace is
	// near-instant. 0 means the default (1); negative means none. Spares count against the
	// plan's device limit, so they are only opened when the limit is known and has room.
	Spares int
	// CachePath is the server cache file. "" means ytm/servers.json in the user cache
	// dir; "off" disables persistence.
	CachePath string

	dial func(context.Context, *proton.WGKey, proton.Server) (*Tunnel, error) // tests only
}

// Pool is a set of tunnels built from one Proton account, with spare servers to swap in.
type Pool struct {
	mu         sync.Mutex
	keys       *keyRing // one WireGuard identity per tunnel
	cache      *Cache
	servers    []proton.Server
	tunnels    map[string]*Tunnel // active, by exit name
	warm       []*Tunnel          // connected, idle standbys
	connecting map[string]bool    // server names being dialled
	tried      map[string]bool    // servers that failed this session
	failedCC   map[string]int
	known      map[string]proton.Server
	reasons    map[string]string // exit name -> why it was last blocked
	closed     bool
	returned   bool // OpenWith has handed the pool to the caller
	refilling  bool
	seq        int
	maxConnect int
	spares     int
	countries  []string
	home       *proton.Location // where the user is, for nearby-first ranking (nil = unknown)
	onExit     func(Exit)
	logf       func(string, ...any)
	label      string // the provider's name for messages
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	// dial brings up one tunnel (Options.dial in tests).
	dial func(context.Context, *proton.WGKey, proton.Server) (*Tunnel, error)
}

var errNoSpare = errors.New("no spare servers")

// keySlots is how many WireGuard identities the pool may use: one per tunnel that can be
// up or connecting at once (the plan's device limit when known), plus room for the extra
// candidate dialled while racing two servers.
func keySlots(maxConnect, n, spares int) int {
	if maxConnect > 0 {
		return maxConnect + 1
	}
	return n + spares + 2
}

// quickAPI bounds the Proton calls that only improve server choice (where the user is, a
// list with locations): startup never waits on them for long.
var quickAPI = 2 * time.Second

// migrateAPI bounds the one-time fetch of a server list with locations, replacing a list
// saved by an older version (the full list is a few MB).
var migrateAPI = 15 * time.Second

// Open connects n tunnels to different countries and returns once all are up. Servers
// that fail are replaced from the candidate list; it errors only if none work. It uses
// the server cache, so it does not wait for Proton's server list when one is on disk.
func Open(ctx context.Context, pc vpnprovider.Provider, n int, logf func(string, ...any)) (*Pool, error) {
	return OpenWith(ctx, pc, n, logf, Options{})
}

// OpenWith is Open with options; see Options.
func OpenWith(ctx context.Context, pc vpnprovider.Provider, n int, logf func(string, ...any), o Options) (*Pool, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	path := o.CachePath
	switch path {
	case "":
		path = CachePathFor(pc.ID())
	case "off":
		path = ""
	}
	label := pc.Label()
	cache := LoadCache(path, pc.AccountID())

	// The certificate (cached in the session file for days) and the server list (cached
	// for minutes) are independent, so ask for whichever is missing at the same time.
	type keyRes struct {
		k   *proton.WGKey
		err error
	}
	kc := make(chan keyRes, 1)
	go func() {
		k, err := pc.IdentityAt(ctx, 0)
		kc <- keyRes{k, err}
	}()
	// Where the user is, so nearby servers are preferred. Best effort and quick: without
	// it, ranking falls back to Proton's score and our own measurements.
	// The last known one is used at once when there is one (and refreshed afterwards).
	hc := make(chan *proton.Location, 1)
	cachedHome := cache.Home()
	fetchHome := func(ctx context.Context) *proton.Location {
		if l, err := pc.Location(ctx); err == nil && (l.Lat != 0 || l.Long != 0) {
			cache.SetHome(l)
			return &l
		}
		return nil
	}
	if cachedHome != nil {
		hc <- cachedHome
	} else {
		go func() {
			lctx, cancel := context.WithTimeout(ctx, quickAPI)
			defer cancel()
			hc <- fetchHome(lctx)
		}()
	}
	now := time.Now()
	list, maxConnect := cache.Servers()
	age := cache.Age(now)
	refresh := false
	if len(list) > 0 && !Located(list) && age < MaxStale {
		// Saved by an older version, without server locations. Fetch a fresh list (once:
		// it's saved with locations) so this run already gets nearby servers; if Proton is
		// slow or down, use the saved one and refresh it in the background as usual.
		lctx, cancel := context.WithTimeout(ctx, migrateAPI)
		sl, err := pc.ServersInfo(lctx)
		cancel()
		if err == nil && len(sl.Servers) > 0 {
			list, maxConnect = sl.Servers, sl.MaxConnect
			cache.SetServers(list, maxConnect, time.Now())
			_ = cache.Save()
			age = 0
		} else {
			age = max(age, FreshTTL) // take the "stale but usable" path below
		}
	}
	switch {
	case len(list) > 0 && age < FreshTTL:
	case len(list) > 0 && age < MaxStale:
		refresh = true // use it now, update it behind the caller's back
	default:
		sl, err := pc.ServersInfo(ctx)
		switch {
		case err == nil:
			list, maxConnect = sl.Servers, sl.MaxConnect
			cache.SetServers(list, maxConnect, time.Now())
			_ = cache.Save()
		case len(list) > 0:
			logf("  (couldn't refresh %s's server list: %v; using the saved one)", label, err)
		default:
			<-kc
			return nil, err
		}
	}
	kr := <-kc
	home := <-hc
	if kr.err != nil {
		return nil, kr.err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no usable %s servers for this account", label)
	}
	// Every tunnel counts as a device on the account; opening more than the plan allows
	// just gets the extras refused.
	if maxConnect > 0 && n > maxConnect {
		logf("  your %s account allows %d devices at once; using %d tunnels", label, maxConnect, maxConnect)
		n = maxConnect
	}
	n = max(n, 1)
	countries := o.Countries
	if len(countries) > 0 && len(cache.Rank(list, RankOpts{Countries: countries, NoShuffle: true}, now)) == 0 {
		logf("  (no usable servers in %s; using any country)", strings.Join(countries, ", "))
		countries = nil
	}
	spares := o.Spares
	switch {
	case spares == 0:
		spares = 1
	case spares < 0:
		spares = 0
	}
	if maxConnect <= 0 { // limit unknown: don't risk kicking a working tunnel off the account
		spares = 0
	} else {
		spares = min(spares, maxConnect-n)
	}

	pctx, cancel := context.WithCancel(ctx)
	p := &Pool{
		keys: newKeyRing(keySlots(maxConnect, n, spares), pc.MaxSlots(), pc.IdentityAt), cache: cache, servers: list,
		tunnels: map[string]*Tunnel{}, connecting: map[string]bool{}, tried: map[string]bool{}, failedCC: map[string]int{},
		known: map[string]proton.Server{}, reasons: map[string]string{},
		maxConnect: maxConnect, spares: spares, countries: countries, home: home, onExit: o.OnExit, logf: logf,
		ctx: pctx, cancel: cancel,
		label: label,
		dial: func(ctx context.Context, k *Key, s Server) (*Tunnel, error) {
			cfg, err := pc.Tunnel(s, k)
			if err != nil {
				return nil, err
			}
			return OpenTunnel(ctx, cfg, s)
		},
	}
	if o.dial != nil {
		p.dial = o.dial
	}
	p.keys.seed(0, kr.k)
	if cachedHome != nil {
		p.spawn(func() { // the user may have moved since: update for later swaps and runs
			if h := fetchHome(pctx); h != nil {
				_ = cache.Save()
				p.mu.Lock()
				p.home = h
				p.mu.Unlock()
			}
		})
	} else if home != nil {
		_ = cache.Save()
	}
	if refresh {
		p.spawn(func() {
			sl, err := pc.ServersInfo(pctx)
			if err != nil || len(sl.Servers) == 0 {
				return
			}
			cache.SetServers(sl.Servers, sl.MaxConnect, time.Now())
			_ = cache.Save()
			p.mu.Lock()
			p.servers = sl.Servers
			p.mu.Unlock()
		})
	}
	return p.start(ctx, n, o.MinReady)
}

// start brings up n tunnels in parallel and returns per minReady.
func (p *Pool) start(ctx context.Context, n, minReady int) (*Pool, error) {
	if minReady <= 0 || minReady > n {
		minReady = n
	}
	var (
		ready     = make(chan struct{})
		readyOnce sync.Once
		done      = make(chan struct{})
		misses    int // shared across workers; guarded by p.mu
		maxMisses = 6 + n
		workers   sync.WaitGroup
	)
	// (Measured: Proton answers the first handshake only after WireGuard's fixed 5s
	// retransmission on most servers, so racing candidates doesn't help; one per worker.)
	const hedge = 1
	for range n {
		workers.Add(1)
		if !p.spawn(func() {
			defer workers.Done()
			for {
				p.mu.Lock()
				stop := misses >= maxMisses || p.closed
				p.mu.Unlock()
				if stop || p.ctx.Err() != nil {
					return
				}
				t, err := p.connectOne(p.ctx, hedge, "")
				if err != nil {
					if errors.Is(err, errNoSpare) {
						return
					}
					p.mu.Lock()
					misses++
					p.mu.Unlock()
					continue
				}
				if !p.install(t) {
					return
				}
				p.mu.Lock()
				if len(p.tunnels) >= minReady {
					readyOnce.Do(func() { close(ready) })
				}
				p.mu.Unlock()
				return
			}
		}) {
			workers.Done()
		}
	}
	p.spawn(func() {
		workers.Wait()
		close(done)
		p.refillWarm() // pre-warm standbys once the real exits are up
	})
	select {
	case <-ready:
	case <-done:
	case <-ctx.Done():
	}
	p.mu.Lock()
	p.returned = true
	count := len(p.tunnels)
	p.mu.Unlock()
	if count == 0 {
		p.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("couldn't reach any %s server: UDP to the WireGuard port may be blocked by your network, or your account's device limit is used up (remove a device in your %s account)", p.label, p.label)
	}
	return p, nil
}

// spawn runs f on a goroutine that Close waits for. It reports false (without running f)
// if the pool is already closed.
func (p *Pool) spawn(f func()) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		f()
	}()
	return true
}

// freeSlotsLocked is how many more tunnels the plan would let us open (large if unknown).
func (p *Pool) freeSlotsLocked() int {
	if p.maxConnect <= 0 {
		return 1 << 20
	}
	return p.maxConnect - len(p.tunnels) - len(p.warm) - len(p.connecting)
}

// claim picks up to width of the best servers not already in use and marks them as being
// dialled.
func (p *Pool) claim(width int, avoidCountry string) []proton.Server {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	avoid := map[string]bool{}
	used := map[string]int{}
	cities := map[string]int{}
	for name := range p.tried {
		avoid[name] = true
	}
	for cc, n := range p.failedCC { // countries where dials keep failing sink behind untried ones
		used[cc] += n
	}
	inUse := func(s proton.Server) {
		avoid[s.Name] = true
		used[s.Country]++
		cities[cityKey(s)]++
	}
	for name := range p.connecting {
		inUse(p.known[name])
	}
	for _, t := range p.tunnels {
		inUse(t.Server)
	}
	for _, t := range p.warm {
		inUse(t.Server)
	}
	ranked := p.cache.Rank(p.servers, RankOpts{Avoid: avoid, UsedCountries: used, UsedCities: cities,
		AvoidCountry: avoidCountry, Countries: p.countries, Home: p.home}, time.Now())
	width = max(1, min(width, p.freeSlotsLocked()))
	out := ranked[:min(width, len(ranked))]
	for _, s := range out {
		p.connecting[s.Name] = true
		p.known[s.Name] = s
	}
	return out
}

func (p *Pool) unclaim(s proton.Server) {
	p.mu.Lock()
	delete(p.connecting, s.Name)
	p.mu.Unlock()
}

// connectOne dials the best width candidates at once and returns the first tunnel that
// comes up (the others are cancelled and closed). It does not register the tunnel.
func (p *Pool) connectOne(ctx context.Context, width int, avoidCountry string) (*Tunnel, error) {
	cands := p.claim(width, avoidCountry)
	if len(cands) == 0 {
		return nil, errNoSpare
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		t   *Tunnel
		s   proton.Server
		err error
	}
	ch := make(chan res, len(cands))
	for _, s := range cands {
		go func() {
			// each candidate on its own key: the loser's handshake must not steal the
			// winner's routing at Proton
			key, release, err := p.keys.acquire(cctx)
			if err != nil {
				ch <- res{nil, s, err}
				return
			}
			t, err := p.dial(cctx, key, s)
			if err != nil {
				release()
			} else {
				t.onClose = release
			}
			ch <- res{t, s, err}
		}()
	}
	var winner *Tunnel
	var last error
	for range cands {
		r := <-ch
		if r.err != nil || winner != nil {
			p.unclaim(r.s) // the winner stays claimed until it is installed
		}
		switch {
		case r.err == nil && winner == nil:
			winner = r.t
			cancel() // the rest no longer matter
		case r.err == nil:
			r.t.Close()
		case errors.Is(r.err, errNoKeySlot):
			last = r.err // not the server's fault
		case winner == nil && ctx.Err() == nil:
			p.mu.Lock()
			p.tried[r.s.Name] = true
			p.failedCC[r.s.Country]++
			p.mu.Unlock()
			p.cache.RecordConnect(r.s, false, 0, time.Now())
			p.logf("  ✗ %s %s: %v", r.s.Name, r.s.Country, r.err)
			last = r.err
		default:
			last = r.err
		}
	}
	if winner == nil {
		if last == nil {
			last = errors.New("cancelled")
		}
		return nil, last
	}
	p.cache.RecordConnect(winner.Server, true, winner.Connect, time.Now())
	return winner, nil
}

// install registers t as an active exit. It reports false (and closes t) if the pool is
// closed.
func (p *Pool) install(t *Tunnel) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		t.Close()
		return false
	}
	p.addLocked(t)
	cb := p.onExit
	announce := p.returned && cb != nil
	p.mu.Unlock()
	p.logf("  ✓ %s (%s)", t.Server.Name, t.Server.City)
	if announce {
		cb(t.Exit)
	}
	return true
}

func (p *Pool) addLocked(t *Tunnel) {
	delete(p.connecting, t.Server.Name)
	p.seq++
	t.seq = p.seq
	p.tunnels[t.Exit.Name] = t
	p.known[t.Exit.Name] = t.Server
	delete(p.reasons, t.Exit.Name)
}

// Exits lists the live exits, oldest first. While background connects are running it
// grows.
func (p *Pool) Exits() []Exit {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts := make([]*Tunnel, 0, len(p.tunnels))
	for _, t := range p.tunnels {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].seq < ts[j].seq })
	out := make([]Exit, len(ts))
	for i, t := range ts {
		out[i] = t.Exit
	}
	return out
}

// server finds what we know about an exit, whether or not it is still active.
func (p *Pool) server(e Exit) (proton.Server, bool) {
	s, ok := p.known[e.Name]
	return s, ok && s.Name != ""
}

// MarkBlocked records that exit was blocked or throttled (reason is free text; use
// "unavailable" when the site refuses the address outright, which also makes the next
// pick move to a different country). The server is kept out of rotation with growing
// back-off, and the cache is saved straight away.
func (p *Pool) MarkBlocked(e Exit, reason string) {
	p.mu.Lock()
	s, ok := p.server(e)
	if ok {
		p.reasons[e.Name] = reason
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	p.cache.RecordBlock(s, reason, time.Now())
	_ = p.cache.Save()
}

// MarkGood records that exit carried traffic well, with its observed throughput in
// bytes per second (<= 0 if unknown). It clears back-off and favours the server and its
// country in later picks.
func (p *Pool) MarkGood(e Exit, bytesPerSec float64) {
	p.mu.Lock()
	s, ok := p.server(e)
	p.mu.Unlock()
	if !ok {
		return
	}
	p.cache.RecordGood(s, bytesPerSec, time.Now())
	p.cache.saveSoon()
}

// Snapshot lists every server we hold a record for or are connected to, best first,
// with load, score and our health data. Meant for a status view.
func (p *Pool) Snapshot() []ServerStat {
	now := time.Now()
	p.mu.Lock()
	servers := append([]proton.Server(nil), p.servers...)
	active := map[string]bool{}
	for _, t := range p.tunnels {
		active[t.Server.Name] = true
	}
	warm := map[string]bool{}
	for _, t := range p.warm {
		warm[t.Server.Name] = true
	}
	p.mu.Unlock()
	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()
	var out []ServerStat
	for _, s := range servers {
		if !active[s.Name] && !warm[s.Name] && p.cache.f.Health[s.Name] == nil {
			continue
		}
		st := p.cache.stat(s, now)
		st.Active, st.Warm = active[s.Name], warm[s.Name]
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.Blocked != b.Blocked {
			return !a.Blocked
		}
		return a.Score+float64(a.Load)/25 < b.Score+float64(b.Load)/25
	})
	return out
}

// Healthy reports whether the tunnel behind e is alive (true for unknown exits). A tunnel
// that has stopped handshaking is recorded as blocked so it is not picked again soon.
func (p *Pool) Healthy(e Exit) bool {
	p.mu.Lock()
	t := p.tunnels[e.Name]
	p.mu.Unlock()
	if t == nil || t.Alive() {
		return true
	}
	p.mu.Lock()
	first := p.reasons[e.Name] == ""
	p.mu.Unlock()
	if first {
		p.MarkBlocked(e, "dead")
	}
	return false
}

// takeWarm hands out a live standby, preferring one outside avoidCountry.
func (p *Pool) takeWarm(avoidCountry string) *Tunnel {
	p.mu.Lock()
	defer p.mu.Unlock()
	var dead []*Tunnel
	var pick *Tunnel
	alive := p.warm[:0:0]
	for _, t := range p.warm {
		if !t.Alive() {
			dead = append(dead, t)
			continue
		}
		alive = append(alive, t)
	}
	for _, t := range alive {
		if avoidCountry == "" || !strings.EqualFold(t.Server.Country, avoidCountry) {
			pick = t
			break
		}
	}
	if pick == nil && len(alive) > 0 {
		pick = alive[0]
	}
	p.warm = p.warm[:0]
	for _, t := range alive {
		if t != pick {
			p.warm = append(p.warm, t)
		}
	}
	for _, t := range dead {
		if !p.spawnUnlocked(t.Close) {
			go t.Close()
		}
	}
	if pick != nil {
		p.addLocked(pick)
	}
	return pick
}

// closeLater closes t without making the caller wait for it.
func (p *Pool) closeLater(t *Tunnel) {
	if !p.spawn(t.Close) {
		t.Close()
	}
}

// spawnUnlocked is spawn for callers that already hold p.mu.
func (p *Pool) spawnUnlocked(f func()) bool {
	if p.closed {
		return false
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		f()
	}()
	return true
}

// refillWarm tops the standby tunnels up in the background (one refill runs at a time).
func (p *Pool) refillWarm() {
	p.mu.Lock()
	if p.refilling || p.closed || p.spares <= 0 {
		p.mu.Unlock()
		return
	}
	p.refilling = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.refilling = false; p.mu.Unlock() }()
	for fails := 0; fails < 2 && p.ctx.Err() == nil; {
		p.mu.Lock()
		need := len(p.warm) < p.spares && p.freeSlotsLocked() >= 1
		p.mu.Unlock()
		if !need {
			return
		}
		t, err := p.connectOne(p.ctx, 1, "")
		if err != nil {
			if errors.Is(err, errNoSpare) {
				return
			}
			fails++
			continue
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			t.Close()
			return
		}
		delete(p.connecting, t.Server.Name)
		p.warm = append(p.warm, t)
		p.mu.Unlock()
	}
}

func (p *Pool) refillSoon() { p.spawn(p.refillWarm) }

// Replace swaps a dead, blocked or throttled exit for one on a different server. The old
// server is put in back-off (if MarkBlocked hasn't already), a standby tunnel is used when
// there is one, and the standbys are refilled in the background. When the reason given
// to MarkBlocked was "unavailable", the replacement is in another country.
func (p *Pool) Replace(ctx context.Context, old Exit) (Exit, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Exit{}, errors.New("closed")
	}
	t := p.tunnels[old.Name]
	delete(p.tunnels, old.Name)
	reason := p.reasons[old.Name]
	p.mu.Unlock()
	avoidCountry := ""
	if t != nil {
		if reason == "" {
			p.cache.RecordBlock(t.Server, "replaced", time.Now())
			p.mu.Lock()
			p.reasons[old.Name] = "replaced"
			p.mu.Unlock()
			_ = p.cache.Save()
		}
		if reason == "unavailable" {
			avoidCountry = t.Server.Country
		}
		p.closeLater(t)
	}
	if w := p.takeWarm(avoidCountry); w != nil {
		p.refillSoon()
		return w.Exit, nil
	}
	for i := 0; i < 6 && ctx.Err() == nil; i++ {
		nt, err := p.connectOne(ctx, 2, avoidCountry)
		if errors.Is(err, errNoSpare) {
			break
		}
		if err != nil {
			continue
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			nt.Close()
			return Exit{}, errors.New("closed")
		}
		p.addLocked(nt)
		p.mu.Unlock()
		p.logf("  ✓ %s (%s)", nt.Server.Name, nt.Server.City)
		p.refillSoon()
		return nt.Exit, nil
	}
	return Exit{}, errors.New("no replacement server")
}

// Close tears down every tunnel, waits for background work to stop and saves the cache.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	ts := p.tunnels
	ws := p.warm
	p.tunnels = map[string]*Tunnel{}
	p.warm = nil
	p.mu.Unlock()
	p.cancel()
	for _, t := range ts { // closing can block; don't hold the lock
		t.Close()
	}
	for _, t := range ws {
		t.Close()
	}
	p.wg.Wait()
	_ = p.cache.Save()
}
