package proton

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"strconv"

	"github.com/bybrooklyn/mfetchy/internal/vpnprovider"
)

// Client is a vpnprovider.Provider.
var _ vpnprovider.Provider = (*Client)(nil)

// ID is the provider's name in settings.
func (c *Client) ID() string { return "proton" }

// Label is the provider's name for people.
func (c *Client) Label() string { return "Proton VPN" }

// AccountID tags the signed-in account (see vpnprovider.AccountHash).
func (c *Client) AccountID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Session == nil {
		return ""
	}
	return vpnprovider.AccountHash(c.Session.Username)
}

// MaxSlots is how many identities ytm keeps.
func (c *Client) MaxSlots() int { return MaxSlots }

// Tunnel builds the connection to s. Proton gives every client the same tunnel address
// and resolver.
func (c *Client) Tunnel(s Server, k *WGKey) (vpnprovider.TunnelConfig, error) {
	pub, err := base64.StdEncoding.DecodeString(s.PublicKey)
	if err != nil || len(pub) != 32 {
		return vpnprovider.TunnelConfig{}, errors.New("bad server key")
	}
	port := s.Port
	if port == 0 {
		port = 51820
	}
	cfg := vpnprovider.TunnelConfig{
		Endpoint:   s.IP + ":" + strconv.Itoa(port),
		PrivateKey: k.Private,
		Addresses:  []netip.Addr{netip.MustParseAddr("10.2.0.2")},
		DNS:        []netip.Addr{netip.MustParseAddr("10.2.0.1")},
	}
	copy(cfg.PeerKey[:], pub)
	return cfg, nil
}
