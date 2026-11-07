// Package wgconf is the "bring your own WireGuard config" provider: it reads standard
// wg-quick .conf files, so any provider that hands out one (IVPN, Windscribe, AirVPN, a
// self-hosted server, ...) works without mfetchy knowing anything about its API.
//
// Only the parts of the format that describe the tunnel are used. Script hooks (PostUp,
// PreDown, ...), Table, FwMark and SaveConfig are ignored: mfetchy never runs commands from a
// config file and never touches the system's network setup.
package wgconf

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Peer is one [Peer] section.
type Peer struct {
	PublicKey  [32]byte
	Preshared  *[32]byte
	Host       string // endpoint host (IP or name)
	Port       int
	AllowedIPs []netip.Prefix
}

// Config is one parsed wg-quick file.
type Config struct {
	PrivateKey [32]byte
	Addresses  []netip.Addr
	DNS        []netip.Addr
	MTU        int
	Peers      []Peer
}

// Parse reads a wg-quick config. Errors name the line and never echo key material.
func Parse(r io.Reader) (*Config, error) {
	var (
		cfg      Config
		section  string
		haveKey  bool
		peer     *Peer
		sawIface bool
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	flush := func() error {
		if peer == nil {
			return nil
		}
		if peer.Host == "" {
			return fmt.Errorf("a [Peer] has no Endpoint")
		}
		if peer.PublicKey == ([32]byte{}) {
			return fmt.Errorf("a [Peer] has no PublicKey")
		}
		if !fullTunnel(peer.AllowedIPs) {
			return fmt.Errorf("peer %s:%d: AllowedIPs must include 0.0.0.0/0 (mfetchy sends everything through the VPN)", peer.Host, peer.Port)
		}
		cfg.Peers = append(cfg.Peers, *peer)
		peer = nil
		return nil
	}
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if err := flush(); err != nil {
				return nil, err
			}
			switch strings.ToLower(strings.Trim(line, "[] \t")) {
			case "interface":
				section = "interface"
				sawIface = true
			case "peer":
				section = "peer"
				peer = &Peer{}
			default:
				return nil, fmt.Errorf("line %d: unknown section %s", n, line)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected \"Key = value\"", n)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch section {
		case "interface":
			switch k {
			case "privatekey":
				key, err := decodeKey(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: PrivateKey: %v", n, err)
				}
				cfg.PrivateKey, haveKey = key, true
			case "address":
				for _, f := range splitList(v) {
					a, err := parseAddr(f)
					if err != nil {
						return nil, fmt.Errorf("line %d: Address: %v", n, err)
					}
					cfg.Addresses = append(cfg.Addresses, a)
				}
			case "dns":
				for _, f := range splitList(v) {
					// search domains are allowed in wg-quick; only addresses matter here
					if a, err := netip.ParseAddr(f); err == nil {
						cfg.DNS = append(cfg.DNS, a)
					}
				}
			case "mtu":
				m, err := strconv.Atoi(v)
				if err != nil || m < 576 || m > 9000 {
					return nil, fmt.Errorf("line %d: MTU: bad value", n)
				}
				cfg.MTU = m
			}
		case "peer":
			switch k {
			case "publickey":
				key, err := decodeKey(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: PublicKey: %v", n, err)
				}
				peer.PublicKey = key
			case "presharedkey":
				key, err := decodeKey(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: PresharedKey: %v", n, err)
				}
				peer.Preshared = &key
			case "endpoint":
				h, p, err := net.SplitHostPort(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: Endpoint must be host:port", n)
				}
				port, err := strconv.Atoi(p)
				if err != nil || port < 1 || port > 65535 || h == "" {
					return nil, fmt.Errorf("line %d: Endpoint has a bad port", n)
				}
				peer.Host, peer.Port = h, port
			case "allowedips":
				for _, f := range splitList(v) {
					p, err := netip.ParsePrefix(f)
					if err != nil {
						return nil, fmt.Errorf("line %d: AllowedIPs: %q is not a CIDR range", n, f)
					}
					peer.AllowedIPs = append(peer.AllowedIPs, p)
				}
			}
		default:
			return nil, fmt.Errorf("line %d: setting outside a section", n)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	switch {
	case !sawIface || !haveKey:
		return nil, fmt.Errorf("no [Interface] PrivateKey: this is not a WireGuard client config")
	case len(cfg.Addresses) == 0:
		return nil, fmt.Errorf("no Address in [Interface]")
	case len(cfg.Peers) == 0:
		return nil, fmt.Errorf("no [Peer] section")
	}
	return &cfg, nil
}

func splitList(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseAddr accepts "10.0.0.2/32", "fd00::2/128" or a bare address.
func parseAddr(s string) (netip.Addr, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%q is not an address", s)
	}
	return a, nil
}

func decodeKey(s string) ([32]byte, error) {
	var k [32]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return k, fmt.Errorf("not a WireGuard key (expected 44 base64 characters)")
	}
	copy(k[:], b)
	return k, nil
}

func fullTunnel(ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Addr().Is4() && p.Bits() == 0 {
			return true
		}
	}
	return false
}
