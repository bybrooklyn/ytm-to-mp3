package main

import (
	"context"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// What lives in internal/core, under the names the screens here use. The pipeline, the
// sources and every flow are there (they ask through a core.Prompter); this package keeps
// the terminal's menus and its forms.

func tilde(p string) string           { return core.Tilde(p) }
func orDefault(s, d string) string    { return core.OrDefault(s, d) }
func count(n int, noun string) string { return core.Count(n, noun) }

type vpnState = core.VPNState

func providerID(cfg config.Config) string                   { return core.ProviderID(cfg) }
func providerLabel(id string) string                        { return core.ProviderLabel(id) }
func currentVPN(cfg config.Config) vpnState                 { return core.CurrentVPN(cfg) }
func connectionsLabel(cfg config.Config, v vpnState) string { return core.ConnectionsLabel(cfg, v) }

func appleSignedIn() bool                             { return core.AppleSignedIn() }
func appleStatus(ctx context.Context, u *ui.UI) error { return core.AppleStatus(ctx, u) }
func appleMatch(ctx context.Context, u *ui.UI, o opts, paths []string) error {
	return core.AppleMatch(ctx, u, o.core(), newPrompter(u), paths)
}

func tidalSignedIn() bool { return core.TidalSignedIn() }

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
