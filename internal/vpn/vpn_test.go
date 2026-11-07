package vpn

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSOCKS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var d net.Dialer
	go ServeSOCKS5(ctx, l, "mfy", "secret", d.DialContext)
	get := func(pass string) (string, error) {
		c, err := NewClient("socks5h://mfy:" + pass + "@" + l.Addr().String())
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
	if got, err := get("secret"); err != nil || got != "hello" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := get("wrong"); err == nil {
		t.Fatal("bad password accepted")
	}
}
