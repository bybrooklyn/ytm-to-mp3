package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// tidalLogin shows the terms, then signs in with Tidal's device-code flow: mfetchy shows a code
// and a link, the user approves it in any browser, and mfetchy waits.
func tidalLogin(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("signing in to Tidal needs a terminal")
	}
	var accept bool
	if err := runForm(ctx, huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Tidal (your own subscription)").Description(tidalWarning),
		huh.NewConfirm().Title("Sign in anyway?").Affirmative("Yes, I understand").Negative("No").Value(&accept),
	))); err != nil || !accept {
		return err
	}
	c := tidalClient()
	dc, err := c.StartDevice(ctx)
	if err != nil {
		return err
	}
	u.Head("Tidal sign-in")
	u.Styled("  Open  " + ui.Bold(dc.URL()))
	u.Styled("  Code  " + ui.Bold(dc.UserCode) + ui.Dim("  (the link already has it filled in)"))
	u.Hint("waiting for you to approve it in the browser · Ctrl-C to cancel")
	if err := c.WaitForLogin(ctx, dc); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := c.Verify(vctx); err != nil {
		return fmt.Errorf("signed in, but Tidal didn't accept the login: %w", err)
	}
	u.Good("Signed in to Tidal (country %s). Lossless needs a HiFi plan.", c.Country())
	return nil
}
