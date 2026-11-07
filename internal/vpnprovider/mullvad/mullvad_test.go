package mullvad

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAccount = "1234567812345678"

// relays is shaped like https://api.mullvad.net/public/relays/wireguard/v2 (checked
// 2026-11-06), trimmed.
const relays = `{
 "locations": {
  "se-sto": {"country": "Sweden", "city": "Stockholm", "latitude": 59.3, "longitude": 18.0},
  "us-nyc": {"country": "USA", "city": "New York, NY", "latitude": 40.7, "longitude": -74.0}
 },
 "wireguard": {
  "port_ranges": [[53,53],[4000,33433],[33565,51820]],
  "ipv4_gateway": "10.64.0.1",
  "relays": [
   {"hostname":"se-sto-wg-001","location":"se-sto","active":true,"owned":true,"provider":"x","ipv4_addr_in":"185.65.135.1","include_in_country":true,"weight":100,"public_key":"ofyfRvMPB0PPIGGItNL+5tNdvTKXuWye5CfjPgPNvQ8=","ipv6_addr_in":"2a03::1"},
   {"hostname":"se-sto-wg-002","location":"se-sto","active":false,"ipv4_addr_in":"185.65.135.2","public_key":"Ny/8wG/vmSH3/bbyIER+8w32oGQBRGIcTnXtg+O90Cc="},
   {"hostname":"us-nyc-wg-301","location":"us-nyc","active":true,"ipv4_addr_in":"198.54.135.1","public_key":"Ny/8wG/vmSH3/bbyIER+8w32oGQBRGIcTnXtg+O90Cc="},
   {"hostname":"broken","location":"us-nyc","active":true,"ipv4_addr_in":"","public_key":""}
  ]
 }
}`

// fakeMullvad serves the endpoints Provider uses and records what it was sent.
type fakeMullvad struct {
	mu      sync.Mutex
	devices map[string]deviceResp // by pubkey
	max     int
	nextID  int
	tokens  int
	bodies  []string // request bodies, to check nothing leaks into URLs
	urls    []string
}

func (f *fakeMullvad) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/public/relays/wireguard/v2", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, relays) })
	mux.HandleFunc("/auth/v1/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			AccountNumber string `json:"account_number"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.AccountNumber != testAccount {
			w.WriteHeader(400)
			io.WriteString(w, `{"code":"INVALID_ACCOUNT","error":"Invalid account"}`)
			return
		}
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expiry": time.Now().Add(time.Hour).Format(time.RFC3339)})
	})
	mux.HandleFunc("/accounts/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			io.WriteString(w, `{"code":"INVALID_ACCESS_TOKEN"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case "GET":
			list := []deviceResp{}
			for _, d := range f.devices {
				list = append(list, d)
			}
			json.NewEncoder(w).Encode(list)
		case "POST":
			var in struct {
				Pubkey    string `json:"pubkey"`
				HijackDNS bool   `json:"hijack_dns"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if len(f.devices) >= f.max {
				w.WriteHeader(400)
				io.WriteString(w, `{"code":"MAX_DEVICES_REACHED","detail":"too many"}`)
				return
			}
			if _, dup := f.devices[in.Pubkey]; dup {
				w.WriteHeader(400)
				io.WriteString(w, `{"code":"PUBKEY_IN_USE"}`)
				return
			}
			f.nextID++
			d := deviceResp{ID: "id" + string(rune('0'+f.nextID)), Pubkey: in.Pubkey, IPv4Address: "10.66.0." + string(rune('0'+f.nextID)) + "/32"}
			f.devices[in.Pubkey] = d
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": d.ID, "pubkey": d.Pubkey, "ipv4_address": d.IPv4Address, "ipv6_address": "fc00::1/128", "hijack_dns": false, "name": "Happy Seagull"})
		}
	})
	mux.HandleFunc("/accounts/v1/devices/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/accounts/v1/devices/")
		f.mu.Lock()
		defer f.mu.Unlock()
		for k, d := range f.devices {
			if d.ID == id {
				delete(f.devices, k)
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ip":"203.0.113.9","country":"United States","city":"Paterson","longitude":-74.16,"latitude":40.91,"mullvad_exit_ip":false}`)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.urls = append(f.urls, r.URL.String())
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
}

func newFixture(t *testing.T, max int) (*Provider, *fakeMullvad) {
	t.Helper()
	f := &fakeMullvad{devices: map[string]deviceResp{}, max: max}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	p := New(filepath.Join(t.TempDir(), "sub", "mullvad.json"))
	p.Base, p.Meta = srv.URL, srv.URL
	return p, f
}

