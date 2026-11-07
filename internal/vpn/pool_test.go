package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/proton"
)

type fakeAPI struct {
	srv       *httptest.Server
	logicals  atomic.Int32
	v2        atomic.Int32
	cert      atomic.Int32
	fail      atomic.Bool
	maxDevice int
	list      []map[string]any
}

func newFakeAPI(t *testing.T, maxDevice int, countries ...string) *fakeAPI {
	f := &fakeAPI{maxDevice: maxDevice}
	for _, cc := range countries {
		for i := 1; i <= 3; i++ {
			f.list = append(f.list, map[string]any{
				"Name": fmt.Sprintf("%s#%d", cc, i), "ExitCountry": cc, "City": cc + "-city", "Tier": 0, "Features": 0,
				"Status": 1, "Score": float64(i), "Load": 10 * i,
				"Location": map[string]any{"Lat": 50.0 + float64(len(f.list)), "Long": 10.0},
				"Servers":  []map[string]any{{"EntryIP": "192.0.2.1", "Status": 1, "X25519PublicKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}},
			})
		}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/vpn/v2":
			f.v2.Add(1)
			fmt.Fprintf(w, `{"Code":1000,"VPN":{"MaxTier":0,"MaxConnect":%d}}`, f.maxDevice)
		case r.URL.Path == "/vpn/v1/logicals":
			f.logicals.Add(1)
			if f.fail.Load() {
				http.Error(w, "boom", 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "LogicalServers": f.list})
		case r.URL.Path == "/vpn/v1/certificate":
			f.cert.Add(1)
			fmt.Fprint(w, `{"Code":1000,"RefreshTime":0,"ExpirationTime":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client(certFresh bool) *proton.Client {
	c := proton.New("")
	c.Base = f.srv.URL
	seed := make([]byte, 32)
	rand.Read(seed)
	c.Session = &proton.Session{Username: "me", UID: "u", AccessToken: "a", RefreshToken: "r", Key: base64.StdEncoding.EncodeToString(seed)}
	if certFresh {
		c.Session.CertUntil = time.Now().Add(48 * time.Hour)
	}
	return c
}

// fakeDial stands in for WireGuard: it records what was dialled and can fail or stall.
type fakeDial struct {
	mu     sync.Mutex
	dialed []string
	closed atomic.Int32
	failed map[string]bool
	delay  map[string]time.Duration
	live   atomic.Int32
	keys   map[string][32]byte // server -> WireGuard private key it was dialled with
}

func (d *fakeDial) dial(ctx context.Context, key *proton.WGKey, s proton.Server) (*Tunnel, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, s.Name)
	if d.keys == nil {
		d.keys = map[string][32]byte{}
	}
	d.keys[s.Name] = key.Private
	fail, delay := d.failed[s.Name] || d.failed[s.Country], d.delay[s.Name]
	d.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("no handshake")
	}
	d.live.Add(1)
	cctx, cancel := context.WithCancel(context.Background())
	_ = cctx
	return &Tunnel{Server: s, Exit: Exit{Name: s.Name + " " + s.Country, Proxy: "socks5h://x@" + s.Name},
		Connect: 50 * time.Millisecond, cancel: func() { cancel(); d.closed.Add(1); d.live.Add(-1) }}, nil
}

func (d *fakeDial) count() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.dialed) }

func openFake(t *testing.T, api *fakeAPI, fd *fakeDial, n int, o Options) *Pool {
	t.Helper()
	if o.CachePath == "" {
		o.CachePath = filepath.Join(t.TempDir(), "servers.json")
	}
	return openFakeClient(t, api.client(true), fd, n, o)
}

func openFakeClient(t *testing.T, pc *proton.Client, fd *fakeDial, n int, o Options) *Pool {
	t.Helper()
	if o.dial == nil {
		o.dial = fd.dial
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	p, err := OpenWith(ctx, pc, n, t.Logf, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func countries(p *Pool) map[string]int {
	m := map[string]int{}
	for _, e := range p.Exits() {
		m[e.Name[len(e.Name)-2:]]++
	}
	return m
}

func TestOpenColdThenCachedMakesNoAPICalls(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL", "DE", "SE")
	path := filepath.Join(t.TempDir(), "servers.json")
	pc := api.client(true) // slot 0's certificate is fresh; slots 1 and 2 are new
	fd := &fakeDial{}
	p := openFakeClient(t, pc, fd, 3, Options{CachePath: path, Spares: -1})
	if api.logicals.Load() != 1 || api.v2.Load() != 1 {
		t.Fatalf("cold run: v2=%d logicals=%d", api.v2.Load(), api.logicals.Load())
	}
	if api.cert.Load() != 2 {
		t.Fatalf("only the two new keys should be registered, got %d", api.cert.Load())
	}
	if len(p.Exits()) != 3 || len(countries(p)) != 3 {
		t.Fatalf("want 3 exits in 3 countries, got %v", countries(p))
	}
	p.Close()

	// second run: list and every certificate are fresh, no API calls at all
	fd2 := &fakeDial{}
	p2 := openFakeClient(t, pc, fd2, 3, Options{CachePath: path, Spares: -1})
	if api.logicals.Load() != 1 || api.v2.Load() != 1 || api.cert.Load() != 2 {
		t.Fatalf("cached run hit the API: v2=%d logicals=%d cert=%d", api.v2.Load(), api.logicals.Load(), api.cert.Load())
	}
	if len(p2.Exits()) != 3 {
		t.Fatal("cached run connected nothing")
	}
}

// Each tunnel has its own key: Proton routes a key only through its latest connection, so
// a shared key makes tunnels knock each other out.
func TestEveryTunnelHasItsOwnKey(t *testing.T) {
	api := newFakeAPI(t, 8, "US", "NL", "DE", "SE")
	fd := &fakeDial{}
	p := openFake(t, api, fd, 4, Options{CachePath: filepath.Join(t.TempDir(), "s.json"), Spares: -1})
	seen := map[[32]byte]string{}
	for _, e := range p.Exits() {
		name := e.Name[:strings.IndexByte(e.Name, ' ')]
		fd.mu.Lock()
		k := fd.keys[name]
		fd.mu.Unlock()
		if other, dup := seen[k]; dup {
			t.Fatalf("%s and %s share a WireGuard key", other, name)
		}
		seen[k] = name
	}
	if len(seen) != 4 {
		t.Fatalf("want 4 distinct keys, got %d", len(seen))
	}
}

func TestExpiredCertificateIsRegisteredOncePerKey(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL")
	pc := api.client(false)
	path := filepath.Join(t.TempDir(), "s.json")
	p := openFakeClient(t, pc, &fakeDial{}, 2, Options{CachePath: path, Spares: -1})
	if api.cert.Load() != 2 {
		t.Fatalf("cert registrations: %d (want one per tunnel key)", api.cert.Load())
	}
	p.Close()
	openFakeClient(t, pc, &fakeDial{}, 2, Options{CachePath: path, Spares: -1})
	if api.cert.Load() != 2 {
		t.Fatalf("fresh certificates were registered again: %d", api.cert.Load())
	}
}

func TestStaleCacheStartsImmediatelyAndRefreshesInBackground(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL", "DE")
	path := filepath.Join(t.TempDir(), "servers.json")
	c := LoadCache(path, AccountID("me"))
	old := []proton.Server{at(srv("OLD#1", "US", 1, 1), 40, -74), at(srv("OLD#2", "NL", 1, 1), 52, 5), at(srv("OLD#3", "DE", 1, 1), 50, 8)}
	c.SetServers(old, 6, time.Now().Add(-2*time.Hour)) // stale, but usable
	c.Save()

	// make the API slow: startup must not wait for it
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		http.Redirect(w, r, api.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer slow.Close()
	pc := api.client(true)
	pc.Base = slow.URL
	// keys registered by earlier runs, still fresh: only a brand-new key needs the API
	for i := 0; i < 4; i++ {
		seed := make([]byte, 32)
		rand.Read(seed)
		pc.Session.ExtraKeys = append(pc.Session.ExtraKeys, proton.SlotKey{
			Key: base64.StdEncoding.EncodeToString(seed), CertUntil: time.Now().Add(48 * time.Hour)})
	}
	start := time.Now()
	p := openFakeClient(t, pc, &fakeDial{}, 2, Options{CachePath: path, Spares: -1})
	if time.Since(start) > 3*time.Second {
		t.Fatal("startup waited for the API")
	}
	for _, e := range p.Exits() {
		if e.Name[:3] != "OLD" {
			t.Fatalf("expected servers from the cache, got %v", e)
		}
	}
	close(release)
	p.Close() // waits for the background refresh to end (it follows no redirects, so it just fails)
	if l, _ := LoadCache(path, AccountID("me")).Servers(); len(l) != 3 || l[0].Name != "OLD#1" {
		t.Fatalf("a failed refresh must leave the old list in place: %v", l)
	}
}

func TestStaleCacheRefreshUpdatesFile(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL", "DE")
	path := filepath.Join(t.TempDir(), "servers.json")
	c := LoadCache(path, AccountID("me"))
	c.SetServers([]proton.Server{srv("OLD#1", "US", 1, 1), srv("OLD#2", "NL", 1, 1)}, 6, time.Now().Add(-time.Hour))
	c.Save()
	p := openFake(t, api, &fakeDial{}, 2, Options{CachePath: path, Spares: -1})
	deadline := time.Now().Add(5 * time.Second)
	for api.logicals.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p.Close()
	l, _ := LoadCache(path, AccountID("me")).Servers()
	if len(l) != 9 {
		t.Fatalf("background refresh not saved: %d servers", len(l))
	}
}

func TestVeryStaleCacheFallsBackWhenNetworkFails(t *testing.T) {
	api := newFakeAPI(t, 6, "US")
	api.fail.Store(true)
	path := filepath.Join(t.TempDir(), "servers.json")
	c := LoadCache(path, AccountID("me"))
	c.SetServers([]proton.Server{srv("OLD#1", "US", 1, 1), srv("OLD#2", "NL", 1, 1)}, 6, time.Now().Add(-3*24*time.Hour))
	c.Save()
	p := openFake(t, api, &fakeDial{}, 2, Options{CachePath: path, Spares: -1})
	if len(p.Exits()) != 2 || api.logicals.Load() != 1 {
		t.Fatalf("exits=%d logicals=%d", len(p.Exits()), api.logicals.Load())
	}
	// and with no cache at all the error surfaces
	_, err := OpenWith(context.Background(), api.client(true), 1, t.Logf, Options{dial: (&fakeDial{}).dial, CachePath: filepath.Join(t.TempDir(), "x.json")})
	if err == nil {
		t.Fatal("expected an error with no cache and no network")
	}
}

func TestMinReadyReturnsEarlyAndAnnouncesLateExits(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL", "DE", "SE")
	fd := &fakeDial{}
	var inflight atomic.Int32
	slowThird := func(ctx context.Context, k *proton.WGKey, s proton.Server) (*Tunnel, error) {
		if inflight.Add(1) == 3 { // the third dial is slow
			select {
			case <-time.After(800 * time.Millisecond):
			case <-ctx.Done():
			}
		}
		return fd.dial(ctx, k, s)
	}
	gotLate := make(chan string, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	p, err := OpenWith(ctx, api.client(true), 3, t.Logf, Options{
		CachePath: filepath.Join(t.TempDir(), "s.json"), MinReady: 2, Spares: -1,
		OnExit: func(e Exit) { gotLate <- e.Name }, dial: slowThird,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("did not return early: %v", d)
	}
	if n := len(p.Exits()); n != 2 {
		t.Fatalf("want 2 ready at return, got %d", n)
	}
	select {
	case <-gotLate:
	case <-time.After(5 * time.Second):
		t.Fatal("late exit never announced")
	}
	if len(p.Exits()) != 3 {
		t.Fatalf("exits did not grow to 3: %v", p.Exits())
	}
}

func TestSpareMakesReplaceInstantAndRefills(t *testing.T) {
	api := newFakeAPI(t, 4, "US", "NL", "DE", "SE", "FR")
	fd := &fakeDial{}
	path := filepath.Join(t.TempDir(), "s.json")
	p := openFake(t, api, fd, 2, Options{CachePath: path, Spares: 2})
	waitFor(t, "two warm spares", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.warm) == 2 })
	if fd.live.Load() != 4 {
		t.Fatalf("live tunnels %d, want 2 active + 2 spare (= plan limit)", fd.live.Load())
	}
	// never more than the plan allows
	if fd.count() > 4 {
		t.Fatalf("dialled %d with a limit of 4", fd.count())
	}
	old := p.Exits()[0]
	before := fd.count()
	start := time.Now()
	ex, err := p.Replace(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("replace with a warm spare took %v", d)
	}
	if fd.count() != before {
		t.Fatal("replace dialled instead of using the spare")
	}
	if ex.Name == old.Name || len(p.Exits()) != 2 {
		t.Fatalf("bad replacement %v, exits %v", ex, p.Exits())
	}
	for _, e := range p.Exits() {
		if e.Name == old.Name {
			t.Fatal("old exit still active")
		}
	}
	// the old server is in back-off, and its tunnel closed
	st := p.Snapshot()
	var found bool
	for _, s := range st {
		if s.Name+" "+s.Country == old.Name {
			found = true
			if !s.Blocked || s.LastReason != "replaced" {
				t.Fatalf("old not marked: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("old missing from snapshot")
	}
	waitFor(t, "old tunnel closed and spare refilled", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return fd.closed.Load() >= 1 && len(p.warm) == 2
	})
	if fd.live.Load() != 4 {
		t.Fatalf("live %d after refill", fd.live.Load())
	}
}

func TestSparesNeverExceedPlanOrRunWhenLimitUnknown(t *testing.T) {
	api := newFakeAPI(t, 2, "US", "NL", "DE")
	fd := &fakeDial{}
	p := openFake(t, api, fd, 2, Options{Spares: 3})
	time.Sleep(100 * time.Millisecond)
	p.Close()
	if fd.count() != 2 {
		t.Fatalf("plan allows 2 devices, dialled %d", fd.count())
	}
	api0 := newFakeAPI(t, 0, "US", "NL", "DE")
	fd0 := &fakeDial{}
	p0 := openFake(t, api0, fd0, 2, Options{Spares: 2})
	time.Sleep(100 * time.Millisecond)
	p0.Close()
	if fd0.count() != 2 {
		t.Fatalf("unknown limit: dialled %d spares", fd0.count()-2)
	}
}

func TestReplaceUnavailableMovesCountryAndNeverPicksBlocked(t *testing.T) {
	api := newFakeAPI(t, 3, "US", "NL", "DE")
	fd := &fakeDial{}
	p := openFake(t, api, fd, 1, Options{Spares: -1})
	old := p.Exits()[0]
	cc := old.Name[len(old.Name)-2:]
	p.MarkBlocked(old, "unavailable")
	var seen []string
	for i := 0; i < 3; i++ {
		ex, err := p.Replace(context.Background(), old)
		if err != nil {
			t.Fatal(err)
		}
		if got := ex.Name[len(ex.Name)-2:]; got == cc {
			t.Fatalf("unavailable block should move country, stayed in %s", cc)
		}
		seen = append(seen, ex.Name)
		old = ex
		cc = ex.Name[len(ex.Name)-2:]
		p.MarkBlocked(old, "unavailable")
	}
	for _, s := range seen[:len(seen)-1] {
		if s == seen[len(seen)-1] {
			t.Fatalf("a blocked server came back: %v", seen)
		}
	}
	// the cache file records the blocks
	l := LoadCache(p.cache.path, AccountID("me"))
	if n := len(l.f.Health); n < 3 {
		t.Fatalf("health not persisted: %d entries", n)
	}
}

func TestReplaceRetriesPastDeadServers(t *testing.T) {
	api := newFakeAPI(t, 4, "US", "NL", "DE", "SE")
	fd := &fakeDial{failed: map[string]bool{}}
	p := openFake(t, api, fd, 1, Options{Spares: -1})
	old := p.Exits()[0]
	// kill two of the countries: replace must skip them and still succeed
	n := 0
	for _, cc := range []string{"US", "NL", "DE", "SE"} {
		if old.Name[len(old.Name)-2:] != cc && n < 2 {
			fd.mu.Lock()
			fd.failed[cc] = true
			fd.mu.Unlock()
			n++
		}
	}
	ex, err := p.Replace(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	bad := fd.failed[ex.Name[len(ex.Name)-2:]]
	fd.mu.Unlock()
	if bad {
		t.Fatalf("replacement is a dead server: %v", ex)
	}
}

func TestMarkGoodFeedsSnapshot(t *testing.T) {
	api := newFakeAPI(t, 3, "US", "NL")
	p := openFake(t, api, &fakeDial{}, 2, Options{Spares: -1})
	e := p.Exits()[0]
	p.MarkGood(e, 3<<20)
	p.MarkGood(Exit{Name: "unknown"}, 1) // must not panic
	p.MarkBlocked(Exit{Name: "unknown"}, "x")
	st := p.Snapshot()
	if len(st) < 2 || !st[0].Active {
		t.Fatalf("snapshot: %+v", st)
	}
	var ok bool
	for _, s := range st {
		if s.Name+" "+s.Country == e.Name && s.Successes == 1 && s.BytesPerSec == 3<<20 && !s.Blocked {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("good not recorded: %+v", st)
	}
}

func TestCountriesOption(t *testing.T) {
	api := newFakeAPI(t, 6, "US", "NL", "DE", "SE")
	p := openFake(t, api, &fakeDial{}, 3, Options{Countries: []string{"nl", "de"}, Spares: -1})
	for cc := range countries(p) {
		if cc != "NL" && cc != "DE" {
			t.Fatalf("exit in %s", cc)
		}
	}
	// a filter matching nothing falls back to automatic
	p2 := openFake(t, api, &fakeDial{}, 2, Options{Countries: []string{"ZZ"}, Spares: -1})
	if len(p2.Exits()) != 2 {
		t.Fatal("fallback to all countries failed")
	}
}

func TestCloseLeavesNothingRunning(t *testing.T) {
	api := newFakeAPI(t, 5, "US", "NL", "DE", "SE")
	fd := &fakeDial{}
	stalled := make(chan struct{}, 4)
	dial := func(ctx context.Context, k *proton.WGKey, s proton.Server) (*Tunnel, error) {
		if fd.count() >= 2 { // the spares hang until cancelled
			fd.mu.Lock()
			fd.dialed = append(fd.dialed, s.Name)
			fd.mu.Unlock()
			stalled <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fd.dial(ctx, k, s)
	}
	p := openFake(t, api, fd, 2, Options{Spares: 2, dial: dial})
	select {
	case <-stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("spare dial never started")
	}
	p.Close() // must cancel the hanging dial and wait for it
	if fd.live.Load() != 0 {
		t.Fatalf("%d tunnels still open after Close", fd.live.Load())
	}
	if _, err := p.Replace(context.Background(), Exit{Name: "x"}); err == nil {
		t.Fatal("Replace on a closed pool should fail")
	}
	p.Close() // twice is fine
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
