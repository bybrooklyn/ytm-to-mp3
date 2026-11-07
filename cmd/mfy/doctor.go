package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// runDoctor checks everything ytm depends on and says what to do about each problem.
func runDoctor(ctx context.Context, u *ui.UI, o opts) error {
	problems := 0
	ok := func(f string, a ...any) { u.Good(f, a...) }
	bad := func(fix string, f string, a ...any) {
		problems++
		u.Bad(f, a...)
		u.Hint("→ %s", fix)
	}
	note := func(f string, a ...any) { u.Note(f, a...) }

	u.Head("ytm %s · %s/%s", version, runtime.GOOS, runtime.GOARCH)

	u.Head("Settings")
	if _, err := config.LoadChecked(); err != nil {
		bad("fix or delete that file, then check Settings", "%v", err)
	} else if config.Exists() {
		ok("%s", config.Path())
	} else {
		note("no settings saved yet (using defaults): see Settings")
	}
	if err := checkFolder(o.out); err != nil {
		bad("pick another folder in Settings", "music folder %s: %v", o.out, err)
	} else {
		ok("music folder %s is writable", o.out)
	}

	u.Head("Tools · ytm downloads any that are missing on first use")
	for _, t := range []struct{ name, why string }{
		{"yt-dlp", "fallback for videos ytm's own client can't open, and Premium downloads"},
		{"ffmpeg", "remuxes downloads and writes tags and cover art; makes mp3/flac"},
		{"ffprobe", "reads the tags and cover art of files you already have"},
		{"deno", "optional: helps yt-dlp with YouTube's JavaScript challenges"},
		{"node", "optional: same, if deno is missing"},
		{"bun", "optional: same, if deno is missing"},
	} {
		if p, err := exec.LookPath(t.name); err == nil {
			ok("%s: %s · %s", t.name, p, t.why)
		} else {
			note("%s: not on PATH · %s", t.name, t.why)
		}
	}

	cfg := config.Load()
	v := currentVPN(cfg)
	u.Head("Network")
	hosts := []string{"https://github.com", "https://www.youtube.com"}
	if v.SignedIn {
		hosts = append(hosts, providerAPIHost(providerID(cfg)))
	}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if err := reach(ctx, h); err != nil {
			bad("check your internet connection, proxy or firewall", "%s: %v", h, err)
		} else {
			ok("%s reachable", h)
		}
	}

	u.Head("VPN and privacy")
	for _, l := range privacyLines(cfg, v, o) {
		switch l.kind {
		case lineGood:
			ok("%s", l.text)
		case lineWarn:
			bad(l.fix, "%s", l.text)
		default:
			note("%s", l.text)
		}
	}

	u.Head("Apple Music")
	if !appleSignedIn() {
		note("not signed in (optional): Settings > Apple Music, for 256 kbps AAC")
	} else {
		cfg := config.Load()
		_ = apple.SetProxy("")
		c, err := apple.New(config.AppleToken(), config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
		if err == nil {
			_, err = c.Verify(ctx)
		}
		if err != nil {
			bad("sign in again at music.apple.com in your browser, then Settings > Apple Music", "the saved login doesn't work: %v", err)
		} else {
			ok("signed in (store %s), and Apple accepts the login", strings.ToUpper(orDefault(cfg.AppleStorefront, "us")))
		}
	}

	u.Head("Tidal")
	tidalDoctor(ctx, bad, ok, note)

	if problems == 0 {
		u.Styled("\n" + ui.Mark(ui.OK("✓"), ui.Bold("All good")))
		return nil
	}
	u.Styled("\n" + ui.Mark(ui.Fail("✗"), ui.Bold(fmt.Sprintf("%d problem(s) found", problems))))
	return fmt.Errorf("%d problem(s) found", problems)
}

func reach(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
