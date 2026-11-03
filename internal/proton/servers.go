package proton

import (
	"context"
	"errors"
	"math/rand"
	"sort"
)

// Server is one usable WireGuard endpoint.
type Server struct {
	Name, Country, City string
	IP                  string // entry IP
	PublicKey           string // WireGuard (X25519) public key, base64
	Score               float64
	Load                int
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
	Servers     []struct {
		EntryIP         string
		Status          int
		X25519PublicKey string
	}
}

// Servers returns every plain, online server the account may use (no Secure Core / Tor).
func (c *Client) Servers(ctx context.Context) ([]Server, error) {
	var vp struct {
		VPN struct{ MaxTier, MaxConnect int }
	}
	maxTier := 0 // if the plan can't be read, assume the smallest so we don't try servers that will refuse us
	if err := c.do(ctx, "GET", "/vpn/v2", true, nil, &vp); err != nil {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status == 401 || ae.Status == 403 || ae.BadVersion() {
			return nil, err // network or auth problem: say so rather than guess
		}
	} else {
		maxTier = vp.VPN.MaxTier
		c.MaxConnect = vp.VPN.MaxConnect
	}
	var r struct{ LogicalServers []logical }
	if err := c.do(ctx, "GET", "/vpn/v1/logicals?SecureCoreFilter=all&WithState=true", true, nil, &r); err != nil {
		return nil, err
	}
	return filterServers(r.LogicalServers, maxTier), nil
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
					IP: s.EntryIP, PublicKey: s.X25519PublicKey, Score: l.Score, Load: l.Load})
				break
			}
		}
	}
	return out
}

// Pick chooses n servers from different countries where possible, favouring low score,
// skipping any in avoid. Which of the good ones is random so runs spread over servers.
func Pick(all []Server, n int, avoid map[string]bool) ([]Server, error) {
	byCountry := map[string][]Server{}
	for _, s := range all {
		if !avoid[s.Name] {
			byCountry[s.Country] = append(byCountry[s.Country], s)
		}
	}
	if len(byCountry) == 0 {
		return nil, errors.New("no usable Proton servers for this account")
	}
	var best []Server
	for _, list := range byCountry {
		sort.Slice(list, func(i, j int) bool { return list[i].Score < list[j].Score })
		top := list[:min(len(list), 3)]
		best = append(best, top[rand.Intn(len(top))])
	}
	sort.Slice(best, func(i, j int) bool { return best[i].Score < best[j].Score })
	// take from the best half of countries, shuffled, to vary exits between runs
	pool := best[:min(len(best), max(n, (len(best)+1)/2))]
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:min(n, len(pool))], nil
}
