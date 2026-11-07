package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/lyrics"
	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/verify"
)

func appleLogout(u *ui.UI) error {
	if err := config.SaveAppleToken(""); err != nil {
		return err
	}
	if err := config.SaveAppleCookies(""); err != nil {
		return err
	}
	u.Good("Forgot your Apple Music login.")
	return nil
}

// appleLogin finds a live Apple Music session in a browser (or takes the token
// pasted), checks it with Apple and saves it with the account's country.
func appleLogin(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("signing in to Apple Music needs a terminal")
	}
	u.Head("Apple Music login")

	var token, jar, store string
	// Read it straight from a browser you're already signed in to. Every
	// installed browser is tried and the session is validated against Apple, so
	// a live login is chosen over a stale one in another browser. The whole
	// cookie jar is kept, not just the token: the web-playback endpoint needs
	// the iTunes store-session cookies too.
	if tok, j, sf, browser, ok := detectLiveAppleLogin(ctx, u); ok {
		if u.Confirm(fmt.Sprintf("Found a live Apple Music login in %s. Use it?", browser), true) {
			token, jar, store = tok, j, sf
		}
	} else {
		u.Warn("No live Apple Music session found in a browser.")
	}
	if token == "" {
		u.Hint("Sign in at https://music.apple.com in your browser (and play a song so the")
		u.Hint("session is active), then try again — or paste the cookie named")
		u.Hint("\"media-user-token\" here (DevTools > Application > Cookies > music.apple.com).")
		token = strings.TrimSpace(u.Ask("media-user-token (or Enter to cancel): ", ""))
		if token == "" {
			return errors.New("no login")
		}
		_ = apple.SetProxy("")
		c, err := apple.New(token, "", "us", "")
		if err != nil {
			return err
		}
		if store, err = c.Verify(ctx); err != nil {
			return fmt.Errorf("that token doesn't work: %w", err)
		}
	}
	if err := config.SaveAppleToken(token); err != nil {
		return err
	}
	if err := config.SaveAppleCookies(jar); err != nil {
		return err
	}
	cfg := config.Load()
	if store != "" {
		cfg.AppleStorefront = store
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	u.Good("Signed in to Apple Music · store %s", strings.ToUpper(orDefault(cfg.AppleStorefront, "us")))
	return nil
}

// appleStatus reports whether a saved login still works, checking it against
// Apple.
func appleStatus(ctx context.Context, u *ui.UI) error {
	token := config.AppleToken()
	if token == "" {
		u.Warn("No Apple Music login saved · sign in from the Apple Music menu")
		return nil
	}
	cfg := config.Load()
	u.Note("Apple Music login saved · store %s; checking...", strings.ToUpper(orDefault(cfg.AppleStorefront, "us")))
	_ = apple.SetProxy("")
	c, err := apple.New(token, config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
	if err != nil {
		return err
	}
	if _, err := c.Verify(ctx); err != nil {
		u.Bad("The login isn't working: %v", err)
		return nil
	}
	u.Good("The login works.")
	return nil
}

// detectLiveAppleLogin tries each installed browser's Apple Music cookies and
// returns the first session Apple still accepts (validated directly, no VPN), with
// the account's storefront.
func detectLiveAppleLogin(ctx context.Context, u *ui.UI) (token, jar, storefront, browser string, ok bool) {
	_ = apple.SetProxy("")
	for _, b := range browsercookies.Detect() {
		cs, err := b.AppleMusicCookies()
		if err != nil {
			continue
		}
		tok := browsercookies.AppleUserToken(cs)
		if tok == "" {
			continue
		}
		j := browsercookies.CookieHeader(cs)
		c, err := apple.New(tok, j, "us", "")
		if err != nil {
			continue
		}
		if sf, err := c.Verify(ctx); err == nil {
			return tok, j, sf, b.Name, true
		}
		u.Warn("%s has an Apple Music login, but it has expired", b.Name)
	}
	return "", "", "", "", false
}

// appleSignedIn reports whether an Apple Music login is saved (not whether it works).
func appleSignedIn() bool { return config.AppleToken() != "" }

// appleSession is a checked Apple login plus the VPN exits its traffic is spread over.
type appleSession struct {
	c  *apple.Client
	st *store.Store
	mu sync.Mutex
	// exits are the proxies to use ("" = direct, when the VPN is off). More join while
	// the run goes on.
	exits []string
	close func()
}

func (s *appleSession) exit(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exits[i%len(s.exits)]
}

func (s *appleSession) exitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.exits)
}