func TestParseRelays(t *testing.T) {
	got, err := ParseRelays([]byte(relays))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 { // inactive and broken relays are dropped
		t.Fatalf("got %d servers: %+v", len(got), got)
	}
	s := got[0]
	if s.Name != "se-sto-wg-001" || s.Country != "SE" || s.City != "Stockholm" || s.IP != "185.65.135.1" || s.Port != 51820 || s.Lat == 0 {
		t.Fatalf("bad server %+v", s)
	}
	if _, err := ParseRelays([]byte(`{"wireguard":{"relays":[]}}`)); err == nil {
		t.Fatal("empty list should be an error")
	}
	if _, err := ParseRelays([]byte(`<html>`)); err == nil {
		t.Fatal("garbage should be an error")
	}
}

func TestNormalizeAccount(t *testing.T) {
	for in, ok := range map[string]bool{
		"1234567812345678": true, "1234 5678 1234 5678": true, " 1234-5678-1234-5678 ": true,
		"123": false, "": false, "abcd567812345678": false, "12345678123456789": false,
	} {
		_, err := NormalizeAccount(in)
		if (err == nil) != ok {
			t.Errorf("NormalizeAccount(%q) err=%v, want ok=%v", in, err, ok)
		}
	}
}

func TestLoginAndSecretFile(t *testing.T) {
	p, f := newFixture(t, 5)
	ctx := context.Background()
	err := p.Login(ctx, "1111 2222 3333 4444")
	var ae *APIError
	if err == nil || !errorsAs(err, &ae) || ae.Code != "INVALID_ACCOUNT" {
		t.Fatalf("wrong account: %v", err)
	}
	if strings.Contains(err.Error(), "1111") {
		t.Fatal("error repeats the account number")
	}
	if p.SignedIn() {
		t.Fatal("a failed login must not leave an account stored")
	}
	if err := p.Login(ctx, "1234 5678 1234 5678"); err != nil {
		t.Fatal(err)
	}
	if !p.SignedIn() || p.Who() != "account ending 5678" {
		t.Fatalf("who = %q", p.Who())
	}
	if strings.Contains(p.Who(), "1234") {
		t.Fatal("Who shows more than the last digits")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p.Path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("session file mode %v, want 0600", st.Mode().Perm())
		}
		dst, _ := os.Stat(filepath.Dir(p.Path))
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("session dir mode %v, want 0700", dst.Mode().Perm())
		}
	}
	// the account number travels only in the token request's body, never in a URL
	for _, u := range f.urls {
		if strings.Contains(u, testAccount) {
			t.Fatalf("account number in URL %s", u)
		}
	}
	// reloading from disk keeps the sign-in
	if q := New(p.Path); !q.SignedIn() || q.AccountID() == "" || q.AccountID() != p.AccountID() {
		t.Fatal("saved account not reloaded")
	}
}

