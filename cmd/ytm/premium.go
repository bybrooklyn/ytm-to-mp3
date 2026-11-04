package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// premiumWarning is shown when Premium is turned on and before every upgrade pass.
const premiumWarning = `YouTube Premium upgrade is EXPERIMENTAL.

  • It uses your Google account's cookies. YouTube can flag, limit or ban accounts it
    thinks are downloading in bulk (yt-dlp's own docs warn about this). Use a spare
    Premium account if you can.
  • It is slow on purpose: one song at a time, 5-10 s apart, on one VPN exit, at most
    300 songs a run. Normal downloads never use your account.
  • YouTube doesn't always offer the 256k streams, even to Premium accounts. Songs it
    doesn't offer them for are left as they are (never re-downloaded at the same quality).
  • It stops at the first bot check or login problem. If that happens, wait a day.`

// premiumSetup turns the Premium upgrade on (after the warning, with a cookies file) or off.
func premiumSetup(ctx context.Context, u *ui.UI) error {
	cfg := config.Load()
	if cfg.Premium {
		off := false
		if err := one(ctx, huh.NewConfirm().Title("Turn off the YouTube Premium upgrade?").
			Description("ytm deletes its copy of your cookies.").Affirmative("Turn off").Negative("Keep").Value(&off)); err != nil || !off {
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
	err := huh.NewForm(
		huh.NewGroup(huh.NewNote().Title("YouTube Premium upgrade").Description(premiumWarning),
			huh.NewConfirm().Title("Turn it on anyway?").Affirmative("Yes, I understand").Negative("No").Value(&accept)),
		huh.NewGroup(huh.NewSelect[string]().Title("Where should ytm get your YouTube sign-in from?").
			Description("Sign in to music.youtube.com in that browser first (with the Premium account).\nImporting copies its current session: YouTube rotates those cookies while the browser\nhas YouTube open, so you may have to import again now and then. A cookies.txt exported\nfrom a private window you then close lasts longer (yt-dlp's advice).").
			Options(sources...).Value(&source)).
			WithHideFunc(func() bool { return !accept }),
		huh.NewGroup(huh.NewInput().Title("Your YouTube cookies file (cookies.txt)").
			Description("Open a private/incognito window, sign in to YouTube Music, export the youtube.com\ncookies with a \"cookies.txt\" browser extension, then CLOSE the private window.").
			Value(&path).Validate(func(s string) error { return checkCookies(config.ExpandPath(strings.Trim(s, `"' `))) })).
			WithHideFunc(func() bool { return !accept || source != "file" }),
	).RunWithContext(ctx)
	if err != nil || !accept {
		return err
	}
	dst := config.PremiumCookiesPath()
	if source == "file" {
		b, err := os.ReadFile(config.ExpandPath(strings.Trim(path, `"' `)))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return err
		}
	} else {
		var cs []browsercookies.Cookie
		for _, b := range browsers {
			if b.ID == source {
				fmt.Printf("  Reading %s's cookies (your keyring may ask for permission)...\n", b.Name)
				if cs, err = b.Import(); err != nil {
					return err
				}
			}
		}
		if err := browsercookies.WriteNetscape(dst, cs); err != nil {
			return err
		}
		fmt.Printf("  %s✓%s %d YouTube cookies imported\n", cGreen, cReset, len(cs))
	}
	cfg.Premium = true
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("  %s✓%s Premium upgrade on. Run it from the menu or with: ytm --premium-upgrade\n", cGreen, cReset)
	return nil
}

// checkCookies accepts a Netscape cookies.txt with YouTube cookies in it.
func checkCookies(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return errors.New("can't open that file")
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	yt := false
	for sc.Scan() {
		if l := sc.Text(); strings.Contains(l, ".youtube.com\t") {
			yt = true
			break
		}
	}
	if !yt {
		return errors.New("no youtube.com cookies in it (it should be a Netscape cookies.txt export)")
	}
	return nil
}

// premiumUpgrade re-downloads already-downloaded songs at YouTube Premium quality, slowly
// and on one exit (see premiumWarning), replacing each file in place. auto: it follows a
// download (the user turned it on in settings and already saw the warning), so it just
// says what it's doing.
func premiumUpgrade(ctx context.Context, u *ui.UI, o opts, auto bool) error {
	cfg := config.Load()
	cookies := config.PremiumCookiesPath()
	if _, err := os.Stat(cookies); err != nil || !cfg.Premium {
		return errors.New("the YouTube Premium upgrade is off: turn it on in settings (ytm setup) first")
	}
	if auto {
		u.Printf("\nYouTube Premium upgrade (experimental, slow; turn off in settings, Ctrl-C to stop)...")
	} else {
		u.Printf("%s\n", premiumWarning)
		if !o.yes && !u.Confirm("Start?", true) {
			return nil
		}
	}
	if _, err := os.Stat(filepath.Join(o.out, ".ytm", "state.db")); err != nil {
		return fmt.Errorf("nothing downloaded in %s yet", o.out)
	}
	st, err := store.Open(filepath.Join(o.out, ".ytm"))
	if err != nil {
		return err
	}
	defer st.Close()
	limit := o.premiumLimit
	if limit <= 0 {
		limit = 300
	}
	todo, err := st.PremiumTodo(limit)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		u.Printf("Every song is already upgraded (or has no Premium stream).")
		return nil
	}
	logf := func(f string, a ...any) { u.Printf(f, a...) }
	tools, err := deps.Ensure(ctx, logf)
	if err != nil {
		return err
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, logf)
	if err != nil {
		return err
	}
	o.conns = 1 // one account, one IP: many IPs at once is what looks suspicious
	xs, err := buildExits(ctx, u, o)
	if err != nil {
		return err
	}
	defer xs.Close()
	proxy := ""
	if len(xs.Exits) > 0 {
		proxy = xs.Exits[0].Proxy
	}
	tmp, err := store.NewTempDir(o.out)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cache, _ := config.CacheDir()

	upgraded, none, failed, missesInARow := 0, 0, 0, 0
songs:
	for i, t := range todo {
		if ctx.Err() != nil {
			break
		}
		if i > 0 { // 5-10 s between songs
			select {
			case <-time.After(5*time.Second + time.Duration(rand.Int63n(int64(5*time.Second)))):
			case <-ctx.Done():
			}
		}
		u.Status("Premium upgrade %d/%d: %s", i+1, len(todo), t.Title)
		if _, err := os.Stat(t.Path); err != nil {
			continue // moved or deleted since
		}
		src, err := engine.PremiumDownload(ctx, tools, proxy, cookies, filepath.Join(cache, "yt-dlp"), o.format, t.ID, tmp)
		switch {
		case errors.Is(err, engine.ErrAccountBlocked):
			u.ClearStatus()
			u.Printf("Stopped: %v. Wait a day before trying again (and re-export the cookies if it persists).", err)
			return nil
		case errors.Is(err, engine.ErrNoPremiumFormat):
			none++
			_ = st.SetPremium(t.ID, -1)
			if missesInARow++; missesInARow >= 5 {
				u.ClearStatus()
				u.Printf("Stopped: 5 songs in a row had no Premium stream, so the account probably isn't getting them right now.")
				break songs
			}
			continue
		case err != nil:
			failed++
			u.Line(red+"✗"+reset, t.Title+"  ("+err.Error()+")")
			continue
		}
		missesInARow = 0
		err = tag.ReplaceAudio(ctx, ffmpeg, ffprobe, src, t.Path)
		_ = os.Remove(src)
		if err != nil {
			failed++
			u.Line(red+"✗"+reset, t.Title+"  ("+err.Error()+")")
			continue
		}
		upgraded++
		_ = st.SetPremium(t.ID, 1)
		u.Line(green+"✓"+reset, t.Title+"  (Premium)")
	}
	u.ClearStatus()
	u.Printf("\n%d upgraded, %d have no Premium stream, %d failed.", upgraded, none, failed)
	if ctx.Err() != nil {
		return context.Canceled
	}
	return nil
}

const (
	red   = "\x1b[31m"
	green = "\x1b[32m"
	reset = "\x1b[0m"
)
