package proton

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestX25519FromSeed(t *testing.T) {
	k := X25519FromSeed(make([]byte, 32))
	if k[0]&7 != 0 || k[31]&128 != 0 || k[31]&64 == 0 {
		t.Fatalf("not clamped: %s", hex.EncodeToString(k[:]))
	}
}

func TestFilterAndPick(t *testing.T) {
	mk := func(name, cc string, tier, feat int, score float64) logical {
		l := logical{Name: name, ExitCountry: cc, Tier: tier, Features: feat, Status: 1, Score: score}
		l.Servers = []struct {
			EntryIP         string
			Status          int
			X25519PublicKey string
		}{{"1.1.1.1", 1, "k"}}
		return l
	}
	all := filterServers([]logical{mk("A#1", "US", 2, 0, 1), mk("B#1", "NL", 2, 1, 1), mk("C#1", "DE", 3, 0, 1), mk("D#1", "CH", 2, 4, 2)}, 2)
	if len(all) != 2 { // secure-core and too-high tier dropped
		t.Fatalf("%+v", all)
	}
	got, err := Pick(all, 5, nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := Pick(all, 2, map[string]bool{"A#1": true}); len(got) != 1 || got[0].Name != "D#1" {
		t.Fatalf("avoid ignored: %+v", got)
	}
}

func TestAPIHeadersAndErrors(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		switch r.URL.Path {
		case "/boom":
			w.WriteHeader(400)
			w.Write([]byte(`{"Code":5003,"Error":"old"}`))
		case "/vpn/v2":
			w.Write([]byte(`{"Code":1000,"VPN":{"MaxTier":2,"MaxConnect":7}}`))
		default:
			w.Write([]byte(`{"Code":1000,"LogicalServers":[]}`))
		}
	}))
	defer srv.Close()
	c := New("")
	c.Base = srv.URL
	c.Session = &Session{UID: "u1", AccessToken: "tok"}
	if _, err := c.Servers(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := appIDs()[0]
	if hdr.Get("x-pm-appversion") != id.Version || hdr.Get("User-Agent") != id.UA ||
		hdr.Get("x-pm-uid") != "u1" || hdr.Get("Authorization") != "Bearer tok" {
		t.Fatalf("headers: %v", hdr)
	}
	if hdr.Get("x-pm-apiversion") != "" {
		t.Fatal("official clients don't send x-pm-apiversion")
	}
	if c.MaxConnect != 7 {
		t.Fatalf("plan device limit not read, got %d", c.MaxConnect)
	}
	// every known identity is rejected: the error must say what to do
	err := c.do(context.Background(), "GET", "/boom", true, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "needs an update") {
		t.Fatalf("got %v", err)
	}
}

func TestAppVersionFallback(t *testing.T) {
	ids := appIDs()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("x-pm-appversion")
		seen = append(seen, v)
		if v != ids[len(ids)-1].Version {
			w.WriteHeader(400)
			w.Write([]byte(`{"Code":5003,"Error":"app version too old"}`))
			return
		}
		w.Write([]byte(`{"Code":1000}`))
	}))
	defer srv.Close()
	c := New("")
	c.Base = srv.URL
	c.Session = &Session{UID: "u", AccessToken: "t"}
	if err := c.do(context.Background(), "GET", "/x", true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(ids) {
		t.Fatalf("expected to walk all %d identities, saw %v", len(ids), seen)
	}
	seen = nil
	if err := c.do(context.Background(), "GET", "/x", true, nil, nil); err != nil || len(seen) != 1 {
		t.Fatalf("the working identity should be remembered: err=%v seen=%v", err, seen)
	}
}

