package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
)

// Tunnel is one userspace WireGuard connection (no root, no system changes) with a
// loopback SOCKS5 front door that carries its Exit.
type Tunnel struct {
	Server proton.Server
	Exit   Exit
	dev    *device.Device
	cancel context.CancelFunc
	seq    int // order the pool installed it in
	// Connect is how long the first handshake plus a test connection took.
	Connect time.Duration
}

// handshakeTimeout bounds one attempt to bring a tunnel up. WireGuard re-sends its
// handshake every 5s, so this allows one retransmission; a server that hasn't answered by
// then is skipped in favour of the next candidate.
var handshakeTimeout = 8 * time.Second

// aliveWithin is how stale the last handshake may be. WireGuard refuses to use a session
// after 180s, and a healthy tunnel re-keys every 120s, so older means dead.
const aliveWithin = 200 * time.Second

// OpenTunnel connects to one Proton server and checks traffic really flows.
func OpenTunnel(ctx context.Context, key *proton.WGKey, s proton.Server) (*Tunnel, error) {
	return openTunnel(ctx, key, s, s.IP+":51820", "www.youtube.com:443")
}

func openTunnel(ctx context.Context, key *proton.WGKey, s proton.Server, endpoint, probe string) (*Tunnel, error) {
	pub, err := base64.StdEncoding.DecodeString(s.PublicKey)
	if err != nil || len(pub) != 32 {
		return nil, errors.New("bad server key")
	}
	// Proton gives every client the same tunnel address and resolver.
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.2.0.2")},
		[]netip.Addr{netip.MustParseAddr("10.2.0.1")}, 1380)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	err = dev.IpcSet(fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\n",
		hex.EncodeToString(key.Private[:]), hex.EncodeToString(pub), endpoint))
	if err == nil {
		err = dev.Up()
	}
	if err == nil {
		// Turning the keepalive on once the device is up makes WireGuard send a packet at
		// once, i.e. start the handshake now rather than when the first connection asks.
		err = dev.IpcSet(fmt.Sprintf("public_key=%s\nupdate_only=true\npersistent_keepalive_interval=25\n", hex.EncodeToString(pub)))
	}
	if err != nil {
		dev.Close()
		return nil, err
	}
	tctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{Server: s, dev: dev, cancel: cancel}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Close()
		return nil, err
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	token := hex.EncodeToString(tok)
	go ServeSOCKS5(tctx, l, "ytm", token, tnet.DialContext)
	t.Exit = Exit{Name: fmt.Sprintf("%s %s", s.Name, s.Country), Proxy: "socks5h://ytm:" + token + "@" + l.Addr().String()}

	// The first packet triggers the handshake. A timeout means the server is dead (it
	// already had a retransmission); another error (reset, DNS) deserves one more go.
	start := time.Now()
	var last error
	for try := 0; try < 2; try++ {
		pctx, pc := context.WithTimeout(ctx, handshakeTimeout)
		c, err := tnet.DialContext(pctx, "tcp", probe)
		timedOut := pctx.Err() != nil
		pc()
		if err == nil {
			c.Close()
			t.Connect = time.Since(start)
			return t, nil
		}
		last = err
		if ctx.Err() != nil || timedOut {
			break
		}
	}
	t.Close()
	return nil, fmt.Errorf("%s unreachable: %v", s.Name, last)
}

// Alive reports whether the tunnel has completed a WireGuard handshake recently. With a
// 25s keepalive a healthy tunnel re-handshakes about every two minutes; see aliveWithin.
func (t *Tunnel) Alive() bool {
	if t.dev == nil {
		return true
	}
	cfg, err := t.dev.IpcGet()
	if err != nil {
		return true
	}
	var sec int64
	for _, line := range strings.Split(cfg, "\n") {
		if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok {
			sec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return sec > 0 && time.Since(time.Unix(sec, 0)) < aliveWithin
}

// Close shuts the tunnel down. It is safe to call more than once.
func (t *Tunnel) Close() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.dev != nil {
		t.dev.Close()
	}
}
