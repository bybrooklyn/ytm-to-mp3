package core

import (
	"context"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/sidecar"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/wgconf"
)

// The rest of this file is the surface the terminal program's own screens (menus, sign-in
// forms, Settings) use. They are thin: the work is in the files they call.

// Provider ids as saved in config.Provider.
const (
	ProvProton    = provProton
	ProvMullvad   = provMullvad
	ProvWireGuard = provWireGuard
)

// VPNState is the chosen VPN provider and whether it is set up.
type VPNState = vpnState

func ProviderID(cfg config.Config) string             { return providerID(cfg) }
func ProviderLabel(id string) string                  { return providerLabel(id) }
func ProviderAPIHost(id string) string                { return providerAPIHost(id) }
func CurrentVPN(cfg config.Config) VPNState           { return currentVPN(cfg) }
func MullvadProvider() *mullvad.Provider              { return mullvadProvider() }
func WireguardProvider() *wgconf.Provider             { return wireguardProvider() }
func SignOutVPN(ctx context.Context, c config.Config) { signOutVPN(ctx, c) }

// Connections is how many VPN exits a run opens; ConnectionsLabel is that for display.
func Connections(cfg config.Config, v VPNState) int         { return connections(cfg, v) }
func ConnectionsLabel(cfg config.Config, v VPNState) string { return connectionsLabel(cfg, v) }

func AppleSignedIn() bool                             { return appleSignedIn() }
func AppleLogin(ctx context.Context, u *ui.UI) error  { return appleLogin(ctx, u) }
func AppleLogout(u *ui.UI) error                      { return appleLogout(u) }
func AppleStatus(ctx context.Context, u *ui.UI) error { return appleStatus(ctx, u) }

// AppleMatch finds a YouTube library on Apple Music and downloads the chosen matches. If p
// also has a SelectBoth method, the two questions it asks are shown as one form.
func AppleMatch(ctx context.Context, u *ui.UI, o Options, p Prompter, paths []string) error {
	return appleMatch(ctx, u, o.internal(p), paths)
}

func TidalSignedIn() bool        { return tidalSignedIn() }
func TidalClient() *tidal.Client { return tidalClient() }
func TidalLogout(u *ui.UI) error { return tidalLogout(u) }

// TidalWarning is the terms notice shown before signing in to Tidal.
const TidalWarning = tidalWarning

func TidalDoctor(ctx context.Context, bad func(fix, f string, a ...any), ok, note func(f string, a ...any)) {
	tidalDoctor(ctx, bad, ok, note)
}

// PremiumWarning is the notice shown when the Premium upgrade is turned on.
const PremiumWarning = premiumWarning

func CheckCookies(path string) error { return checkCookies(path) }

func PremiumUpgrade(ctx context.Context, u *ui.UI, o Options, p Prompter, auto bool) error {
	return premiumUpgrade(ctx, u, o.internal(p), auto)
}

func AllAccountsFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	return allAccountsFlow(ctx, u, o.internal(p))
}

func LiveLibraryFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	return liveLibraryFlow(ctx, u, o.internal(p))
}

func LyricsLibraryFlow(ctx context.Context, u *ui.UI, o Options) error {
	return lyricsLibraryFlow(ctx, u, o.internal(nil))
}

func RunDoctor(ctx context.Context, u *ui.UI, o Options) error {
	return runDoctor(ctx, u, o.internal(nil))
}

// RunTag fixes the tags and names of dir (or only files under it).
func RunTag(ctx context.Context, u *ui.UI, o Options, dir string, files ...string) error {
	return runTag(ctx, u, o.internal(nil), dir, files...)
}

// AudioFiles lists the songs under dir.
func AudioFiles(dir string) []string { return audioFiles(dir) }

// SidecarOptions is what Settings says to write; ok is false when all of it is off.
func SidecarOptions(cfg config.Config) (o sidecar.Options, ok bool) { return sidecarOptions(cfg) }
