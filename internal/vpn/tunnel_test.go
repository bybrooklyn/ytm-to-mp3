package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
)

// Two userspace WireGuard peers over localhost UDP: the client tunnel must carry an HTTP
// request to a server that exists only inside the "VPN".
func TestTunnelLoopback(t *testing.T) {
	var cpriv, spriv [32]byte
	rand.Read(cpriv[:])
	rand.Read(spriv[:])
	cpub, _ := curve25519.X25519(cpriv[:], curve25519.Basepoint)
	spub, _ := curve25519.X25519(spriv[:], curve25519.Basepoint)

	stun, snet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.2.0.1")}, nil, 1380)
	if err != nil {
		t.Fatal(err)
	}
	sdev := device.NewDevice(stun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer sdev.Close()
	if err := sdev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.2.0.2/32\n",
		hex.EncodeToString(spriv[:]), hex.EncodeToString(cpub))); err != nil {
		t.Fatal(err)
	}
	if err := sdev.Up(); err != nil {
		t.Fatal(err)
	}
	// find the server's UDP port
	cfg, _ := sdev.IpcGet()
	var port string
	fmt.Sscanf(cfg[indexOf(cfg, "listen_port=")+len("listen_port="):], "%s", &port)

	l, err := snet.ListenTCPAddrPort(netip.MustParseAddrPort("10.2.0.1:80"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "inside the vpn") }))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := &proton.WGKey{Private: cpriv}
	srv := proton.Server{Name: "test", PublicKey: base64.StdEncoding.EncodeToString(spub)}
	tun, err := openTunnel(ctx, key, srv, "127.0.0.1:"+port, "10.2.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	c, _ := NewClient(tun.Exit.Proxy)
	resp, err := c.Get("http://10.2.0.1/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "inside the vpn" {
		t.Fatalf("got %q", b)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return 0
}