// openApple builds a logged-in client, brings the VPN up, checks the login with Apple
// (through the VPN) and opens the music folder's database.
func openApple(ctx context.Context, u *ui.UI, o opts) (*appleSession, error) {
	token := config.AppleToken()
	if token == "" {
		return nil, errors.New("no Apple Music login: sign in in Settings (Apple Music)")
	}
	cfg := config.Load()
	c, err := apple.New(token, config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
	if err != nil {
		return nil, err
	}
	xs, err := buildExits(ctx, u, o)
	if err != nil {
		return nil, err
	}
	s := &appleSession{c: c, close: xs.Close}
	for _, x := range xs.Exits {
		s.exits = append(s.exits, x.Proxy)
	}
	if len(s.exits) == 0 {
		s.exits = []string{""}
		u.Note("no VPN · Apple Music traffic will use your own address")
	}
	if xs.New != nil {
		go func() {
			for x := range xs.New {
				s.mu.Lock()
				if s.exits[0] == "" {
					s.exits = s.exits[:0]
				}
				s.exits = append(s.exits, x.Proxy)
				s.mu.Unlock()
			}
		}()
	}
	if err := apple.SetProxy(s.exits[0]); err != nil {
		s.close()
		return nil, err
	}
	sf, err := c.Verify(ctx)
	if err != nil {
		s.close()
		if errors.Is(err, apple.ErrLogin) {
			return nil, fmt.Errorf("%w\n(then sign in again in Settings: Apple Music)", err)
		}
		return nil, fmt.Errorf("can't reach Apple Music: %w", err)
	}
	if sf != "" && sf != cfg.AppleStorefront {
		cfg.AppleStorefront = sf
		_ = cfg.Save()
	}
	if s.st, err = store.Open(appname.StateDir(o.out)); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *appleSession) Close() {
	if s.st != nil {
		s.st.Close()
	}
	s.close()
}

// appleJob is one song to download: where it goes and, for a library song, the YouTube
// track it stands for.
type appleJob struct {
	t      apple.Track
	key    string // the song's id in a run's events ("" = the Apple id)
	dest   string
	ytID   string // the library song this is the Apple copy of ("" for links)
	ytPath string // that song's YouTube file ("" if not downloaded)
	// replace: the Apple copy takes the YouTube song's place. Its file (if any) is moved
	// to .mfetchy/replaced, and a library download won't fetch it from YouTube again.
	replace bool
}

// applePath is where an Apple song goes: the same Artist/Album/NN - Title scheme as
// YouTube downloads.
func applePath(root string, t apple.Track) string {
	artist := firstNonEmpty(t.AlbumArtist, t.Artist)
	parts := []string{names.Dirs(root, artist, t.Album)}
	prefix := ""
	if t.Album != "" {
		prefix = names.TrackPrefix(t.TrackNumber, t.TrackCount)
	}
	parts = append(parts, names.PartBetween(prefix, t.Title, "Unknown", ".m4a"))
	return filepath.Join(parts...)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var errAppleExists = errors.New("already downloaded")

// appleLive is a song being downloaded, for the live display.
type appleLive struct {
	label      string
	id         string       // the song's id in the run's events
	sent       atomic.Int64 // bytes last reported as a progress event
	got, total atomic.Int64
}

type appleLiveKey struct{}

// appleRun downloads jobs several at a time over the session's exits, retrying a
// song on another exit when the network fails, and records each in the database.
func appleRun(ctx context.Context, u *ui.UI, o opts, s *appleSession, list []appleJob) error {
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, u.Logf)
	if err != nil {
		return err
	}
	// Leave out what is already done, or that Apple doesn't offer this account.
	var todo []appleJob
	have, notOffered := 0, 0
	for _, j := range list {
		st := s.st.Apple(j.t.ID)
		switch {
		case st.Done && fileExists(st.Path):
			have++
		case st.Permanent:
			notOffered++
		default:
			todo = append(todo, j)
		}
	}
	if have > 0 || notOffered > 0 {
		u.Note("%d already downloaded from Apple Music · %d not offered to your account (skipped)", have, notOffered)
	}
	if len(todo) == 0 {
		u.Good("Nothing new to download.")
		return nil
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	live := map[*appleLive]bool{}
	var liveMu sync.Mutex
	var done, failed, unavailable, finished atomic.Int64
	var bytes atomic.Int64
	apple.SetProgress(func(ctx context.Context, n, total int64) {
		if l, _ := ctx.Value(appleLiveKey{}).(*appleLive); l != nil {
			l.got.Add(n)
			if total > 0 {
				l.total.Store(total)
			}
		}
		bytes.Add(n)
	})
	defer apple.SetProgress(nil)

	workers := min(len(todo), max(2, 2*s.exitCount()), 8)
	if o.jobs > 0 {
		workers = min(workers, o.jobs)
	}
	u.Head("Downloading %s from Apple Music · 256 kbps AAC, %d at a time", count(len(todo), "song"), workers)
	began := time.Now()
	stopDraw := make(chan struct{})
	drawn := make(chan struct{})
	go func() {
		defer close(drawn)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopDraw:
				return
			case <-tick.C:
			}
			liveMu.Lock()
			var ls []*appleLive
			for l := range live {
				ls = append(ls, l)
			}
			liveMu.Unlock()
			sort.Slice(ls, func(a, b int) bool { return ls[a].label < ls[b].label })
			for _, l := range ls {
				if got := l.got.Load(); got != l.sent.Swap(got) {
					o.watch(evProgress(l.id, l.label, "apple", got, l.total.Load()))
				}
			}
			u.Block(appleRows(ls, int(finished.Load()), len(todo), int(failed.Load()+unavailable.Load()),
				float64(bytes.Load())/time.Since(began).Seconds(), u.Width()))
		}
	}()

	var next atomic.Int64
	var transientInARow atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(todo) {
					return
				}
				j := todo[i]
				l := &appleLive{label: names.Clean(j.t.Artist + " - " + j.t.Title), id: firstNonEmpty(j.key, j.t.ID)}
				for _, e := range evStart(l.id, l.label, "apple") {
					o.watch(e)
				}
				liveMu.Lock()
				live[l] = true
				liveMu.Unlock()
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					if attempt > 0 {
						l.got.Store(0)
						select {
						case <-time.After(time.Duration(attempt) * 2 * time.Second):
						case <-ctx.Done():
						}
					}
					jctx := context.WithValue(apple.WithExit(ctx, s.exit(w+attempt)), appleLiveKey{}, l)
					err = appleSave(jctx, s, ffmpeg, ffprobe, o.out, j)
					if !apple.Transient(err) || ctx.Err() != nil {
						break
					}
				}
				liveMu.Lock()
				delete(live, l)
				liveMu.Unlock()
				finished.Add(1)
				switch {
				case err == nil || errors.Is(err, errAppleExists):
					transientInARow.Store(0)
					done.Add(1)
					u.Line(green+"✓"+reset, l.label)
					o.watch(evDone(l.id, l.label, "apple", s.st.Apple(j.t.ID).Path, "256 kbps AAC"))
				case errors.Is(err, apple.ErrLogin):
					cancel(err)
				case ctx.Err() != nil:
				case apple.Permanent(err):
					transientInARow.Store(0)
					unavailable.Add(1)
					_ = s.st.AppleFailed(j.t.ID, j.t.Title, j.t.Artist, err.Error(), true)
					u.Line(cYellow+"–"+reset, l.label+"  (not offered to your account)")
					o.watch(evFailed(l.id, l.label, "apple", "Apple Music doesn't offer it to your account", jobs.ClassUnavailable, false))
				default:
					failed.Add(1)
					_ = s.st.AppleFailed(j.t.ID, j.t.Title, j.t.Artist, err.Error(), false)
					u.Line(red+"✗"+reset, l.label+"  ("+err.Error()+")")
					o.watch(evFailed(l.id, l.label, "apple", "Apple Music download didn't finish", jobs.ClassNetwork, true))
					if apple.Transient(err) && transientInARow.Add(1) >= 8 {
						cancel(errors.New("8 songs in a row failed on the network: Apple may be blocking these exits; try again later"))
					}
				}
			}
		}()
	}
	wg.Wait()
	close(stopDraw)
	<-drawn
	u.ClearStatus()
	line := runSummary(int(done.Load()), int(failed.Load()), 0, 0, "Apple Music")
	if n := unavailable.Load(); n > 0 {
		line += "  " + ui.Warn("–") + " " + fmt.Sprint(n) + " not offered"
	}
	u.Styled(line + "  " + ui.Dim("· "+ui.Sanitize(tilde(o.out))+" (m4a / AAC 256)"))
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	if failed.Load() > 0 {
		u.Hint("Run the same command again to retry the failed ones.")
	}
	return ctx.Err()
}

