// Package vpn gives ytm several network "exits" to spread downloads across.
//
// An Exit is a SOCKS5 proxy URL on 127.0.0.1 in front of one userspace WireGuard
// tunnel to a server of any vpnprovider.Provider (Proton, Mullvad, a WireGuard config).
// Nothing is installed or changed on the system and no
// root is needed, on any OS.
package vpn

import "github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider"

// Server, Key and Location are the provider-neutral types the pool works with.
type (
	Server   = vpnprovider.Server
	Key      = vpnprovider.Key
	Location = vpnprovider.Location
)

// Exit is one way out to the internet. An empty Proxy means the normal connection.
type Exit struct {
	Name  string
	Proxy string // e.g. socks5h://user:pass@127.0.0.1:1080 ; "" = direct
}

// Direct is the machine's normal connection.
var Direct = Exit{Name: "direct"}
