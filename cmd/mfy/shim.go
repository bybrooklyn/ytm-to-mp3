package main

import (
	"context"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/sidecar"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/wgconf"
)

// What lives in internal/core now, under the names the screens here use. The pipeline,
// the sources and the flows that need no form are there; the menus and sign-in forms stay
// in this package and call these.

func tilde(p string) string           { return core.Tilde(p) }
func orDefault(s, d string) string    { return core.OrDefault(s, d) }
func count(n int, noun string) string { return core.Count(n, noun) }
func fileExists(p string) bool        { return core.FileExists(p) }

const (
	provProton    = core.ProvProton
	provMullvad   = core.ProvMullvad
	provWireGuard = core.ProvWireGuard

	tidalWarning   = core.TidalWarning
	premiumWarning = core.PremiumWarning
)

type vpnState = core.VPNState

func providerID(cfg config.Config) string                   { return core.ProviderID(cfg) }
func providerLabel(id string) string                        { return core.ProviderLabel(id) }
func currentVPN(cfg config.Config) vpnState                 { return core.CurrentVPN(cfg) }
func mullvadProvider() *mullvad.Provider                    { return core.MullvadProvider() }
func wireguardProvider() *wgconf.Provider                   { return core.WireguardProvider() }
func signOutVPN(ctx context.Context, cfg config.Config)     { core.SignOutVPN(ctx, cfg) }
func connections(cfg config.Config, v vpnState) int         { return core.Connections(cfg, v) }
func connectionsLabel(cfg config.Config, v vpnState) string { return core.ConnectionsLabel(cfg, v) }

func appleSignedIn() bool                             { return core.AppleSignedIn() }
func appleLogin(ctx context.Context, u *ui.UI) error  { return core.AppleLogin(ctx, u) }
func appleLogout(u *ui.UI) error                      { return core.AppleLogout(u) }
func appleStatus(ctx context.Context, u *ui.UI) error { return core.AppleStatus(ctx, u) }
func appleMatch(ctx context.Context, u *ui.UI, o opts, paths []string) error {
	return core.AppleMatch(ctx, u, o.core(), newPrompter(u), paths)
}

func tidalSignedIn() bool        { return core.TidalSignedIn() }
func tidalClient() *tidal.Client { return core.TidalClient() }
func tidalLogout(u *ui.UI) error { return core.TidalLogout(u) }

func checkCookies(p string) error { return core.CheckCookies(p) }
func premiumUpgrade(ctx context.Context, u *ui.UI, o opts, auto bool) error {
	return core.PremiumUpgrade(ctx, u, o.core(), newPrompter(u), auto)
}

func allAccountsFlow(ctx context.Context, u *ui.UI, o opts) error {
	return core.AllAccountsFlow(ctx, u, o.core(), newPrompter(u))
}
func liveLibraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	return core.LiveLibraryFlow(ctx, u, o.core(), newPrompter(u))
}
func lyricsLibraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	return core.LyricsLibraryFlow(ctx, u, o.core())
}
func runDoctor(ctx context.Context, u *ui.UI, o opts) error { return core.RunDoctor(ctx, u, o.core()) }
func runTag(ctx context.Context, u *ui.UI, o opts, dir string, files ...string) error {
	return core.RunTag(ctx, u, o.core(), dir, files...)
}

func chooseYouTubeBrowser(ctx context.Context) error {
	return core.ChooseYouTubeBrowser(ctx, termPrompter{})
}
func chooseBandcampBrowser(ctx context.Context) error {
	return core.ChooseBandcampBrowser(ctx, termPrompter{})
}

func sidecarOptions(cfg config.Config) (sidecar.Options, bool) { return core.SidecarOptions(cfg) }
func audioFiles(dir string) []string                           { return core.AudioFiles(dir) }
