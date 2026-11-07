package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/proton"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpn"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
)

// VPNSignIn is the "set up a VPN" screen: pick a provider, then that provider's credentials,
// and save the choice. Choosing a different provider than the current one signs out of the
// old one first, so no device keys are left registered behind its back.
func VPNSignIn(ctx context.Context, u *ui.UI, p Prompter) error {
	c := config.Load()
	err := vpnFlow(ctx, u, p, &c)
	if serr := c.Save(); serr != nil && err == nil {
		err = serr
	}
	return err
}

// VPNSignOut ends the chosen provider's sign-in, at the provider too where it can: Mullvad's
// device keys are removed from the account; Proton's stay listed until removed at
// account.protonvpn.com.
func VPNSignOut(ctx context.Context) { signOutVPN(ctx, config.Load()) }

func vpnFlow(ctx context.Context, u *ui.UI, p Prompter, cfg *config.Config) error {
	choice := providerID(*cfg)
	if cfg.NoVPN && !currentVPN(*cfg).SignedIn {
		choice = provProton
	}
	choice, err := p.Select(ctx, "Download through a VPN?",
		"Several servers at once, so YouTube can't throttle one address. Your IP is never used instead: if the VPN can't connect, mfetchy stops.",
		[]Option{
			{Value: provProton, Label: "Proton VPN"},
			{Value: provMullvad, Label: "Mullvad (account number only)"},
			{Value: provWireGuard, Label: "My own WireGuard config (any provider)"},
			{Value: "none", Label: "No VPN"},
		}, choice)
	if err != nil {
		return err
	}
	if choice == "none" {
		signOutVPN(ctx, *cfg)
		cfg.NoVPN = true
		return nil
	}
	if choice != providerID(*cfg) && currentVPN(*cfg).SignedIn {
		u.Styled(ui.Mark(ui.Dim("·"), "signing out of "+providerLabel(providerID(*cfg))))
		signOutVPN(ctx, *cfg)
	}
	cfg.Provider = choice
	cfg.NoVPN = false
	switch choice {
	case provMullvad:
		return mullvadFlow(ctx, u, p)
	case provWireGuard:
		return wireguardFlow(ctx, u, p)
	}
	return protonLoginFlow(ctx, u, p, protonClient(), cfg)
}

// tryAgain asks whether to retry after a failure, with the failure in the question.
func tryAgain(ctx context.Context, p Prompter, title, why string) bool {
	again, err := p.Confirm(ctx, Question{Title: title, Detail: why, Yes: "Yes", No: "No", Default: true})
	return err == nil && again
}

// mullvadFlow asks for the account number and checks it with Mullvad.
func mullvadFlow(ctx context.Context, u *ui.UI, p Prompter) error {
	mp := mullvadProvider()
	for {
		ans, err := p.Form(ctx, FormSpec{Fields: []Field{{
			Key: "account", Kind: FieldSecret, Label: "Mullvad account number",
			Detail: "16 digits. Kept only in a private file on this computer (mode 0600), sent only to api.mullvad.net, never shown or logged.\n" +
				"mfetchy registers one WireGuard key per connection under it (Mullvad allows 5 devices) and removes them when you sign out.",
			Validate: func(s string) error { _, e := mullvad.NormalizeAccount(s); return e },
		}}})
		if err != nil {
			return err
		}
		err = p.Working(ctx, "Checking your account...", func(c context.Context) error {
			if err := mp.Login(c, ans["account"]); err != nil {
				return err
			}
			_, e := mp.ServersInfo(c)
			return e
		})
		if err == nil {
			u.Good("signed in to Mullvad (%s)", mp.Who())
			return nil
		}
		if errors.Is(err, ErrBack) || errors.Is(err, context.Canceled) {
			return err
		}
		u.Bad("%s", err.Error())
		var ae *mullvad.APIError
		if errors.As(err, &ae) && ae.Code != "INVALID_ACCOUNT" {
			mp.Logout(ctx)
			return errors.New("stopping here; fix the above, then try again in Settings")
		}
		if !tryAgain(ctx, p, "Try again?", ui.Sanitize(err.Error())) {
			mp.Logout(ctx)
			return errors.New("not signed in; try again in Settings")
		}
	}
}

