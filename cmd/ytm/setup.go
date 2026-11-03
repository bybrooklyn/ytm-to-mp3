package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

func protonClient() *proton.Client {
	p, _ := proton.SessionPath()
	return proton.New(p)
}

// checkFolder makes sure the music folder can be created and written, so a typo shows up
// here instead of after a long download.
func checkFolder(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	dir := config.ExpandPath(s)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't create that folder: %v", err)
	}
	f, err := os.CreateTemp(dir, ".ytm-write-test-*")
	if err != nil {
		return fmt.Errorf("can't write there: %v", err)
	}
	f.Close()
	return os.Remove(f.Name())
}

func one(ctx context.Context, f huh.Field) error {
	return huh.NewForm(huh.NewGroup(f)).RunWithContext(ctx)
}

func working(ctx context.Context, title string, f func(context.Context) error) error {
	var err error
	if serr := spinner.New().Title(title).Context(ctx).ActionWithErr(func(c context.Context) error {
		err = f(c)
		return nil
	}).Run(); serr != nil && err == nil {
		err = serr
	}
	return err
}

// runSetup is the one-time, re-runnable wizard.
func runSetup(ctx context.Context, u *ui.UI, accountOnly bool) error {
	if !u.Interactive() {
		return errors.New("setup needs a terminal")
	}
	cfg := config.Load()
	pc := protonClient()

	fmt.Print("\n  ytm setup\n\n")
	if err := signIn(ctx, pc, &cfg); err != nil {
		return err
	}
	if accountOnly {
		return cfg.Save()
	}

	out, format, conns := cfg.Out, cfg.Format, cfg.Connections
	if out == "" {
		out = config.DefaultOut()
	}
	if format == "" {
		format = "mp3"
	}
	if conns == 0 {
		conns = 3
	}
	opts := []huh.Field{
		huh.NewInput().Title("Where should music go?").Value(&out).Validate(checkFolder),
		huh.NewSelect[string]().Title("Format").Options(huh.NewOptions(engine.Formats...)...).Value(&format),
	}
	vpnOn := pc.SignedIn() && !cfg.NoVPN
	if vpnOn {
		opts = append(opts, huh.NewSelect[int]().Title("VPN connections at once").
			Description("more = faster; each uses a different Proton server").
			Options(huh.NewOption("1", 1), huh.NewOption("2", 2), huh.NewOption("3 (recommended)", 3),
				huh.NewOption("4", 4), huh.NewOption("6", 6)).Value(&conns))
	}
	if err := huh.NewForm(huh.NewGroup(opts...)).RunWithContext(ctx); err != nil {
		return err
	}
	cfg.Out, cfg.Format, cfg.Connections = config.ExpandPath(out), format, conns
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("couldn't save your settings to %s: %w", config.Path(), err)
	}

	fmt.Printf("\n  ✓ saved to %s\n    music goes to %s\n    now run:  ytm \"<playlist-or-video-link>\"   or just   ytm   (for your Takeout)\n\n", config.Path(), cfg.Out)
	if pc.SignedIn() && !cfg.NoVPN {
		test := true
		if one(ctx, huh.NewConfirm().Title("Test the VPN now?").Value(&test)) == nil && test {
			return runStatus(ctx, u)
		}
	}
	return nil
}

func signIn(ctx context.Context, pc *proton.Client, cfg *config.Config) error {
	if pc.SignedIn() {
		keep := true
		if err := one(ctx, huh.NewConfirm().Title("Signed in to Proton as "+pc.Session.Username+".").
			Affirmative("Keep").Negative("Change").Value(&keep)); err != nil {
			return err
		}
		if keep {
			cfg.NoVPN = false
			return nil
		}
	}
	useVPN := true
	if err := one(ctx, huh.NewConfirm().Title("Download through Proton VPN?").
		Description("Uses several servers at once so YouTube can't throttle you. Needs a Proton VPN account.").
		Value(&useVPN)); err != nil {
		return err
	}
	if !useVPN {
		cfg.NoVPN = true
		pc.Logout()
		return nil
	}
	cfg.NoVPN = false
	for {
		var user, pass string
		err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Proton email or username").Value(&user).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("required")
					}
					return nil
				}),
			huh.NewInput().Title("Password").Description("only sent to Proton, never saved").
				EchoMode(huh.EchoModePassword).Value(&pass),
		)).RunWithContext(ctx)
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
			fmt.Println("  ✓ signed in as", pc.Session.Username)
			return nil
		}
		fmt.Println("  ✗", err)
		var ae *proton.APIError
		if errors.As(err, &ae) && (ae.RateLimited() || ae.BadVersion() || ae.Code == 9001) {
			// retrying can't help and a rate limit gets longer with every attempt
			return errors.New("stopping here; fix the above, then run ytm setup again (set YTM_DEBUG=1 to see Proton's raw reply)")
		}
		again := true
		if one(ctx, huh.NewConfirm().Title("Try again?").Value(&again)) != nil || !again {
			return errors.New("not signed in; run ytm setup to try again")
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
		fmt.Println("  ✗", err)
	}
	return errors.New("2FA failed")
}

// runStatus connects the configured tunnels and shows where each one exits.
func runStatus(ctx context.Context, u *ui.UI) error {
	cfg := config.Load()
	pc := protonClient()
	u.Printf("Settings (%s):", config.Path())
	u.Printf("  music folder: %s", config.ExpandPath(orDefault(cfg.Out, config.DefaultOut())))
	u.Printf("  format:       %s", orDefault(cfg.Format, "mp3"))
	u.Printf("  VPN exits:    %d", orInt(cfg.Connections, 3))
	if !pc.SignedIn() {
		u.Printf("VPN: not signed in (run: ytm setup)")
		return nil
	}
	if cfg.NoVPN {
		u.Printf("VPN: signed in as %s but turned off", pc.Session.Username)
		return nil
	}
	u.Printf("VPN: signed in as %s; connecting...", pc.Session.Username)
	pool, err := vpn.Open(ctx, pc, orInt(cfg.Connections, 3), func(f string, a ...any) { u.Printf(f, a...) })
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
		u.Printf("  %-22s exits at %s", x.Name, ip)
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