// appleRows is the live block: a summary line and one bar per song in progress.
func appleRows(ls []*appleLive, finished, total, failed int, speed float64, width int) []string {
	head := fmt.Sprintf("  %s %s  %s%d/%d%s", ui.Accent("♪"), ui.Bold("Apple Music"), cBold, finished, total, cReset)
	if failed > 0 {
		head += "  " + ui.Fail("✗") + " " + fmt.Sprintf("%d not downloaded", failed)
	}
	if speed > 0 {
		head += "  " + ui.Cyan(engine.HumanBytes(int64(speed))+"/s")
	}
	rows := []string{head}
	for _, l := range ls {
		f := 0.0
		if t := l.total.Load(); t > 0 {
			f = min(1, float64(l.got.Load())/float64(t))
		}
		const barW = 20
		label := ui.Sanitize(l.label)
		if room := width - barW - 20; room > 10 && len([]rune(label)) > room {
			label = string([]rune(label)[:room-1]) + "…"
		}
		rows = append(rows, fmt.Sprintf("  %s %s %s %3.0f%%  %s", ui.Cyan("↓"), ui.Badge("apple"), ui.Bar(f, barW, ""), f*100, label))
	}
	return rows
}

// appleSave downloads one song to j.dest, embeds its cover, writes its tags and
// records it. In replace mode the YouTube file it replaces is moved to .mfetchy/replaced.
func appleSave(ctx context.Context, s *appleSession, ffmpeg, ffprobe, root string, j appleJob) error {
	t := j.t
	if j.dest != j.ytPath && fileExists(j.dest) {
		_ = s.st.AppleDone(t.ID, t.Title, t.Artist, j.dest)
		if j.replace && j.ytID != "" {
			_ = s.st.ReplaceWithApple(j.ytID, t.Title, t.Artist, j.dest)
		}
		return errAppleExists
	}
	if err := os.MkdirAll(filepath.Dir(j.dest), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(j.dest), ".mfy-apple-"+t.ID+".m4a")
	_ = os.Remove(tmp)
	defer os.Remove(tmp)
	if err := s.c.Download(ctx, t, tmp); err != nil {
		return err
	}

	cover := ""
	if art, err := s.c.FetchArtwork(ctx, t, 1200); err == nil && len(art) > 0 {
		cp := tmp + ".cover.jpg"
		if os.WriteFile(cp, art, 0o644) == nil {
			cover = cp
			defer os.Remove(cp)
		}
	}
	set := map[string]string{
		"title":        names.StripControl(t.Title),
		"artist":       names.StripControl(t.Artist),
		"album":        names.StripControl(t.Album),
		"album_artist": names.StripControl(firstNonEmpty(t.AlbumArtist, t.Artist)),
		"comment":      t.URL(),
	}
	opt := func(k, v string) {
		if v = names.StripControl(v); v != "" {
			set[k] = v
		}
	}
	opt("track", names.TrackTag(t.TrackNumber, t.TrackCount))
	opt("disc", names.TrackTag(t.DiscNumber, t.DiscCount))
	opt("date", t.ReleaseDate)
	opt("genre", t.Genre)
	opt("composer", t.Composer)
	opt("copyright", t.Copyright)
	// m4a can't hold these through ffmpeg (tag.Write drops them for it), but an Apple song
	// written to another format keeps them.
	opt("isrc", t.ISRC)
	opt("label", t.Label)
	opt("barcode", t.UPC)
	if t.Explicit {
		set["explicit"] = "1"
	}
	if err := tag.Write(ctx, ffmpeg, ffprobe, tmp, set, cover); err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	// Never file a damaged or cut-short download.
	if err := verify.File(ctx, ffprobe, tmp, verify.Want{Ext: ".m4a", Duration: float64(t.DurationMillis) / 1000}); err != nil {
		return err
	}

	var backup string
	if j.replace && fileExists(j.ytPath) {
		// The YouTube copy may know things Apple's tags don't (MusicBrainz ids, lyrics, a
		// genre): keep them. Failing to is not a reason to skip the upgrade.
		if from, err := tag.CarryTags(ctx, ffmpeg, ffprobe, tmp, string(catalog.Apple), j.ytPath, string(catalog.YouTube)); err == nil && len(from) > 0 {
			_ = s.st.SetProvenance(appleToCatalog(t).Key(), from)
		}
		rel, err := filepath.Rel(root, j.ytPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(j.ytPath)
		}
		backup = filepath.Join(appname.StateDir(root), "replaced", rel)
		if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
			return err
		}
		if err := os.Rename(j.ytPath, backup); err != nil {
			return fmt.Errorf("move the YouTube copy aside: %w", err)
		}
	}
	if err := os.Rename(tmp, j.dest); err != nil {
		if backup != "" {
			_ = os.Rename(backup, j.ytPath)
		}
		return err
	}
	_ = s.st.AppleDone(t.ID, t.Title, t.Artist, j.dest)
	if j.replace && j.ytID != "" {
		_ = s.st.ReplaceWithApple(j.ytID, t.Title, t.Artist, j.dest)
		lyrics.MoveSidecars(j.ytPath, j.dest) // the lyrics follow the song to its new file
	}
	return nil
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
