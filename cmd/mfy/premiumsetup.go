package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// premiumSetup turns the Premium upgrade on (after the warning, with a cookies file) or off.
func premiumSetup(ctx context.Context, u *ui.UI) error {
	cfg := config.Load()
	if cfg.Premium {
		off := false
		if err := one(ctx, huh.NewConfirm().Title("Turn off the YouTube Premium upgrade?").
			Description("mfetchy deletes its copy of your cookies.").Affirmative("Turn off").Negative("Keep").Value(&off)); err != nil || !off {
			return err
		}
		_ = os.Remove(config.PremiumCookiesPath())
		cfg.Premium = false
		return cfg.Save()
	}
	browsers := browsercookies.Detect()
	var sources []huh.Option[string]
	for _, b := range browsers {
		label := "Import from " + b.Name
		if b.Note != "" {
			label += " (" + b.Note + ")"
		}
		sources = append(sources, huh.NewOption(label, b.ID))
	}
	sources = append(sources, huh.NewOption("Use a cookies.txt file", "file"))
	var accept bool
	source, path := sources[0].Value, ""
	err := runForm(ctx, huh.NewForm(
		huh.NewGroup(huh.NewNote().Title("YouTube Premium upgrade").Description(premiumWarning),
			huh.NewConfirm().Title("Turn it on anyway?").Affirmative("Yes, I understand").Negative("No").Value(&accept)),
		huh.NewGroup(huh.NewSelect[string]().Title("Where should mfetchy get your YouTube sign-in from?").
			Description("Sign in to music.youtube.com in that browser first (with the Premium account).\nImporting copies its current session: YouTube rotates those cookies while the browser\nhas YouTube open, so you may have to import again now and then. A cookies.txt exported\nfrom a private window you then close lasts longer (yt-dlp's advice).").
			Options(sources...).Value(&source)).
			WithHideFunc(func() bool { return !accept }),
		huh.NewGroup(huh.NewInput().Title("Your YouTube cookies file (cookies.txt)").
			Description("Open a private/incognito window, sign in to YouTube Music, export the youtube.com\ncookies with a \"cookies.txt\" browser extension, then CLOSE the private window.").
			Value(&path).Validate(func(s string) error { return checkCookies(config.ExpandPath(strings.Trim(s, `"' `))) })).
			WithHideFunc(func() bool { return !accept || source != "file" }),
	))
	if err != nil || !accept {
		return err
	}
	dst := config.PremiumCookiesPath()
	if source == "file" {
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
			if b.ID == source {
				fmt.Println(ui.Mark(ui.Dim("·"), "Reading "+ui.Sanitize(b.Name)+"'s cookies · your keyring may ask for permission"))
				if cs, err = b.Import(); err != nil {
					return err
				}
			}
		}
		if err := browsercookies.WriteNetscape(dst, cs); err != nil {
			return err
		}
		fmt.Println(ui.Mark(ui.OK("✓"), fmt.Sprintf("%d YouTube cookies imported", len(cs))))
	}
	cfg.Premium = true
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println(ui.Mark(ui.OK("✓"), "Premium upgrade on · run it from the menu or with: mfy --premium-upgrade"))
	return nil
}
