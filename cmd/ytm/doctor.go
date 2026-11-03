package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// runDoctor checks everything ytm depends on and says what to do about each problem.
func runDoctor(ctx context.Context, u *ui.UI, o opts) error {
	problems := 0
	ok := func(f string, a ...any) { u.Printf("  ✓ "+f, a...) }
	bad := func(fix string, f string, a ...any) {
		problems++
		u.Printf("  ✗ "+f, a...)
		u.Printf("      → %s", fix)
	}
	note := func(f string, a ...any) { u.Printf("  · "+f, a...) }

	u.Printf("ytm %s on %s/%s", version, runtime.GOOS, runtime.GOARCH)

	u.Printf("\nSettings")
	if _, err := config.LoadChecked(); err != nil {
		bad("fix or delete that file, then run: ytm setup", "%v", err)
	} else if config.Exists() {
		ok("%s", config.Path())
	} else {
		note("no settings saved yet (using defaults). Run: ytm setup")
	}
	if err := checkFolder(o.out); err != nil {
		bad("pick another folder with: ytm setup", "music folder %s: %v", o.out, err)
	} else {
		ok("music folder %s is writable", o.out)
	}

	u.Printf("\nTools (ytm downloads any that are missing on first use)")
	for _, t := range []string{"yt-dlp", "ffmpeg", "ffprobe", "deno", "node", "bun"} {
		if p, err := exec.LookPath(t); err == nil {
			ok("%s: %s", t, p)
		} else {
			note("%s: not on PATH", t)
		}
	}

	u.Printf("\nNetwork")
	for _, h := range []string{"https://github.com", "https://www.youtube.com", "https://vpn-api.proton.me"} {
		if err := reach(ctx, h); err != nil {
			bad("check your internet connection, proxy or firewall", "%s: %v", h, err)
		} else {
			ok("%s reachable", h)
		}
	}

	u.Printf("\nProton VPN")
	pc := protonClient()
	switch {
	case !pc.SignedIn():
		note("not signed in (optional). Run: ytm setup")
	case config.Load().NoVPN:
		note("signed in as %s, VPN turned off in settings", pc.Session.Username)
	default:
		ok("signed in as %s (test the tunnels with: ytm status)", pc.Session.Username)
	}

	if problems == 0 {
		u.Printf("\nAll good.")
		return nil
	}
	u.Printf("\n%d problem(s) found.", problems)
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
