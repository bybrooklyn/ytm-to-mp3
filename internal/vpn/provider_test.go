package vpn

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider"
)

// fakeProvider is a provider that knows nothing about Proton: the pool must work with
// only what the vpnprovider.Provider interface gives it.
type fakeProvider struct {
	mu        sync.Mutex
	servers   []vpnprovider.Server
	max       int
	listErr   error
	slots     map[int]bool // identities handed out
	tunnelFor []string
}

func newFakeProvider(max int, names ...string) *fakeProvider {
	f := &fakeProvider{max: max, slots: map[int]bool{}}
	for i, n := range names {
		f.servers = append(f.servers, vpnprovider.Server{Name: n, Country: []string{"SE", "NL", "DE", "US"}[i%4], City: n + "-city",
			IP: "192.0.2.1", PublicKey: "x", Status: 1})
	}
	return f
}

func (f *fakeProvider) ID() string        { return "fake" }
func (f *fakeProvider) Label() string     { return "FakeVPN" }
func (f *fakeProvider) AccountID() string { return "fake-acct" }
func (f *fakeProvider) MaxSlots() int     { return 4 }
func (f *fakeProvider) Location(context.Context) (vpnprovider.Location, error) {
	return vpnprovider.Location{}, vpnprovider.ErrNoLocation
}
func (f *fakeProvider) IdentityAt(_ context.Context, slot int) (*vpnprovider.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots[slot] = true
	k := &vpnprovider.Key{Address: []netip.Addr{netip.MustParseAddr("10.0.0.2")}}
	k.Private[0] = byte(slot + 1) // distinct per slot
	return k, nil
}
func (f *fakeProvider) ServersInfo(context.Context) (vpnprovider.ServerList, error) {
	if f.listErr != nil {
		return vpnprovider.ServerList{}, f.listErr
	}
	return vpnprovider.ServerList{Servers: f.servers, MaxConnect: f.max}, nil
}
func (f *fakeProvider) Tunnel(s vpnprovider.Server, k *vpnprovider.Key) (vpnprovider.TunnelConfig, error) {
	f.mu.Lock()
	f.tunnelFor = append(f.tunnelFor, s.Name)
	f.mu.Unlock()
	return vpnprovider.TunnelConfig{Endpoint: "192.0.2.1:51820"}, nil
}

func openProvider(t *testing.T, fp *fakeProvider, n int, logf func(string, ...any)) (*Pool, *fakeDial, error) {
	t.Helper()
	fd := &fakeDial{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if logf == nil {
		logf = t.Logf
	}
	p, err := OpenWith(ctx, fp, n, logf, Options{dial: fd.dial, CachePath: filepath.Join(t.TempDir(), "s.json"), Spares: -1})
	if p != nil {
		t.Cleanup(p.Close)
	}
	return p, fd, err
}

func TestPoolWithNonProtonProvider(t *testing.T) {
	fp := newFakeProvider(3, "a", "b", "c", "d", "e")
	p, fd, err := openProvider(t, fp, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.Exits()); got != 3 {
		t.Fatalf("%d exits, want 3", got)
	}
	// every tunnel was dialled on its own identity
	seen := map[[32]byte]bool{}
	for _, k := range fd.keys {
		seen[k] = true
	}
	if len(seen) != 3 {
		t.Fatalf("tunnels shared keys: %d distinct for 3 tunnels", len(seen))
	}
}

func TestPoolCapsConnectionsAtTheProvidersLimit(t *testing.T) {
	fp := newFakeProvider(2, "a", "b", "c", "d")
	var mu sync.Mutex
	var logs []string
	p, _, err := openProvider(t, fp, 6, func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.Exits()); got != 2 {
		t.Fatalf("%d exits, want the provider's limit of 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range logs {
		if strings.Contains(l, "FakeVPN") && strings.Contains(l, "allows 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no clear message about the limit in %q", logs)
	}
}

// If the VPN can't be set up the pool errors; it never hands out a direct exit.
func TestPoolStopsWhenProviderFails(t *testing.T) {
	fp := newFakeProvider(3, "a", "b")
	fp.listErr = errors.New("device limit reached")
	p, _, err := openProvider(t, fp, 2, nil)
	if err == nil || p != nil {
		t.Fatalf("want an error and no pool, got %v %v", p, err)
	}
	// servers that all fail to connect: error, not an empty (direct) pool
	fp2 := newFakeProvider(3, "a", "b")
	fd := &fakeDial{failed: map[string]bool{"a": true, "b": true}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p2, err := OpenWith(ctx, fp2, 2, t.Logf, Options{dial: fd.dial, CachePath: filepath.Join(t.TempDir(), "s.json")})
	if err == nil || p2 != nil {
		t.Fatalf("want an error when no server connects, got %v", err)
	}
	if !strings.Contains(err.Error(), "FakeVPN") {
		t.Fatalf("error should name the provider: %v", err)
	}
}

func TestCachePathPerProvider(t *testing.T) {
	proton := CachePathFor("proton")
	if proton != CachePathFor("") || filepath.Base(proton) != "servers.json" {
		t.Fatalf("proton keeps servers.json, got %q", proton)
	}
	if mv := CachePathFor("mullvad"); mv == proton || filepath.Base(mv) != "servers-mullvad.json" {
		t.Fatalf("mullvad path %q", mv)
	}
}

func TestEndpointNamesResolveOnlyWhenNeeded(t *testing.T) {
	got, err := resolveEndpoint(context.Background(), "203.0.113.4:51820")
	if err != nil || got != "203.0.113.4:51820" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := resolveEndpoint(context.Background(), "no-port"); err == nil {
		t.Fatal("endpoint without a port must be refused")
	}
	if _, err := resolveEndpoint(context.Background(), "does-not-exist.invalid:51820"); err == nil {
		t.Fatal("unresolvable name must fail")
	}
}
