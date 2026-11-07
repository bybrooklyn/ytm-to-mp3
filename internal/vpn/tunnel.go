package vpn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider"
)

// Tunnel is one userspace WireGuard connection (no root, no system changes) with a
// loopback SOCKS5 front door that carries its Exit.
type Tunnel struct {
	Server Server
	Exit   Exit
	dev    *device.Device
	cancel context.CancelFunc
	seq    int // order the pool installed it in
	// Connect is how long the first handshake plus a test connection took.
	Connect time.Duration

	closeOnce sync.Once
	onClose   func() // returns the tunnel's key slot to the pool
}

// handshakeTimeout bounds one attempt to bring a tunnel up. WireGuard re-sends its
// handshake every 5s, so this allows one retransmission; a server that hasn't answered by
// then is skipped in favour of the next candidate.
var handshakeTimeout = 8 * time.Second

// aliveWithin is how stale the last handshake may be. WireGuard refuses to use a session
// after 180s, and a healthy tunnel re-keys every 120s, so older means dead.
const aliveWithin = 200 * time.Second

// resolveEndpoint turns a host:port endpoint into ip:port. A host name has to be looked up
// before the tunnel exists, so that one lookup uses the system resolver (see PRIVACY.md);
// providers that publish IP addresses (Proton, Mullvad) never need it.
func resolveEndpoint(ctx context.Context, endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("bad endpoint %q", endpoint)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return endpoint, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("can't resolve the VPN server %s", host)
	}
	return net.JoinHostPort(ips[0].Unmap().String(), port), nil
}

// OpenTunnel connects to s with the configuration cfg and checks traffic really flows.
func OpenTunnel(ctx context.Context, cfg vpnprovider.TunnelConfig, s Server) (*Tunnel, error) {
	return openTunnel(ctx, cfg, s, "www.youtube.com:443")
}

func openTunnel(ctx context.Context, cfg vpnprovider.TunnelConfig, s Server, probe string) (*Tunnel, error) {
	endpoint, err := resolveEndpoint(ctx, cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("the VPN config has no tunnel address")
	}
	dns := cfg.DNS
	if len(dns) == 0 {
		// Still reached through the tunnel, so the lookup never leaves it.
		dns = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1380
	}
	allowed := "allowed_ip=0.0.0.0/0\n"
	for _, a := range cfg.Addresses {
		if a.Is6() && !a.Is4In6() {
			allowed += "allowed_ip=::/0\n"
			break
		}
	}
	tun, tnet, err := netstack.CreateNetTUN(cfg.Addresses, dns, mtu)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	psk := ""
	if cfg.Preshared != nil {
		psk = "preshared_key=" + hex.EncodeToString(cfg.Preshared[:]) + "\n"
	}
	pubHex := hex.EncodeToString(cfg.PeerKey[:])
	err = dev.IpcSet(fmt.Sprintf("private_key=%s\npublic_key=%s\n%sendpoint=%s\n%s",
		hex.EncodeToString(cfg.PrivateKey[:]), pubHex, psk, endpoint, allowed))
	if err == nil {
		err = dev.Up()
	}
	if err == nil {
		// Turning the keepalive on once the device is up makes WireGuard send a packet at
		// once, i.e. start the handshake now rather than when the first connection asks.
		err = dev.IpcSet(fmt.Sprintf("public_key=%s\nupdate_only=true\npersistent_keepalive_interval=25\n", pubHex))
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
	t.closeOnce.Do(func() {
		if t.onClose != nil {
			t.onClose()
		}
	})
}