func TestRegistersOneKeyPerSlot(t *testing.T) {
	p, f := newFixture(t, 5)
	ctx := context.Background()
	if err := p.Login(ctx, testAccount); err != nil {
		t.Fatal(err)
	}
	k0, err := p.IdentityAt(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	k1, err := p.IdentityAt(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if k0.Private == k1.Private || k0.Address[0] == k1.Address[0] {
		t.Fatal("slots must not share a key")
	}
	if len(f.devices) != 2 {
		t.Fatalf("%d devices registered, want 2", len(f.devices))
	}
	// asking again reuses the registered key, no new device
	again, err := p.IdentityAt(ctx, 0)
	if err != nil || again.Private != k0.Private || len(f.devices) != 2 {
		t.Fatalf("slot 0 not reused: %v, %d devices", err, len(f.devices))
	}
	// and so does a fresh process reading the saved file
	q := New(p.Path)
	q.Base, q.Meta = p.Base, p.Meta
	if k, err := q.IdentityAt(ctx, 1); err != nil || k.Private != k1.Private || len(f.devices) != 2 {
		t.Fatalf("saved key not reused: %v", err)
	}
	if _, err := p.IdentityAt(ctx, 5); err == nil {
		t.Fatal("slot beyond the device limit must be refused")
	}
}

func TestTunnelConfig(t *testing.T) {
	p, _ := newFixture(t, 5)
	ctx := context.Background()
	p.Login(ctx, testAccount)
	k, err := p.IdentityAt(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := ParseRelays([]byte(relays))
	cfg, err := p.Tunnel(servers[0], k)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "185.65.135.1:51820" || cfg.Addresses[0] != k.Address[0] || cfg.DNS[0].String() != "10.64.0.1" || cfg.PrivateKey != k.Private || cfg.PeerKey == ([32]byte{}) {
		t.Fatalf("bad config %+v", cfg)
	}
}

func TestDeviceLimitCapsConnections(t *testing.T) {
	p, f := newFixture(t, 5)
	ctx := context.Background()
	p.Login(ctx, testAccount)
	// two devices belong to other apps
	f.devices["other1"] = deviceResp{ID: "o1", Pubkey: "other1"}
	f.devices["other2"] = deviceResp{ID: "o2", Pubkey: "other2"}
	sl, err := p.ServersInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sl.MaxConnect != 3 || len(sl.Servers) != 2 {
		t.Fatalf("MaxConnect=%d servers=%d, want 3 and 2", sl.MaxConnect, len(sl.Servers))
	}
	// one of ours registered: still 3 usable (ours count towards the cap, not against it)
	if _, err := p.IdentityAt(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if sl, _ = p.ServersInfo(ctx); sl.MaxConnect != 3 {
		t.Fatalf("MaxConnect=%d after registering, want 3", sl.MaxConnect)
	}
	// the account is full of other devices: a clear error, not a hang
	f.devices["other3"] = deviceResp{ID: "o3", Pubkey: "other3"}
	f.devices["other4"] = deviceResp{ID: "o4", Pubkey: "other4"}
	f.devices["other5"] = deviceResp{ID: "o5", Pubkey: "other5"}
	_, err = p.ServersInfo(ctx)
	if err == nil || !strings.Contains(err.Error(), "device slots") {
		t.Fatalf("want a device-limit message, got %v", err)
	}
}

func TestRegistrationRefusedAtLimit(t *testing.T) {
	p, _ := newFixture(t, 0)
	ctx := context.Background()
	p.Login(ctx, testAccount)
	_, err := p.IdentityAt(ctx, 0)
	var ae *APIError
	if !errorsAs(err, &ae) || ae.Code != "MAX_DEVICES_REACHED" || !strings.Contains(err.Error(), "remove one") {
		t.Fatalf("want MAX_DEVICES_REACHED with advice, got %v", err)
	}
}

func TestRemovedKeyIsRegisteredAgain(t *testing.T) {
	p, f := newFixture(t, 5)
	ctx := context.Background()
	p.Login(ctx, testAccount)
	k0, _ := p.IdentityAt(ctx, 0)
	// the user removes the device at mullvad.net
	for k := range f.devices {
		delete(f.devices, k)
	}
	q := New(p.Path)
	q.Base, q.Meta = p.Base, p.Meta
	k1, err := q.IdentityAt(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Private == k0.Private || len(f.devices) != 1 {
		t.Fatal("a key that vanished from the account must be replaced")
	}
}

func TestLogoutRemovesDevicesAndFile(t *testing.T) {
	p, f := newFixture(t, 5)
	ctx := context.Background()
	p.Login(ctx, testAccount)
	p.IdentityAt(ctx, 0)
	p.IdentityAt(ctx, 1)
	p.Logout(ctx)
	if len(f.devices) != 0 {
		t.Fatalf("%d devices left on the account", len(f.devices))
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("session file not removed")
	}
	if p.SignedIn() {
		t.Fatal("still signed in")
	}
}

func TestLocationAndServersErrors(t *testing.T) {
	p, _ := newFixture(t, 5)
	loc, err := p.Location(context.Background())
	if err != nil || loc.Lat == 0 {
		t.Fatalf("location %+v %v", loc, err)
	}
	// without an account the device list can't be read: say so rather than panic
	if _, err := p.ServersInfo(context.Background()); err == nil {
		t.Fatal("ServersInfo without a sign-in should fail")
	}
	// server down
	p.Base = "http://127.0.0.1:1"
	p.Login(context.Background(), testAccount)
	if err := p.Login(context.Background(), testAccount); err == nil || strings.Contains(err.Error(), testAccount) {
		t.Fatalf("unreachable API: %v", err)
	}
}

func errorsAs(err error, target **APIError) bool {
	for err != nil {
		if a, ok := err.(*APIError); ok {
			*target = a
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
