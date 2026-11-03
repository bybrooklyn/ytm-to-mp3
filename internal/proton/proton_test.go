package proton

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestX25519FromSeed(t *testing.T) {
	// SHA-512 of 32 zero bytes, first half clamped
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
		case "/vpn":
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
	if hdr.Get("x-pm-appversion") != appVersion || hdr.Get("x-pm-uid") != "u1" || hdr.Get("Authorization") != "Bearer tok" {
		t.Fatalf("headers: %v", hdr)
	}
	if c.MaxConnect != 7 {
		t.Fatalf("plan device limit not read, got %d", c.MaxConnect)
	}
	err := c.do(context.Background(), "GET", "/boom", true, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "needs an update") {
		t.Fatalf("got %v", err)
	}
}