// wireguardFlow imports standard wg-quick .conf files.
func wireguardFlow(ctx context.Context, u *ui.UI, p Prompter) error {
	wp := wireguardProvider()
	for {
		path, err := p.File(ctx, FileRequest{
			Title: "WireGuard config: a .conf file, or a folder of them",
			Detail: "From any provider (IVPN, Windscribe, AirVPN, your own server...) or `wg-quick` setup.\n" +
				"mfetchy copies it into its private settings folder (mode 0600) and uses each file as one connection.\n" +
				"Script lines (PostUp etc.) are ignored; nothing on your system is changed.",
			File: true, Folder: true, Exts: []string{".conf"},
			Validate: func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("required")
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
		n, err := wp.Import(config.ExpandPath(strings.Trim(path, `"' `)))
		if err == nil {
			u.Good("imported %s", count(n, "config file"))
			return nil
		}
		u.Bad("%s", err.Error())
		if !tryAgain(ctx, p, "Try another path?", ui.Sanitize(err.Error())) {
			return errors.New("no config imported; try again in Settings")
		}
	}
}

// protonLoginFlow signs in to Proton (with 2FA and retries); vpnFlow has already asked which
// provider to use.
func protonLoginFlow(ctx context.Context, u *ui.UI, p Prompter, pc *proton.Client, cfg *config.Config) error {
	cfg.NoVPN = false
	user := ""
	for {
		ans, err := p.Form(ctx, FormSpec{Fields: []Field{
			{Key: "user", Label: "Proton email or username", Default: user, Validate: func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("required")
				}
				return nil
			}},
			{Key: "pass", Kind: FieldSecret, Label: "Password", Detail: "only sent to Proton, never saved"},
		}})
		if err != nil {
			return err
		}
		user = strings.TrimSpace(ans["user"])
		err = p.Working(ctx, "Signing in...", func(c context.Context) error {
			return pc.Login(c, user, ans["pass"])
		})
		if errors.Is(err, proton.ErrNeed2FA) {
			err = ask2FA(ctx, u, p, pc)
		}
		if err == nil {
			err = p.Working(ctx, "Checking your VPN access...", func(c context.Context) error {
				_, e := pc.Identity(c)
				return e
			})
		}
		if err == nil {
			u.Good("signed in as %s", pc.Session.Username)
			return nil
		}
		if errors.Is(err, ErrBack) || errors.Is(err, context.Canceled) {
			return err // Esc or Ctrl-C at the 2FA prompt
		}
		u.Bad("%s", err.Error())
		var ae *proton.APIError
		if errors.As(err, &ae) && (ae.RateLimited() || ae.BadVersion() || ae.Code == 9001) {
			// retrying can't help and a rate limit gets longer with every attempt
			return errors.New("stopping here; fix the above, then try again in Settings (set MFY_DEBUG=1 to see Proton's raw reply)")
		}
		if !tryAgain(ctx, p, "Try again?", ui.Sanitize(err.Error())) {
			return errors.New("not signed in; try again in Settings")
		}
	}
}

func ask2FA(ctx context.Context, u *ui.UI, p Prompter, pc *proton.Client) error {
	for try := 0; try < 3; try++ {
		ans, err := p.Form(ctx, FormSpec{Fields: []Field{{Key: "code", Label: "2FA code", Detail: "from your authenticator app"}}})
		if err != nil {
			return err
		}
		err = p.Working(ctx, "Checking code...", func(c context.Context) error {
			return pc.Submit2FA(c, strings.TrimSpace(ans["code"]))
		})
		if err == nil {
			return nil
		}
		u.Bad("%s", err.Error())
	}
	return errors.New("2FA failed")
}

// VPNTest connects the configured tunnels and shows where each one exits.
func VPNTest(ctx context.Context, u *ui.UI) error {
	cfg := config.Load()
	v := currentVPN(cfg)
	label := providerLabel(providerID(cfg))
	u.Head("Settings · %s", tilde(config.Path()))
	u.Item("music folder", "%s", tilde(config.ExpandPath(orDefault(cfg.Out, config.DefaultOut()))))
	u.Item("format      ", "%s", orDefault(cfg.Format, config.DefaultFormat))
	u.Item("VPN exits   ", "%s", connectionsLabel(cfg, v))
	if !v.SignedIn {
		u.Warn("VPN: not signed in · sign in in Settings")
		return nil
	}
	if cfg.NoVPN {
		u.Note("VPN: %s (%s), but turned off", label, v.Who)
		return nil
	}
	u.Note("VPN: %s (%s) · connecting...", label, v.Who)
	pool, err := vpn.OpenWith(ctx, v.Prov, connections(cfg, v), u.Logf, vpn.Options{})
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, x := range pool.Exits() {
		c, _ := vpn.NewClient(x.Proxy)
		c.Timeout = 20 * time.Second
		ip := "?"
		if resp, err := c.Get(ipifyURL); err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			ip = strings.TrimSpace(string(b))
		}
		u.Item(fmt.Sprintf("%-22s", x.Name), "exits at %s", ip)
	}
	return nil
}

// ipifyURL is the address that says which IP a request came from.
const ipifyURL = "https://api.ipify.org"
