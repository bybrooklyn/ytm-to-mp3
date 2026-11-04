package engine

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

func TestEnvCarriesProxyNotArgv(t *testing.T) {
	y := ytdlp{proxy: "socks5h://ytm:secret@127.0.0.1:9"}
	for _, a := range y.baseArgs() {
		if strings.Contains(a, "secret") {
			t.Fatalf("proxy password leaked into argv: %q", a)
		}
	}
	var found int
	for _, kv := range y.env() {
		if strings.HasSuffix(kv, "=socks5h://ytm:secret@127.0.0.1:9") {
			found++
		}
	}
	if found != 6 {
		t.Fatalf("expected 6 proxy variables, got %d", found)
	}
	if (ytdlp{}).env() != nil {
		t.Fatal("no proxy should inherit the environment")
	}
}

// Proves yt-dlp really sends its traffic through the proxy from the environment: point it
// at a SOCKS server that records the destination and refuses the connection. Skipped
// without yt-dlp on PATH.
func TestYtDlpUsesEnvProxy(t *testing.T) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Skip("yt-dlp not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	var dest atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go vpn.ServeSOCKS5(ctx, l, "ytm", "secret", func(_ context.Context, _, addr string) (net.Conn, error) {
		hits.Add(1)
		dest.Store(addr)
		return nil, net.ErrClosed
	})
	y := ytdlp{tools: deps.Tools{YtDlp: bin}, proxy: "socks5h://ytm:secret@" + l.Addr().String()}
	rctx, rc := context.WithTimeout(ctx, 40*time.Second)
	defer rc()
	_, _ = y.Resolve(rctx, "dQw4w9WgXcQ")
	if hits.Load() == 0 {
		t.Fatal("yt-dlp did not use the proxy from the environment")
	}
	if d, _ := dest.Load().(string); !strings.Contains(d, "youtube") && !strings.Contains(d, "google") && !strings.Contains(d, ":443") {
		t.Logf("proxy saw %q", d)
	}
}
