package core

import (
	"context"
	"os"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// Seams for tests: which browsers there are, and reading one's cookies.
var (
	detectBrowsers = browsercookies.Detect
	importCookies  = func(b browsercookies.Browser) ([]browsercookies.Cookie, error) { return b.Import() }
)

// PremiumSetup turns the Premium upgrade on (after the warning, with a cookies file or a
// browser's sign-in) or off.
func PremiumSetup(ctx context.Context, u *ui.UI, p Prompter) error {
	cfg := config.Load()
	if cfg.Premium {
		off, err := p.Confirm(ctx, Question{Title: "Turn off the YouTube Premium upgrade?",
			Detail: "mfetchy deletes its copy of your cookies.", Yes: "Turn off", No: "Keep"})
		if err != nil || !off {
			return err
		}
		_ = os.Remove(config.PremiumCookiesPath())
		cfg.Premium = false
		return cfg.Save()
	}
	accept, err := p.Confirm(ctx, Question{Title: "Turn it on anyway?",
		Detail: "YouTube Premium upgrade\n\n" + premiumWarning, Yes: "Yes, I understand", No: "No"})
	if err != nil || !accept {
		return err
	}
	browsers := detectBrowsers()
	var sources []Option
	for _, b := range browsers {
		label := "Import from " + b.Name
		if b.Note != "" {
			label += " (" + b.Note + ")"
		}
		sources = append(sources, Option{Value: b.ID, Label: label})
	}
	sources = append(sources, Option{Value: "file", Label: "Use a cookies.txt file"})
	source, err := p.Select(ctx, "Where should mfetchy get your YouTube sign-in from?",
		"Sign in to music.youtube.com in that browser first (with the Premium account).\nImporting copies its current session: YouTube rotates those cookies while the browser\nhas YouTube open, so you may have to import again now and then. A cookies.txt exported\nfrom a private window you then close lasts longer (yt-dlp's advice).",
		sources, sources[0].Value)
	if err != nil {
		return err
	}
	dst := config.PremiumCookiesPath()
	if source == "file" {
		path, err := p.File(ctx, FileRequest{
			Title: "Your YouTube cookies file (cookies.txt)",
			Detail: "Open a private/incognito window, sign in to YouTube Music, export the youtube.com\n" +
				"cookies with a \"cookies.txt\" browser extension, then CLOSE the private window.",
			File: true, Exts: []string{".txt"},
			Validate: func(s string) error { return checkCookies(config.ExpandPath(strings.Trim(s, `"' `))) },
		})
		if err != nil {
			return err
		}
		b, err := os.ReadFile(config.ExpandPath(strings.Trim(path, `"' `)))
		if err != nil {
			return err
		}
		if err := config.WriteSecret(dst, b); err != nil {
			return err
		}
	} else {
		var cs []browsercookies.Cookie
		for _, b := range browsers {
			if b.ID != source {
				continue
			}
			u.Note("Reading %s's cookies · your keyring may ask for permission", ui.Sanitize(b.Name))
			err := p.Working(ctx, "Reading "+b.Name+"'s cookies...", func(context.Context) (e error) {
				cs, e = importCookies(b)
				return
			})
			if err != nil {
				return err
			}
		}
		if err := browsercookies.WriteNetscape(dst, cs); err != nil {
			return err
		}
		u.Good("%d YouTube cookies imported", len(cs))
	}
	cfg.Premium = true
	if err := cfg.Save(); err != nil {
		return err
	}
	u.Good("Premium upgrade on · run it from the menu or with: mfy --premium-upgrade")
	return nil
}
