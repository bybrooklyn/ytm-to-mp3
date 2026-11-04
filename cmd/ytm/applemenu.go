package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// appleMenu is the home menu's way into Apple Music: sign in first, then download links
// or find the YouTube library there.
func appleMenu(ctx context.Context, u *ui.UI, o opts) error {
	if !appleSignedIn() {
		fmt.Printf("\n  %sApple Music%s gives 256 kbps AAC with your own Apple Music subscription.\n", cAccent, cReset)
		fmt.Printf("  %sSign in at music.apple.com in your browser (and play a song), then continue.%s\n\n", cDim, cReset)
		in := true
		if err := one(ctx, huh.NewConfirm().Title("Sign in to Apple Music now?").
			Affirmative("Sign in").Negative("Back").Value(&in)); err != nil {
			return err
		}
		if !in {
			return errBack
		}
		if err := appleLogin(ctx, u); err != nil {
			return err
		}
	}
	cfg := config.Load()
	var choice string
	if err := one(ctx, huh.NewSelect[string]().
		Title("Apple Music").
		Description(fmt.Sprintf("Signed in · store %s · songs go to %s as m4a (AAC 256)",
			strings.ToUpper(orDefault(cfg.AppleStorefront, "us")), tilde(o.out))).
		Options(
			huh.NewOption("Find my YouTube library on Apple Music", "match"),
			huh.NewOption("Download an Apple Music link (album, song or playlist)", "link"),
			huh.NewOption("Check the sign-in", "status"),
			huh.NewOption("Sign out", "logout"),
			huh.NewOption("Back", "back"),
		).Value(&choice)); err != nil {
		return err
	}
	switch choice {
	case "match":
		src, err := chooseExport(ctx, u)
		if err != nil {
			return err
		}
		return appleMatch(ctx, u, o, src.Paths)
	case "link":
		return appleLinkFlow(ctx, u, o)
	case "status":
		return appleStatus(ctx, u)
	case "logout":
		return appleLogout(u)
	}
	return errBack
}

// appleLinkFlow asks for Apple Music links and downloads them.
func appleLinkFlow(ctx context.Context, u *ui.UI, o opts) error {
	var in string
	var links []string
	if err := one(ctx, huh.NewInput().Title("Paste an Apple Music link").
		Description("An album, song or playlist from music.apple.com. Several links separated by spaces work too.").
		Value(&in).Validate(func(s string) error {
		links = links[:0]
		for _, f := range strings.Fields(s) {
			f = strings.Trim(f, `"'`)
			if !apple.IsURL(f) {
				return fmt.Errorf("%q isn't an Apple Music album, song or playlist link", f)
			}
			links = append(links, f)
		}
		if len(links) == 0 {
			return errors.New("paste a link")
		}
		return nil
	})); err != nil {
		return err
	}
	return appleDownloadLinks(ctx, u, o, links)
}
