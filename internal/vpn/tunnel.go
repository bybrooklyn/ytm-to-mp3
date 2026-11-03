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
	"sync"
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
}

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
	err = dev.IpcSet(fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=25\n",
		hex.EncodeToString(key.Private[:]), hex.EncodeToString(pub), endpoint))
	if err == nil {
		err = dev.Up()
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

	// First packet triggers the handshake; retry briefly.
	var last error
	for try := 0; try < 3; try++ {
		pctx, pc := context.WithTimeout(ctx, 12*time.Second)
		c, err := tnet.DialContext(pctx, "tcp", probe)
		pc()
		if err == nil {
			c.Close()
			return t, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	t.Close()
	return nil, fmt.Errorf("%s unreachable: %v", s.Name, last)
}

// Alive reports whether the tunnel has completed a WireGuard handshake recently. With a
// 25s keepalive a healthy tunnel re-handshakes about every two minutes.
func (t *Tunnel) Alive() bool {
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
	return sec > 0 && time.Since(time.Unix(sec, 0)) < 4*time.Minute
}

// Healthy reports whether the tunnel behind e is alive (true for unknown exits).
func (p *Pool) Healthy(e Exit) bool {
	p.mu.Lock()
	t := p.tunnels[e.Name]
	p.mu.Unlock()
	return t == nil || t.Alive()
}

// Close shuts the tunnel down.
func (t *Tunnel) Close() {
	t.cancel()
	t.dev.Close()
}

// Pool is a set of tunnels built from one Proton account, with spare servers to swap in.
type Pool struct {
	mu      sync.Mutex
	key     *proton.WGKey
	spare   []proton.Server
	tunnels map[string]*Tunnel // by exit name
	logf    func(string, ...any)
}

// Open connects n tunnels to different countries. Servers that fail are replaced from
// the spare list; it errors only if none work.
func Open(ctx context.Context, pc *proton.Client, n int, logf func(string, ...any)) (*Pool, error) {
	key, err := pc.Identity(ctx)
	if err != nil {
		return nil, err
	}
	all, err := pc.Servers(ctx)
	if err != nil {
		return nil, err
	}
	picks, err := proton.Pick(all, n*3, nil)
	if err != nil {
		return nil, err
	}
	p := &Pool{key: key, tunnels: map[string]*Tunnel{}, logf: logf}
	used := map[string]bool{}
	for _, s := range picks {
		used[s.Name] = true
	}
	// everything else is a spare, best first
	rest, _ := proton.Pick(all, len(all), used)
	p.spare = append(picks[min(n, len(picks)):], rest...)
	cand := picks[:min(n, len(picks))]

	var wg sync.WaitGroup
	for _, s := range cand {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t, err := OpenTunnel(ctx, key, s); err != nil {
				logf("  ✗ %s %s: %v", s.Name, s.Country, err)
			} else {
				logf("  ✓ %s (%s)", s.Name, s.City)
				p.mu.Lock()
				p.tunnels[t.Exit.Name] = t
				p.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for len(p.tunnels) < n && len(p.spare) > 0 && ctx.Err() == nil {
		if _, err := p.next(ctx); err != nil {
			continue
		}
	}
	if len(p.tunnels) == 0 {
		return nil, errors.New("couldn't connect to any Proton server (is UDP blocked?)")
	}
	return p, nil
}

// next opens a tunnel to the next spare server.
func (p *Pool) next(ctx context.Context) (Exit, error) {
	p.mu.Lock()
	if len(p.spare) == 0 {
		p.mu.Unlock()
		return Exit{}, errors.New("no spare servers")
	}
	s := p.spare[0]
	p.spare = p.spare[1:]
	p.mu.Unlock()
	t, err := OpenTunnel(ctx, p.key, s)
	if err != nil {
		p.logf("  ✗ %s %s: %v", s.Name, s.Country, err)
		return Exit{}, err
	}
	p.logf("  ✓ %s (%s)", s.Name, s.City)
	p.mu.Lock()
	p.tunnels[t.Exit.Name] = t
	p.mu.Unlock()
	return t.Exit, nil
}

// Exits lists the live exits.
func (p *Pool) Exits() []Exit {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Exit
	for _, t := range p.tunnels {
		out = append(out, t.Exit)
	}
	return out
}

// Replace swaps a dead or throttled exit for one on a different server.
func (p *Pool) Replace(ctx context.Context, old Exit) (Exit, error) {
	p.mu.Lock()
	if t := p.tunnels[old.Name]; t != nil {
		delete(p.tunnels, old.Name)
		go t.Close()
	}
	p.mu.Unlock()
	for i := 0; i < 3; i++ {
		if ex, err := p.next(ctx); err == nil {
			return ex, nil
		}
	}
	return Exit{}, errors.New("no replacement server")
}

// Close tears down every tunnel.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.tunnels {
		t.Close()
	}
	p.tunnels = map[string]*Tunnel{}
}
