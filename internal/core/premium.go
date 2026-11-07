package core

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

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
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
		return errors.New("the YouTube Premium upgrade is off: turn it on in Settings first")
	}
	if auto {
		u.Head("YouTube Premium upgrade · experimental, slow; turn off in settings, Ctrl-C to stop")
	} else {
		warnBlock(u, premiumWarning)
		if !o.yes {
			if ok, _ := o.prompt.Confirm(ctx, Question{Title: "Start?", Detail: premiumWarning, Default: true}); !ok {
				return nil
			}
		}
	}
	if _, err := os.Stat(filepath.Join(appname.StateDir(o.out), "state.db")); err != nil {
		return fmt.Errorf("nothing downloaded in %s yet", o.out)
	}
	st, err := store.Open(appname.StateDir(o.out))
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
		u.Good("Every song is already upgraded · or has no Premium stream")
		return nil
	}
	logf := u.Logf
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
			u.Bad("Stopped: %v", err)
			u.Hint("Wait a day before trying again (and re-export the cookies if it persists).")
			return nil
		case errors.Is(err, engine.ErrNoPremiumFormat):
			none++
			_ = st.SetPremium(t.ID, -1)
			if missesInARow++; missesInARow >= 5 {
				u.ClearStatus()
				u.Bad("Stopped: 5 songs in a row had no Premium stream · the account probably isn't getting them right now")
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
	line := fmt.Sprintf("\n  %s  %s %s upgraded", ui.Bold("Premium"), ui.OK("✓"), ui.Bold(fmt.Sprint(upgraded)))
	if none > 0 {
		line += "  " + ui.Dim(fmt.Sprintf("%d have no Premium stream", none))
	}
	if failed > 0 {
		line += "  " + ui.Fail("✗") + " " + ui.Red(fmt.Sprint(failed)) + " failed"
	}
	u.Styled(line)
	if ctx.Err() != nil {
		return context.Canceled
	}
	return nil
}

var (
	red   = ui.CRed
	green = ui.CGreen
	reset = ui.CReset
)