// Proton once answered an outdated app version with code 2028 and only the message saying
// so; that must walk to the next identity, not be reported as a sign-in block.
func TestUnsupportedVersionByMessage(t *testing.T) {
	ids := appIDs()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-pm-appversion") != ids[1].Version {
			n++
			w.WriteHeader(422)
			w.Write([]byte(`{"Code":2028,"Error":"This version of the app is no longer supported, please update to continue using the app"}`))
			return
		}
		w.Write([]byte(`{"Code":1000}`))
	}))
	defer srv.Close()
	c := New("")
	c.Base = srv.URL
	c.Session = &Session{UID: "u", AccessToken: "t"}
	if err := c.do(context.Background(), "GET", "/x", true, nil, nil); err != nil || n != 1 {
		t.Fatalf("err=%v rejections=%d", err, n)
	}
	e := &APIError{Status: 422, Code: 2028, Msg: "This version of the app is no longer supported"}
	if e.RateLimited() || !e.BadVersion() {
		t.Fatal("must not be classed as a rate limit")
	}
}

func TestHumanVerificationAndRateLimitErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hv":
			w.WriteHeader(422)
			w.Write([]byte(`{"Code":9001,"Error":"Human verification required","Details":{"HumanVerificationMethods":["captcha","email"],"HumanVerificationToken":"tok123"}}`))
		case "/rl":
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(429)
			w.Write([]byte(`{"Code":2028,"Error":"slow down"}`))
		}
	}))
	defer srv.Close()
	c := New("")
	c.Base = srv.URL
	var ae *APIError
	err := c.do(context.Background(), "POST", "/hv", false, map[string]string{}, nil)
	if !errors.As(err, &ae) || !ae.NeedsHuman() || ae.HVToken != "tok123" || len(ae.HVMethods) != 2 {
		t.Fatalf("human check not parsed: %v %+v", err, ae)
	}
	if !strings.Contains(err.Error(), "account.protonvpn.com") {
		t.Fatalf("no next step in: %v", err)
	}
	err = c.do(context.Background(), "POST", "/rl", false, map[string]string{}, nil)
	if !errors.As(err, &ae) || !ae.RateLimited() || ae.RetryAfter != 10*time.Minute {
		t.Fatalf("rate limit not parsed: %v %+v", err, ae)
	}
	if !strings.Contains(err.Error(), "slow down") || !strings.Contains(err.Error(), "11 minutes") {
		t.Fatalf("message should include Proton's text and wait time: %v", err)
	}
}

func TestRefreshHappensOnceUnderConcurrent401s(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/refresh":
			refreshes.Add(1)
			time.Sleep(50 * time.Millisecond)
			w.Write([]byte(`{"Code":1000,"AccessToken":"new","RefreshToken":"rt2"}`))
		case r.Header.Get("Authorization") == "Bearer new":
			w.Write([]byte(`{"Code":1000}`))
		default:
			w.WriteHeader(401)
			w.Write([]byte(`{"Code":401,"Error":"expired"}`))
		}
	}))
	defer srv.Close()
	c := New(filepath.Join(t.TempDir(), "p.json"))
	c.Base = srv.URL
	c.Session = &Session{UID: "u", AccessToken: "old", RefreshToken: "rt1"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.do(context.Background(), "GET", "/data", true, nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := refreshes.Load(); n != 1 {
		t.Fatalf("refresh token rotates, so it must be used once; used %d times", n)
	}
	if st, err := os.Stat(c.Path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("session file missing or not 0600: %v %v", st, err)
	}
}

func TestPinCheck(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if verifyPin([][]byte{der}, nil) == nil {
		t.Fatal("an unpinned certificate must be rejected")
	}
	cert, _ := x509.ParseCertificate(der)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(sum[:])
	pins[pin] = true
	defer delete(pins, pin)
	if err := verifyPin([][]byte{{1, 2, 3}, der}, nil); err != nil {
		t.Fatalf("a pinned certificate anywhere in the chain should pass: %v", err)
	}
}

func TestSessionFileIsPrivateAndAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "proton.json")
	c := New(p)
	c.Session = &Session{Username: "u", UID: "id", AccessToken: "a", RefreshToken: "r"}
	if err := c.saveErr(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600 file, got %v %v", st, err)
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
	again := New(p)
	if !again.SignedIn() || again.Session.RefreshToken != "r" {
		t.Fatal("session did not round-trip")
	}
	again.Logout()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("logout must delete the session file")
	}
}
