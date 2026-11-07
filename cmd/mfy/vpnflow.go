package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
)

// vpnFlow is the sign-in screen: pick a provider, then that provider's credentials.
// Choosing a different provider than the current one signs out of the old one first, so
// no device keys are left registered behind its back.
func vpnFlow(ctx context.Context, cfg *config.Config) error {
	choice := providerID(*cfg)
	if cfg.NoVPN && !currentVPN(*cfg).SignedIn {
		choice = provProton
	}
	err := one(ctx, huh.NewSelect[string]().Title("Download through a VPN?").
		Description("Several servers at once, so YouTube can't throttle one address. Your IP is never used instead: if the VPN can't connect, mfetchy stops.").
		Options(
			huh.NewOption("Proton VPN", provProton),
			huh.NewOption("Mullvad (account number only)", provMullvad),
			huh.NewOption("My own WireGuard config (any provider)", provWireGuard),
			huh.NewOption("No VPN", "none"),
		).Value(&choice))
	if err != nil {
		return err
	}
	if choice == "none" {
		signOutVPN(ctx, *cfg)
		cfg.NoVPN = true
		return nil
	}
	if choice != providerID(*cfg) && currentVPN(*cfg).SignedIn {
		fmt.Println(ui.Mark(ui.Dim("·"), "signing out of "+providerLabel(providerID(*cfg))))
		signOutVPN(ctx, *cfg)
	}
	cfg.Provider = choice
	cfg.NoVPN = false
	switch choice {
	case provMullvad:
		return mullvadFlow(ctx)
	case provWireGuard:
		return wireguardFlow(ctx)
	}
	return loginFlow(ctx, protonClient(), cfg)
}

// mullvadFlow asks for the account number and checks it with Mullvad.
func mullvadFlow(ctx context.Context) error {
	p := mullvadProvider()
	for {
		var acct string
		err := one(ctx, huh.NewInput().Title("Mullvad account number").
			Description("16 digits. Kept only in a private file on this computer (mode 0600), sent only to api.mullvad.net, never shown or logged.\nytm registers one WireGuard key per connection under it (Mullvad allows 5 devices) and removes them when you sign out.").
			EchoMode(huh.EchoModePassword).Value(&acct).
			Validate(func(s string) error { _, e := mullvad.NormalizeAccount(s); return e }))
		if err != nil {
			return err
		}
		err = working(ctx, "Checking your account...", func(c context.Context) error {
			if err := p.Login(c, acct); err != nil {
				return err
			}
			_, e := p.ServersInfo(c)
			return e
		})
		if err == nil {
			fmt.Println(ui.Mark(ui.OK("✓"), "signed in to Mullvad ("+p.Who()+")"))
			return nil
		}
		if errors.Is(err, errBack) || errors.Is(err, context.Canceled) {
			return err
		}
		fmt.Println(ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
		var ae *mullvad.APIError
		if errors.As(err, &ae) && ae.Code != "INVALID_ACCOUNT" {
			p.Logout(ctx)
			return errors.New("stopping here; fix the above, then try again in Settings")
		}
		again := true
		if one(ctx, huh.NewConfirm().Title("Try again?").Value(&again)) != nil || !again {
			p.Logout(ctx)
			return errors.New("not signed in; try again in Settings")
		}
	}
}

// wireguardFlow imports standard wg-quick .conf files.
func wireguardFlow(ctx context.Context) error {
	p := wireguardProvider()
	for {
		var path string
		err := one(ctx, huh.NewInput().Title("WireGuard config: a .conf file, or a folder of them").
			Description("From any provider (IVPN, Windscribe, AirVPN, your own server...) or `wg-quick` setup.\nytm copies it into its private settings folder (mode 0600) and uses each file as one connection.\nScript lines (PostUp etc.) are ignored; nothing on your system is changed.").
			Value(&path).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("required")
			}
			return nil
		}))
		if err != nil {
			return err
		}
		n, err := p.Import(config.ExpandPath(strings.Trim(path, `"' `)))
		if err == nil {
			fmt.Println(ui.Mark(ui.OK("✓"), "imported "+count(n, "config file")))
			return nil
		}
		fmt.Println(ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
		again := true
		if one(ctx, huh.NewConfirm().Title("Try another path?").Value(&again)) != nil || !again {
			return errors.New("no config imported; try again in Settings")
		}
	}
}
