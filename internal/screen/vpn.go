package screen

import (
	"fmt"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider/mullvad"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider/wgconf"
)

// VPNInfo is the chosen VPN provider and whether it is set up (no network involved).
type VPNInfo struct {
	Provider string // "Proton VPN", "Mullvad", "WireGuard config"
	SignedIn bool
	Who      string // "me@proton.me", "account ending 1234", "2 config files"
}

// DetectVPN reads the chosen provider's saved state. Provider ids are those saved in
// config.Provider ("" is Proton, the original and default).
func DetectVPN(cfg config.Config) VPNInfo {
	switch cfg.Provider {
	case "mullvad":
		p := mullvad.New(config.MullvadPath())
		return VPNInfo{Provider: "Mullvad", SignedIn: p.SignedIn(), Who: p.Who()}
	case "wireguard":
		p := wgconf.New()
		who := ""
		if p.SignedIn() {
			who = fmt.Sprintf("%d config file", p.Files())
			if p.Files() != 1 {
				who += "s"
			}
		}
		return VPNInfo{Provider: "WireGuard config", SignedIn: p.SignedIn(), Who: who}
	}
	info := VPNInfo{Provider: "Proton VPN"}
	if p, err := proton.SessionPath(); err == nil {
		if c := proton.New(p); c.SignedIn() {
			info.SignedIn, info.Who = true, c.Session.Username
		}
	}
	return info
}
