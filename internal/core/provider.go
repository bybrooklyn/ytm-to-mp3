package core

import (
	"context"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/proton"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/wgconf"
)

// Provider ids as saved in config.Provider ("" is Proton, the original and default).
const (
	provProton    = "proton"
	provMullvad   = "mullvad"
	provWireGuard = "wireguard"
)

// ProtonClient is the Proton API client with the saved session.
func ProtonClient() *proton.Client { return protonClient() }

func protonClient() *proton.Client {
	p, _ := proton.SessionPath()
	return proton.New(p)
}

// vpnState is the chosen VPN provider and whether it is set up.
type vpnState struct {
	Prov     vpnprovider.Provider
	SignedIn bool
	Who      string // "me@proton.me", "account ending 1234", "2 config files"
}

// providerID is the saved choice, with the default filled in.
func providerID(cfg config.Config) string {
	switch cfg.Provider {
	case provMullvad, provWireGuard:
		return cfg.Provider
	}
	return provProton
}

// providerLabel is a provider's name for people.
func providerLabel(id string) string {
	switch id {
	case provMullvad:
		return "Mullvad"
	case provWireGuard:
		return "WireGuard config"
	}
	return "Proton VPN"
}

func mullvadProvider() *mullvad.Provider { return mullvad.New(config.MullvadPath()) }

func wireguardProvider() *wgconf.Provider { return wgconf.New() }

// currentVPN loads the chosen provider and its sign-in state (no network).
func currentVPN(cfg config.Config) vpnState {
	switch providerID(cfg) {
	case provMullvad:
		p := mullvadProvider()
		return vpnState{Prov: p, SignedIn: p.SignedIn(), Who: p.Who()}
	case provWireGuard:
		p := wireguardProvider()
		who := ""
		if p.SignedIn() {
			who = count(p.Files(), "config file")
		}
		return vpnState{Prov: p, SignedIn: p.SignedIn(), Who: who}
	}
	pc := protonClient()
	who := ""
	if pc.SignedIn() {
		who = pc.Session.Username
	}
	return vpnState{Prov: pc, SignedIn: pc.SignedIn(), Who: who}
}

// vpnWanted reports whether this run is meant to use the VPN: it is set up and neither
// the setting nor --no-vpn turns it off. When true, nothing may fall back to your own IP.
func vpnWanted(cfg config.Config, o opts) bool {
	return !o.noVPN && !cfg.NoVPN && currentVPN(cfg).SignedIn
}

// signOutVPN ends the chosen provider's sign-in (also at the provider where it can).
func signOutVPN(ctx context.Context, cfg config.Config) {
	switch providerID(cfg) {
	case provMullvad:
		mullvadProvider().Logout(ctx) // frees the device slots too
	case provWireGuard:
		_ = wireguardProvider().Remove()
	default:
		protonClient().Revoke(ctx)
	}
}
