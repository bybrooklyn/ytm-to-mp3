// Package vpnprovider defines what the tunnel pool (internal/vpn) needs from a VPN
// provider, so Proton, Mullvad and "any WireGuard config" all plug into the same pool.
//
// A provider knows how to list servers, how to give each simultaneous tunnel its own
// WireGuard identity (registering it with the provider where that is needed) and how to
// turn a server plus an identity into a tunnel configuration. Everything else (ranking,
// back-off, spares, the SOCKS front door, "stop rather than fall back to your own IP")
// is the pool's and is the same for every provider.
package vpnprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
)

// Server is one usable WireGuard endpoint.
type Server struct {
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	City      string  `json:"city"`
	IP        string  `json:"ip"`             // entry IP (or host) the tunnel connects to
	Port      int     `json:"port,omitempty"` // 0 = 51820
	PublicKey string  `json:"public_key"`     // WireGuard (X25519) public key, base64
	Ref       string  `json:"ref,omitempty"`  // provider-private handle (e.g. which config file)
	Score     float64 `json:"score"`          // the provider's own rating; lower is better
	Load      int     `json:"load"`           // percent, 0-100
	Tier      int     `json:"tier"`
	Features  int     `json:"features"`
	Status    int     `json:"status"`
	Lat       float64 `json:"lat,omitempty"` // where the server is (0,0 = unknown, e.g. an older cache)
	Long      float64 `json:"long,omitempty"`
}

// Key is the WireGuard identity of one tunnel.
type Key struct {
	Private [32]byte // X25519, clamped
	// PEM is provider-specific public-key material (Proton wants the Ed25519 key as PEM).
	PEM string
	// Address is the tunnel address(es) the provider assigned this key, if it assigns
	// any (Mullvad does, per key).
	Address []netip.Addr
}

// Location is where the provider places this connection (the user's own IP).
type Location struct {
	Lat, Long float64
	Country   string
}

// ErrNoLocation is returned by providers that cannot say where the user is.
var ErrNoLocation = errors.New("location unknown")

// ServerList is what a provider reports about the account and its servers.
type ServerList struct {
	Servers    []Server
	MaxConnect int // tunnels the account may hold at once (0 = unknown)
	MaxTier    int
}

// TunnelConfig is everything needed to bring one WireGuard tunnel up.
type TunnelConfig struct {
	Endpoint   string // host:port; a host name is resolved before the tunnel exists
	PeerKey    [32]byte
	PrivateKey [32]byte
	Preshared  *[32]byte
	Addresses  []netip.Addr // our address(es) inside the tunnel
	DNS        []netip.Addr // resolvers reached through the tunnel; names are never looked up outside it
	MTU        int          // 0 = 1380
}

// Provider is a VPN service mfetchy can build a tunnel pool from.
type Provider interface {
	// ID is the stable short name used in settings: "proton", "mullvad", "wireguard".
	ID() string
	// Label is the name shown to people: "Proton VPN", "Mullvad", ...
	Label() string
	// AccountID is a short non-reversible tag of the account, so one account's cached
	// server list is never used for another.
	AccountID() string
	// IdentityAt returns the WireGuard identity for slot (one slot per simultaneous
	// tunnel), registering it with the provider when needed.
	IdentityAt(ctx context.Context, slot int) (*Key, error)
	// MaxSlots bounds how many identities the provider can ever hold.
	MaxSlots() int
	// Location asks where the user is, for ranking servers by distance; ErrNoLocation if
	// the provider cannot say.
	Location(ctx context.Context) (Location, error)
	// ServersInfo lists the usable servers and the account's limit on simultaneous tunnels.
	ServersInfo(ctx context.Context) (ServerList, error)
	// Tunnel builds the configuration for connecting k to s.
	Tunnel(s Server, k *Key) (TunnelConfig, error)
}

// AccountHash is a short non-reversible tag of an account name or number.
func AccountHash(account string) string {
	if account == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(account)))
	return hex.EncodeToString(sum[:6])
}
