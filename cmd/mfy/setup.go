package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"github.com/charmbracelet/x/term"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/proton"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpn"
)

func protonClient() *proton.Client {
	p, _ := proton.SessionPath()
	return proton.New(p)
}

// checkFolder makes sure the music folder can be created and written, so a typo shows up
// here instead of after a long download.
func checkFolder(s string) error { return screen.CheckFolder(s) }

func one(ctx context.Context, f huh.Field) error {
	return runForm(ctx, huh.NewForm(huh.NewGroup(f)))
}

// errQuit is Ctrl-C in a form: leave ytm (quietly, like any interrupt).
var errQuit = fmt.Errorf("quit: %w", context.Canceled)

// formKeys are the form keys: Esc goes back a screen, Ctrl-C quits. The menus are short,
// so typing to filter them is off (and its "/ filter" hint gives way to "esc back").
func formKeys() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	// The form handles Quit before the field sees the key, so this binding never filters;
	// it is only the hint in the help line.
	km.Select.Filter = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	return km
}

// runForm runs a form with ytm's keys. Esc comes back as errBack (the caller returns to
// where it came from), Ctrl-C as errQuit.
func runForm(ctx context.Context, f *huh.Form) error {
	var esc atomic.Bool
	err := f.WithKeyMap(formKeys()).WithTheme(ytmTheme).WithProgramOptions(tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			esc.Store(k.String() == "esc")
		}
		return msg
	})).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		if esc.Load() {
			return errBack
		}
		return errQuit
	}
	return err
}

func working(ctx context.Context, title string, f func(context.Context) error) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return f(ctx) // no terminal to draw a spinner on (piped, CI, a script)
	}
	var err error
	if serr := spinner.New().WithTheme(ytmSpinner).Title(title).Context(ctx).ActionWithErr(func(c context.Context) error {
		err = f(c)
		return nil
	}).Run(); serr != nil && err == nil {
		err = serr
	}
	return err
}

// loginFlow signs in to Proton (with 2FA and retries); vpnFlow has already asked which
// provider to use.
func loginFlow(ctx context.Context, pc *proton.Client, cfg *config.Config) error {
	cfg.NoVPN = false
	for {
		var user, pass string
		err := runForm(ctx, huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Proton email or username").Value(&user).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("required")
					}
					return nil
				}),
			huh.NewInput().Title("Password").Description("only sent to Proton, never saved").
				EchoMode(huh.EchoModePassword).Value(&pass),
		)))
		if err != nil {
			return err
		}
		err = working(ctx, "Signing in...", func(c context.Context) error {
			return pc.Login(c, strings.TrimSpace(user), pass)
		})
		if errors.Is(err, proton.ErrNeed2FA) {
			err = ask2FA(ctx, pc)
		}
		if err == nil {
			err = working(ctx, "Checking your VPN access...", func(c context.Context) error {
				_, e := pc.Identity(c)
				return e
			})
		}
		if err == nil {
			fmt.Println(ui.Mark(ui.OK("✓"), "signed in as "+ui.Sanitize(pc.Session.Username)))
			return nil
		}
		if errors.Is(err, errBack) || errors.Is(err, context.Canceled) {
			return err // Esc or Ctrl-C at the 2FA prompt
		}
		fmt.Println(ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
		var ae *proton.APIError
		if errors.As(err, &ae) && (ae.RateLimited() || ae.BadVersion() || ae.Code == 9001) {
			// retrying can't help and a rate limit gets longer with every attempt
			return errors.New("stopping here; fix the above, then try again in Settings (set YTM_DEBUG=1 to see Proton's raw reply)")
		}
		again := true
		if one(ctx, huh.NewConfirm().Title("Try again?").Value(&again)) != nil || !again {
			return errors.New("not signed in; try again in Settings")
		}
	}
}

func ask2FA(ctx context.Context, pc *proton.Client) error {
	for try := 0; try < 3; try++ {
		var code string
		if err := one(ctx, huh.NewInput().Title("2FA code").Description("from your authenticator app").Value(&code)); err != nil {
			return err
		}
		err := working(ctx, "Checking code...", func(c context.Context) error {
			return pc.Submit2FA(c, strings.TrimSpace(code))
		})
		if err == nil {
			return nil
		}
		fmt.Println(ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
	}
	return errors.New("2FA failed")
}

// runStatus connects the configured tunnels and shows where each one exits.
func runStatus(ctx context.Context, u *ui.UI) error {
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
		if resp, err := c.Get("https://api.ipify.org"); err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			ip = strings.TrimSpace(string(b))
		}
		u.Item(fmt.Sprintf("%-22s", x.Name), "exits at %s", ip)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}
