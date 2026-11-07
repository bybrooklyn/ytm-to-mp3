package core

import (
	"context"
	"fmt"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// TidalLogin shows the terms, then signs in with Tidal's device-code flow: mfetchy shows a
// code and a link, the person approves it in any browser, and mfetchy waits.
func TidalLogin(ctx context.Context, u *ui.UI, p Prompter) error {
	accept, err := p.Confirm(ctx, Question{Title: "Sign in anyway?",
		Detail: "Tidal (your own subscription)\n\n" + tidalWarning, Yes: "Yes, I understand", No: "No"})
	if err != nil || !accept {
		return err
	}
	c := tidalClient()
	dc, err := c.StartDevice(ctx)
	if err != nil {
		return err
	}
	err = p.Wait(ctx, Info{
		Title: "Tidal sign-in", Link: dc.URL(), LinkLabel: "Open Tidal's sign-in page", Code: dc.UserCode,
		Detail:  "the link already has it filled in",
		Waiting: "waiting for you to approve it in the browser",
	}, func(ctx context.Context) error { return c.WaitForLogin(ctx, dc) })
	if err != nil {
		return err // includes tidal.ErrExpired and tidal.ErrDenied, which say what happened
	}
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := c.Verify(vctx); err != nil {
		return fmt.Errorf("signed in, but Tidal didn't accept the login: %w", err)
	}
	u.Good("Signed in to Tidal (country %s). Lossless needs a HiFi plan.", c.Country())
	return nil
}
