package wgconf

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider"
)

// Provider serves the .conf files in Dir, one server per [Peer].
type Provider struct {
	Dir string

	loaded  bool
	confs   map[string]*Config // by server name
	servers []vpnprovider.Server
	files   int
	err     error
}

var _ vpnprovider.Provider = (*Provider)(nil)

// New returns a provider reading the default directory (see config.WireGuardDir).
func New() *Provider { return &Provider{Dir: config.WireGuardDir()} }

func (p *Provider) ID() string    { return "wireguard" }
func (p *Provider) Label() string { return "WireGuard config" }

// load parses every config in the directory once. Files that fail to parse are skipped
// and reported through p.err only when nothing at all is usable.
func (p *Provider) load() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.confs = map[string]*Config{}
	ents, err := os.ReadDir(p.Dir)
	if err != nil {
		p.err = errors.New("no WireGuard config imported: add a .conf file in Settings")
		return
	}
	var firstBad error
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".conf") {
			continue
		}
		f, err := os.Open(filepath.Join(p.Dir, e.Name()))
		if err != nil {
			continue
		}
		cfg, err := Parse(f)
		f.Close()
		if err != nil {
			if firstBad == nil {
				firstBad = fmt.Errorf("%s: %w", e.Name(), err)
			}
			continue
		}
		p.files++
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		for i, peer := range cfg.Peers {
			name := base
			if i > 0 {
				name += "#" + strconv.Itoa(i+1)
			}
			p.confs[name] = &Config{PrivateKey: cfg.PrivateKey, Addresses: cfg.Addresses, DNS: cfg.DNS,
				MTU: cfg.MTU, Peers: []Peer{peer}}
			p.servers = append(p.servers, vpnprovider.Server{
				Name: name, IP: peer.Host, Port: peer.Port, Ref: e.Name(),
				PublicKey: base64.StdEncoding.EncodeToString(peer.PublicKey[:]), Status: 1,
			})
		}
	}
	if len(p.servers) == 0 {
		p.err = errors.New("no usable WireGuard config in " + p.Dir)
		if firstBad != nil {
			p.err = firstBad
		}
	}
}

// Reload forgets what was read, so the next call sees files added or removed since.
func (p *Provider) Reload() { *p = Provider{Dir: p.Dir} }

// SignedIn reports whether at least one usable config is imported.
func (p *Provider) SignedIn() bool { p.load(); return p.err == nil }

// Files is how many config files are imported.
func (p *Provider) Files() int { p.load(); return p.files }

// AccountID tags the set of imported servers (never their keys).
func (p *Provider) AccountID() string {
	p.load()
	if p.err != nil {
		return ""
	}
	names := make([]string, 0, len(p.servers))
	for _, s := range p.servers {
		names = append(names, s.Name+"|"+s.IP+"|"+s.PublicKey)
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return "wg-" + hex.EncodeToString(sum[:6])
}

// MaxSlots is one identity per imported config file.
func (p *Provider) MaxSlots() int { p.load(); return max(p.files, 1) }

// IdentityAt returns a placeholder: each config carries its own key, which Tunnel uses.
func (p *Provider) IdentityAt(context.Context, int) (*vpnprovider.Key, error) {
	p.load()
	if p.err != nil {
		return nil, p.err
	}
	return &vpnprovider.Key{}, nil
}

func (p *Provider) Location(context.Context) (vpnprovider.Location, error) {
	return vpnprovider.Location{}, vpnprovider.ErrNoLocation
}

// ServersInfo lists the configured peers. The limit on simultaneous tunnels is the number
// of config files: more than that would reuse one key from two places at once.
func (p *Provider) ServersInfo(context.Context) (vpnprovider.ServerList, error) {
	p.load()
	if p.err != nil {
		return vpnprovider.ServerList{}, p.err
	}
	return vpnprovider.ServerList{Servers: append([]vpnprovider.Server(nil), p.servers...), MaxConnect: p.files}, nil
}

// Tunnel builds the connection from the config the server came from.
func (p *Provider) Tunnel(s vpnprovider.Server, _ *vpnprovider.Key) (vpnprovider.TunnelConfig, error) {
	p.load()
	c := p.confs[s.Name]
	if c == nil {
		return vpnprovider.TunnelConfig{}, errors.New("that WireGuard config is no longer imported")
	}
	peer := c.Peers[0]
	return vpnprovider.TunnelConfig{
		Endpoint:   net.JoinHostPort(peer.Host, strconv.Itoa(peer.Port)),
		PeerKey:    peer.PublicKey,
		PrivateKey: c.PrivateKey,
		Preshared:  peer.Preshared,
		Addresses:  c.Addresses,
		DNS:        c.DNS,
		MTU:        c.MTU,
	}, nil
}

// Import validates src (one .conf file, or a folder of them) and copies the good ones
// into Dir with mode 0600. It returns how many files were imported.
func (p *Provider) Import(src string) (int, error) {
	st, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	var paths []string
	if st.IsDir() {
		ents, err := os.ReadDir(src)
		if err != nil {
			return 0, err
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".conf") {
				paths = append(paths, filepath.Join(src, e.Name()))
			}
		}
	} else {
		paths = []string{src}
	}
	if len(paths) == 0 {
		return 0, errors.New("no .conf files there")
	}
	n := 0
	var firstErr error
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err == nil {
			_, err = Parse(strings.NewReader(string(b)))
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			continue
		}
		if err := config.WriteSecret(filepath.Join(p.Dir, safeName(filepath.Base(path))), b); err != nil {
			return n, err
		}
		n++
	}
	p.Reload()
	if n == 0 {
		return 0, firstErr
	}
	return n, nil
}

// Remove deletes every imported config.
func (p *Provider) Remove() error {
	ents, err := os.ReadDir(p.Dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".conf") {
			if err := os.Remove(filepath.Join(p.Dir, e.Name())); err != nil {
				return err
			}
		}
	}
	p.Reload()
	return nil
}

// safeName keeps file names to plain characters, always ending in .conf.
func safeName(n string) string {
	n = strings.TrimSuffix(n, filepath.Ext(n))
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		b.WriteString("vpn")
	}
	return b.String() + ".conf"
}
