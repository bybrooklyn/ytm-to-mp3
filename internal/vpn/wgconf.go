package vpn

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// WG is a parsed WireGuard client config (the wg-quick format).
type WG struct {
	PrivateKey string
	Addresses  []netip.Prefix
	DNS        []string
	MTU        int
	PublicKey  string
	PSK        string
	Endpoint   string
	AllowedIPs string
	Keepalive  string
}

// ParseWG reads a wg-quick style config with one [Peer].
func ParseWG(text string) (*WG, error) {
	w := &WG{}
	section := ""
	peers := 0
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] "))
			if section == "peer" {
				peers++
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch section {
		case "interface":
			switch k {
			case "privatekey":
				w.PrivateKey = v
			case "address":
				for _, a := range strings.Split(v, ",") {
					p, err := netip.ParsePrefix(strings.TrimSpace(a))
					if err != nil {
						return nil, fmt.Errorf("bad Address %q", a)
					}
					w.Addresses = append(w.Addresses, p)
				}
			case "dns":
				for _, d := range strings.Split(v, ",") {
					if d = strings.TrimSpace(d); d != "" {
						w.DNS = append(w.DNS, d)
					}
				}
			case "mtu":
				w.MTU, _ = strconv.Atoi(v)
			}
		case "peer":
			if peers > 1 {
				continue
			}
			switch k {
			case "publickey":
				w.PublicKey = v
			case "presharedkey":
				w.PSK = v
			case "endpoint":
				w.Endpoint = v
			case "allowedips":
				w.AllowedIPs = v
			case "persistentkeepalive":
				w.Keepalive = v
			}
		}
	}
	switch {
	case w.PrivateKey == "":
		return nil, errors.New("not a WireGuard config: no PrivateKey")
	case w.PublicKey == "" || w.Endpoint == "":
		return nil, errors.New("not a WireGuard config: [Peer] needs PublicKey and Endpoint")
	case len(w.Addresses) == 0:
		return nil, errors.New("not a WireGuard config: no Address")
	}
	if w.AllowedIPs == "" {
		w.AllowedIPs = "0.0.0.0/0, ::/0"
	}
	return w, nil
}

// SetConf renders the stripped config that `wg setconf` accepts (no Address/DNS/MTU).
func (w *WG) SetConf() string {
	var b strings.Builder
	b.WriteString("[Interface]\nPrivateKey = " + w.PrivateKey + "\n\n[Peer]\nPublicKey = " + w.PublicKey + "\n")
	if w.PSK != "" {
		b.WriteString("PresharedKey = " + w.PSK + "\n")
	}
	b.WriteString("Endpoint = " + w.Endpoint + "\nAllowedIPs = 0.0.0.0/0, ::/0\n")
	if w.Keepalive != "" {
		b.WriteString("PersistentKeepalive = " + w.Keepalive + "\n")
	} else {
		b.WriteString("PersistentKeepalive = 25\n")
	}
	return b.String()
}

// HasV6 reports whether the tunnel address list includes IPv6.
func (w *WG) HasV6() bool {
	for _, a := range w.Addresses {
		if a.Addr().Is6() {
			return true
		}
	}
	return false
}
