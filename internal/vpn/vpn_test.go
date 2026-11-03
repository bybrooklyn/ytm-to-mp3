package vpn

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sample = `[Interface]
# NAT-PMP = on
PrivateKey = AAAA
Address = 10.2.0.2/32, fd00::2/128
DNS = 10.2.0.1

[Peer]
PublicKey = BBBB
AllowedIPs = 0.0.0.0/0
Endpoint = 1.2.3.4:51820
`

func TestParseWG(t *testing.T) {
	w, err := ParseWG(sample)
	if err != nil {
		t.Fatal(err)
	}
	if w.PrivateKey != "AAAA" || w.Endpoint != "1.2.3.4:51820" || len(w.Addresses) != 2 || !w.HasV6() || w.DNS[0] != "10.2.0.1" {
		t.Fatalf("%+v", w)
	}
	if s := w.SetConf(); strings.Contains(s, "Address") || strings.Contains(s, "DNS") {
		t.Fatalf("setconf not stripped: %s", s)
	}
	if _, err := ParseWG("[Interface]\nPrivateKey=x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestProxies(t *testing.T) {
	ex, err := Proxies([]string{"127.0.0.1:1080, socks5://a:b@h:2", "http://p:3"})
	if err != nil || len(ex) != 3 || ex[0].Proxy != "socks5h://127.0.0.1:1080" {
		t.Fatalf("%v %+v", err, ex)
	}
	if _, err := Proxies([]string{"ftp://x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSOCKSRelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sock := t.TempDir() + "/s.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go ServeSOCKS5(ctx, l, "ytm", "secret")
	addr, err := Relay(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	get := func(proxy string) (string, error) {
		c, err := NewClient(proxy)
		if err != nil {
			return "", err
		}
		resp, err := c.Get(srv.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}
	if got, err := get("socks5h://ytm:secret@" + addr); err != nil || got != "hello" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := get("socks5h://ytm:wrong@" + addr); err == nil {
		t.Fatal("bad password accepted")
	}
}
