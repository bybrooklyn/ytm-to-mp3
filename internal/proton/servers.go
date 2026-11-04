package proton

import (
	"context"
	"errors"
)

// Server is one usable WireGuard endpoint.
type Server struct {
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	City      string  `json:"city"`
	IP        string  `json:"ip"`         // entry IP
	PublicKey string  `json:"public_key"` // WireGuard (X25519) public key, base64
	Score     float64 `json:"score"`      // Proton's own rating; lower is better
	Load      int     `json:"load"`       // percent, 0-100
	Tier      int     `json:"tier"`
	Features  int     `json:"features"`
	Status    int     `json:"status"`
	Lat       float64 `json:"lat,omitempty"` // where the server is (0,0 = unknown, e.g. an older cache)
	Long      float64 `json:"long,omitempty"`
}

// Location is where Proton places this connection (the user's own IP).
type Location struct {
	Lat, Long float64
	Country   string
}

// Location asks Proton where the current connection is, for ranking servers by distance.
func (c *Client) Location(ctx context.Context) (Location, error) {
	var l Location
	err := c.do(ctx, "GET", "/vpn/v1/location", true, nil, &l)
	return l, err
}

// ServerList is what the API reports about the account and its servers.
type ServerList struct {
	Servers    []Server
	MaxConnect int // devices the plan allows at once (0 = unknown)
	MaxTier    int
}

type logical struct {
	Name        string
	ExitCountry string
	City        string
	Tier        int
	Features    int
	Status      int
	Score       float64
	Load        int
	Location    struct{ Lat, Long float64 }
	Servers     []struct {
		EntryIP         string
		Status          int
		X25519PublicKey string
	}
}

// Servers returns every plain, online server the account may use (no Secure Core / Tor).
// It also records the plan's device limit in c.MaxConnect; code that runs concurrently
// should use ServersInfo instead, which touches no shared state.
func (c *Client) Servers(ctx context.Context) ([]Server, error) {
	l, err := c.ServersInfo(ctx)
	if err != nil {
		return nil, err
	}
	if l.MaxConnect > 0 {
		c.MaxConnect = l.MaxConnect
	}
	return l.Servers, nil
}

// ServersInfo is Servers plus the plan limits, with no side effects on the client.
func (c *Client) ServersInfo(ctx context.Context) (ServerList, error) {
	var vp struct {
		VPN struct{ MaxTier, MaxConnect int }
	}
	maxTier := 0 // if the plan can't be read, assume the smallest so we don't try servers that will refuse us
	out := ServerList{}
	if err := c.do(ctx, "GET", "/vpn/v2", true, nil, &vp); err != nil {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status == 401 || ae.Status == 403 || ae.BadVersion() {
			return out, err // network or auth problem: say so rather than guess
		}
	} else {
		maxTier = vp.VPN.MaxTier
		out.MaxTier, out.MaxConnect = vp.VPN.MaxTier, vp.VPN.MaxConnect
	}
	var r struct{ LogicalServers []logical }
	if err := c.do(ctx, "GET", "/vpn/v1/logicals?SecureCoreFilter=all&WithState=true", true, nil, &r); err != nil {
		return out, err
	}
	out.Servers = filterServers(r.LogicalServers, maxTier)
	return out, nil
}

func filterServers(ls []logical, maxTier int) []Server {
	var out []Server
	for _, l := range ls {
		if l.Status != 1 || l.Tier > maxTier || l.Features&(1|2) != 0 { // secure core, tor
			continue
		}
		for _, s := range l.Servers {
			if s.Status == 1 && s.X25519PublicKey != "" && s.EntryIP != "" {
				out = append(out, Server{Name: l.Name, Country: l.ExitCountry, City: l.City,
					IP: s.EntryIP, PublicKey: s.X25519PublicKey, Score: l.Score, Load: l.Load,
					Tier: l.Tier, Features: l.Features, Status: l.Status,
					Lat: l.Location.Lat, Long: l.Location.Long})
				break
			}
		}
	}
	return out
}
