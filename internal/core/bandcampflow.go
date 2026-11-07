package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// bandcampCollection reads the purchases of the account a browser is signed in to. It is a
// variable so tests need no browser.
var bandcampCollection = func(ctx context.Context, b browsercookies.Browser) (profile string, items []bandcamp.Item, err error) {
	cs, err := b.BandcampCookies()
	if err != nil {
		return "", nil, fmt.Errorf("%s isn't signed in to Bandcamp: open bandcamp.com there, sign in, and try again", b.Name)
	}
	c, err := bandcamp.New(ctx, &http.Client{Timeout: 5 * time.Minute}, cs)
	if err != nil {
		return "", nil, fmt.Errorf("Bandcamp didn't accept that sign-in: %w", err)
	}
	items, err = c.Collection(ctx)
	return c.FanURL, items, err
}

// BandcampFlow downloads everything the signed-in Bandcamp account has bought, as the
// original files: pick the browser if none is set up, read the purchases, show how many,
// download on a yes.
func BandcampFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	op := o.internal(p)
	cfg := config.Load()
	var b browsercookies.Browser
	if cfg.BandcampBrowser != "" {
		for _, d := range detectBrowsers() {
			if d.ID == cfg.BandcampBrowser {
				b = d
			}
		}
	}
	if b.ID == "" {
		var err error
		if b, err = pickBandcampBrowser(ctx, p); err != nil {
			return err
		}
	}
	var profile string
	var items []bandcamp.Item
	if err := p.Working(ctx, "Reading your Bandcamp purchases from "+b.Name+"...", func(ctx context.Context) (e error) {
		profile, items, e = bandcampCollection(ctx, b)
		return
	}); err != nil {
		return err
	}
	if len(items) == 0 {
		u.Note("%s has no Bandcamp purchases on this account", b.Name)
		return errors.New("no Bandcamp purchases found on that account")
	}
	if profile == "" {
		return errors.New("Bandcamp didn't say whose purchases these are; sign in again and retry")
	}
	u.Good("%s", count(len(items), "Bandcamp purchase")+" found")
	ok, err := p.Confirm(ctx, Question{
		Title:  "Download " + count(len(items), "purchase") + "?",
		Detail: fmt.Sprintf("Everything you bought on Bandcamp, as the original files (FLAC when offered).\nSaving to %s. Songs already in the library are skipped.", tilde(op.out)),
		Yes:    "Download", No: "Back", Default: true})
	if err != nil {
		return err
	}
	if !ok {
		return ErrBack
	}
	_, err = runDownload(ctx, u, op, nil, []string{profile})
	return err
}
