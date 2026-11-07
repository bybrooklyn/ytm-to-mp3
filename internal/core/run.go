package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/httpfetch"
	batch "github.com/bybrooklyn/mfetchy/internal/inputs"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/spotify"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/takeout"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpn"
)

// singleVideoID returns the video id of a link to one video (watch?v=, youtu.be/, shorts/),
// or false for playlists, albums, channels and anything it doesn't recognise.
func singleVideoID(link string) (string, bool) {
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("list") != "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var id string
	switch {
	case host == "youtu.be":
		id = strings.Trim(u.Path, "/")
	case host == "youtube.com" || host == "music.youtube.com" || host == "m.youtube.com":
		switch {
		case u.Path == "/watch":
			id = u.Query().Get("v")
		case strings.HasPrefix(u.Path, "/shorts/"):
			id = strings.TrimPrefix(u.Path, "/shorts/")
		}
	}
	if !model.ValidID(id) {
		return "", false
	}
	return id, true
}

func isURL(s string) bool { return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") }

// asURL accepts links pasted without a scheme (youtu.be/..., music.youtube.com/...) and
// bare playlist ids, returning the full URL.
func asURL(s string) (string, bool) {
	if isURL(s) {
		return s, true
	}
	for _, h := range []string{"youtube.com/", "youtu.be/", "music.youtube.com/", "www.youtube.com/"} {
		if strings.HasPrefix(s, h) {
			return "https://" + s, true
		}
	}
	if len(s) >= 18 && (strings.HasPrefix(s, "PL") || strings.HasPrefix(s, "OLAK5uy_") || strings.HasPrefix(s, "RDCLAK")) &&
		!strings.ContainsAny(s, `/\. `) {
		return "https://www.youtube.com/playlist?list=" + s, true
	}
	return "", false
}

func downloadOnce(ctx context.Context, u *ui.UI, o opts, led *jobs.Ledger, extra jobs.Sink, inputs []string) (rerr error) {
	logf := u.Logf
	var tools deps.Tools
	removeLegacyCookies()

	// One clean list from whatever was given: pasted blocks and lists of links are split,
	// links normalised, duplicates dropped; lines that are no use are named, the rest goes on.
	if len(inputs) > 0 {
		res := batch.Parse(inputs)
		for _, b := range res.Bad {
			u.Warn("%s", b.String())
		}
		if res.Dups > 0 {
			u.Note("%s dropped (the same link or file given twice)", count(res.Dups, "duplicate"))
		}
		if len(res.Items) == 0 {
			if len(res.Bad) > 0 {
				return fmt.Errorf("nothing to download: none of the %s could be used.\nPaste YouTube, Apple Music, Spotify, Tidal, SoundCloud or Bandcamp links (one per line is fine) or the path to a Takeout .zip or a text file of links", count(len(res.Bad), "line"))
			}
			if len(o.preset) == 0 {
				return errors.New("nothing to download")
			}
		}
		inputs = res.Values()
	}

	// The VPN is brought up the first time anything needs the network (so even looking up
	// a pasted playlist goes out through it and not from your own address), and every part
	// of the run shares it.
	ex := &runExits{ctx: ctx, u: u, o: o, led: led, sink: jobs.Multi{led, u.Sink(), extra}, extra: extra}
	// However the run ends (finished, failed, Ctrl-C), say what happened to each song.
	defer func() { printRunSummary(u, led, ctx.Err() != nil) }()
	ex.tools = sync.OnceValues(func() (deps.Tools, error) { return ensureTools(ctx, logf) })
	defer ex.Close()
	setCurrentRun(ex) // the tag fixing that follows reuses these exits
	defer setCurrentRun(nil)
	var xs exitSet
	connect := func() (err error) { xs, err = ex.get(); return err }
	ensure := func() (err error) {
		if tools.YtDlp == "" {
			tools, err = ex.tools()
		}
		return err
	}
	// A run that will go online starts connecting now, and checks yt-dlp and ffmpeg at the
	// same time, so both are ready by the time the first link is looked up.
	for _, in := range inputs {
		in = strings.Trim(in, `"' `)
		_, link := asURL(in)
		export := o.albums && (fileExists(in) || fileExists(takeout.CleanPath(in))) // its albums get looked up
		if link || export {
			ex.start()
			go ex.tools()
			break
		}
	}

	// Spotify and Apple Music links name songs; the unified pipeline fetches each from the
	// best source there is (Apple Music when signed in and matched, otherwise YouTube).
	var catalogLinks, webLinks, rest []string
	for _, in := range inputs {
		if t := strings.Trim(in, `"' `); spotify.IsURL(t) || apple.IsURL(t) || tidal.IsURL(t) {
			catalogLinks = append(catalogLinks, t)
		} else if engine.WebSite(t) != "" {
			webLinks = append(webLinks, t)
		} else {
			rest = append(rest, in)
		}
	}
	// SoundCloud and Bandcamp links: yt-dlp finds the songs, mfetchy files and tags them.
	// They run alongside the Spotify/Apple/Tidal part (which has its own lanes): the two
	// don't share a source, so neither waits for the other.
	var webErr error
	var webDone sync.WaitGroup
	if len(webLinks) > 0 {
		webDone.Add(1)
		go func() {
			defer webDone.Done()
			webErr = downloadWeb(ctx, u.Lane("web"), o, ex, webLinks)
		}()
	}
	var catErr error
	if len(catalogLinks) > 0 {
		catErr = downloadUnified(ctx, u, o, ex, catalogLinks)
	}
	webDone.Wait()
	if len(rest) == 0 || ctx.Err() != nil {
		return errors.Join(catErr, webErr)
	}
	// A failed part is reported and the rest of the batch goes on.
	for _, e := range []error{catErr, webErr} {
		if e != nil {
			u.Bad("%v", e)
		}
	}
	inputs = rest

	var tracks []model.Track
	fromExport := false // some input was a Takeout export (a library import)
	seen := map[string]bool{}
	add := func(ts []model.Track) {
		for _, t := range ts {
			if !seen[t.ID] {
				seen[t.ID] = true
				tracks = append(tracks, t)
			}
		}
	}
	// Every input gets a slot, so the songs keep the order they were given in; links that
	// need looking up are filled in afterwards, several at a time.
	songAlbums := o.albums && !o.song // a song link brings its whole album
	fromList := false                 // some input was a playlist or album link
	slots := make([][]model.Track, len(inputs))
	var lookups []linkLookup
	song := func(i int, id string) {
		if songAlbums {
			lookups = append(lookups, linkLookup{slot: i, song: id})
		} else {
			slots[i] = []model.Track{{ID: id}} // the download looks it up anyway
		}
	}
	for i, input := range inputs {
		input = strings.Trim(input, `"' `)
		if _, ok := asURL(input); !ok {
			if _, err := os.Stat(input); err != nil {
				input = takeout.CleanPath(input) // ~/..., file:// links, My\ File.zip
			}
		}
		if link, ok := asURL(input); ok {
			if id, ok := singleVideoID(link); ok {
				song(i, id)
			} else {
				lookups = append(lookups, linkLookup{slot: i, link: link})
				fromList = true
			}
		} else if _, err := os.Stat(input); err != nil {
			if id, ok := takeout.ExtractID(input); ok && len(input) == 11 {
				song(i, id)
				continue
			}
			return fmt.Errorf("%q is not a YouTube link or an existing file.\nPaste a video/playlist link (in quotes, since & and ? confuse shells) or the path to your Takeout .zip", input)
		} else {
			ts, err := takeout.Load(input, o.all)
			if err != nil {
				return err
			}
			fromExport = true
			slots[i] = ts
		}
	}
	if len(o.preset) > 0 {
		slots = append(slots, o.preset)
		fromExport = true
	}
	if len(lookups) > 0 {
		if err := ensure(); err != nil {
			return err
		}
		if err := connect(); err != nil {
			return err
		}
		if err := lookUpLinks(ctx, u, o, tools, exitProxies(xs), lookups, slots); err != nil {
			return err
		}
	}
	for _, s := range slots {
		add(s)
	}
	u.Note("Found %s", takeout.Describe(tracks))

	// The state database (it also remembers album lookups); a dry run never creates one.
	var st *store.Store
	if _, serr := os.Stat(appname.StateDir(o.out)); serr == nil || !o.dryRun {
		s, err := store.Open(appname.StateDir(o.out))
		if err != nil {
			return err
		}
		defer s.Close()
		st = s
	}
	// Completing albums: every album the library or a playlist has a song from gets its
	// other songs too, after asking (with the size). A song link already brought its album.
	if o.albums && !o.song && (fromExport || fromList) {
		if err := ensure(); err != nil {
			return err
		}
		if err := connect(); err != nil {
			return err
		}
		var proxies []string
		for _, x := range xs.Exits {
			proxies = append(proxies, x.Proxy)
		}
		add(completeAlbums(ctx, u, o, tools, proxies, st, tracks))
	}
	if st == nil {
		// Nothing downloaded here yet and a dry run must not create anything.
		u.Styled(planLine(len(tracks), 0, o.out, o.format))
		for _, t := range tracks {
			u.Item(t.ID, "%s", t.Label())
		}
		return nil
	}
	numberFromAlbums(st, tracks)
	if n, _ := st.ImportDisk(o.out, idFromTags(ctx, logf)); n > 0 {
		u.Note("Recognised %d files already in %s", n, tilde(o.out))
	}
	// A song this run (or an earlier one) already fetched from a better source than YouTube,
	// under another link, is not fetched again.
	if byName := heldByName(st); len(byName) > 0 {
		kept := tracks[:0:0]
		better := 0
		for _, t := range tracks {
			if t.Title != "" {
				if h, ok := byName[catalog.NameKey(t.Title, t.PrimaryArtist())]; ok && h.Source != "" &&
					catalog.Rank(catalog.Source(h.Source)) > catalog.Rank(catalog.YouTube) && fileExists(h.Path) {
					better++
					continue
				}
			}
			kept = append(kept, t)
		}
		if better > 0 {
			u.Note("%s already held from a better source than YouTube, left alone", count(better, "song"))
			led.NoteHeld(better)
		}
		tracks = kept
	}
	plan, err := st.Plan(tracks, o.retry)
	if err != nil {
		return err
	}
	led.NoteHeld(plan.Done)
	httpfetch.Sweep(filepath.Join(appname.StateDir(o.out), "partial"), 3*24*time.Hour)
	u.Styled(planLine(len(plan.Todo), plan.Done, o.out, o.format))
	if plan.Skipped > 0 {
		u.Note("%d tracks were unavailable last time and are skipped · --retry tries them again", plan.Skipped)
	}
	if n := len(plan.MusicFirst); n > 0 {
		u.Note("%d tracks that were unavailable last time will be looked for on YouTube Music", n)
	}
	if o.dryRun {
		for _, t := range plan.Todo {
			u.Item(t.ID, "%s", t.Label())
		}
		return nil
	}
	began := time.Now()
	if len(plan.Todo) == 0 {
		return afterDownload(ctx, u, o, st, began, nil)
	}
	ex.queue(engine.SourceName, plan.Todo...)

	if err := ensure(); err != nil {
		return err
	}
	if err := connect(); err != nil {
		return err
	}
	cache, _ := config.CacheDir()
	pr := startPost(ctx, u, o, st, began) // tags and lyrics follow the downloads as songs arrive
	sum := engine.Run(ctx, engine.Options{
		Out: o.out, Format: o.format, Jobs: o.jobs, Tools: tools,
		Store: st, UI: u, CacheDir: filepath.Join(cache, "yt-dlp"), Exits: xs.Exits, Replace: xs.Replace, Healthy: xs.Healthy,
		OnBlocked: xs.Blocked, OnGood: xs.Good, NewExits: xs.New, MusicFirst: plan.MusicFirst, Replacements: plan.Replacements, Sink: ex.extra,
	}, plan.Todo)

	ex.recordYouTube(st, plan.Todo, nil)
	u.Styled(runSummary(sum.Done, sum.Failed, sum.Bytes, sum.Elapsed, ""))
	if appname.Getenv("DEBUG") != "" { // per-exit detail is for debugging, not everyday output
		if sum.Swaps > 0 {
			u.Note("%d exit(s) were blocked and replaced with other locations", sum.Swaps)
		}
		for _, x := range sum.PerExit {
			u.Item(fmt.Sprintf("%-24s", x.Name), "%4d tracks  %s", x.Done, engine.HumanBytes(x.Bytes))
		}
	}
	if all, _ := st.Failures(); len(all) > 0 {
		// A failed id whose song downloaded under another id isn't missing from the library.
		var fails []store.Failure
		dups := 0
		for _, f := range all {
			if f.OtherVersion {
				dups++
			} else {
				fails = append(fails, f)
			}
		}
		if dups > 0 {
			u.Note("%d unavailable track(s) are other versions of songs you already have", dups)
		}
		if len(fails) > 0 {
			u.Styled(fmt.Sprintf("\n  %s %s", ui.Bold(fmt.Sprintf("Not downloaded (%d)", len(fails))),
				ui.Dim("· run mfy again to retry the temporary ones")))
			for _, l := range failureLines(fails, 15) {
				u.Styled(l)
			}
		}
	}
	if sum.Interrupted {
		pr.Stop()
		return context.Canceled
	}
	return afterDownload(ctx, u, o, st, began, pr)
}

// afterDownload runs the extras that follow a download (each on unless switched off in
// settings): MusicBrainz tags for the songs just downloaded (not the whole library, which
// would take an hour at MusicBrainz's one request a second), then the Premium upgrade if
// a sign-in is set up.
func afterDownload(ctx context.Context, u *ui.UI, o opts, st *store.Store, since time.Time, pr *postRunner) error {
	if pr == nil {
		pr = startPost(ctx, u, o, st, since)
	}
	// The tag and lyrics passes have been following the downloads; this does what is left.
	if err := pr.Finish(); err != nil {
		return err
	}
	if files := st.DoneSince(since); len(files) > 0 {
		writeSidecars(ctx, u, o.out, files)
	}
	if o.premiumAfter && config.Load().Premium {
		st.Close() // the upgrade opens the database itself
		return premiumUpgrade(ctx, u, o, true)
	}
	return nil
}

// idFromTags reads the video id mfetchy writes into every file's comment tag
// ("https://music.youtube.com/watch?v=ID"), for recognising files whose names don't carry
// it. ffprobe is only fetched if a file actually needs reading.
func idFromTags(ctx context.Context, logf func(string, ...any)) func(string) string {
	var ffprobe string
	var once sync.Once
	return func(p string) string {
		once.Do(func() { _, ffprobe, _ = deps.EnsureMedia(ctx, logf) })
		if ffprobe == "" {
			return ""
		}
		info, err := tag.Probe(ctx, ffprobe, p)
		if err != nil {
			return ""
		}
		id, _ := takeout.ExtractID(info.Tags["comment"])
		return id
	}
}

// removeLegacyCookies deletes the cookie jar older mfetchy versions may have left behind.
// mfetchy never uses a Google account login: when YouTube blocks an exit, mfetchy moves to a
// different location instead.
func removeLegacyCookies() {
	if cache, err := config.CacheDir(); err == nil {
		_ = os.Remove(filepath.Join(cache, "cookies.txt"))
	}
}

// runExits connects the VPN once per run and hands the same exits to every part of it, so
// a run mixing YouTube, Apple Music and Spotify links connects once. start begins
// connecting in the background (as soon as a run knows it will need the network), so the
// handshakes overlap with everything that doesn't need them: checking yt-dlp and ffmpeg,
// reading an export, opening the database. get waits for the first tunnel.
type runExits struct {
	ctx context.Context
	u   *ui.UI
	o   opts
	// tools checks (and if need be fetches) yt-dlp and ffmpeg once per run, shared by every
	// part of it and safe to start early in the background.
	tools func() (deps.Tools, error)
	// led remembers how every song of the run ended (for the summary and the retry offer);
	// sink is led plus the terminal, for sources that report through events.
	led  *jobs.Ledger
	sink jobs.Sink
	// extra is the caller's sink (the window): every event goes to it as well.
	extra jobs.Sink
	once  sync.Once
	done  chan struct{}
	xs    exitSet
	err   error
}

func (r *runExits) start() {
	r.once.Do(func() {
		r.done = make(chan struct{})
		go func() {
			defer close(r.done)
			r.xs, r.err = buildExits(r.ctx, r.u, r.o)
		}()
	})
}

func (r *runExits) get() (exitSet, error) {
	r.start()
	<-r.done
	return r.xs, r.err
}

// ensureTools is r.tools, or a plain check when the run set none up.
func (r *runExits) ensureTools(ctx context.Context, u *ui.UI) (deps.Tools, error) {
	if r.tools != nil {
		return r.tools()
	}
	return ensureTools(ctx, u.Logf)
}

func (r *runExits) Close() {
	if r.done == nil {
		return
	}
	<-r.done // a connect still under way stops with the run's context
	if r.xs.Close != nil {
		r.xs.Close()
	}
}

// exitSet is the VPN side of a run: the exits that are up, how to replace or check one,
// the hooks that feed block/throughput results back into the server cache, and a channel
// of exits that finish connecting after the run has started.
type exitSet struct {
	Exits   []vpn.Exit
	Replace func(context.Context, vpn.Exit) (vpn.Exit, error)
	Healthy func(vpn.Exit) bool
	Blocked func(vpn.Exit, string)
	Good    func(vpn.Exit, float64)
	New     <-chan vpn.Exit
	Close   func()
}

// buildExits connects the Proton VPN tunnels when you're signed in (unless --no-vpn).
// The run goes on as soon as the first is up; the rest join while it runs. It never
// silently falls back to your own IP: if the VPN was meant to be on and can't connect,
// it stops.
// connections is how many VPN exits to open: the saved number (3 when unset), or with
// "auto" (-1) the Proton plan's device limit minus one kept free for a spare. Auto is not
// the default: in a 120-song test 10 exits were no faster than 6 (2026-11-04).
func connections(cfg config.Config, v vpnState) int {
	if cfg.Connections > 0 {
		return cfg.Connections
	}
	if cfg.Connections == 0 {
		return 3
	}
	if _, limit := vpn.LoadCache(vpn.CachePathFor(v.Prov.ID()), v.Prov.AccountID()).Servers(); limit > 1 {
		return min(limit-1, screen.MaxConns)
	}
	return 3
}

// connectionsLabel is connections for display: "auto (10)" or the saved number.
func connectionsLabel(cfg config.Config, v vpnState) string {
	if cfg.Connections >= 0 {
		return fmt.Sprint(connections(cfg, v))
	}
	return fmt.Sprintf("auto (%d)", connections(cfg, v))
}

func buildExits(ctx context.Context, u *ui.UI, o opts) (exitSet, error) {
	none := exitSet{Close: func() {}}
	cfg := config.Load()
	v := currentVPN(cfg)
	pc, label := v.Prov, providerLabel(providerID(cfg))
	if o.noVPN || cfg.NoVPN || !v.SignedIn {
		if !o.noVPN && !cfg.NoVPN {
			u.Note("no VPN · set one up in mfetchy's Settings to download faster")
		}
		return none, nil
	}
	n := connections(cfg, v)
	if o.conns > 0 {
		n = min(o.conns, screen.MaxConns)
	}
	// A live line while the tunnels come up: how many are up, how long it's been. The run
	// goes on at the first one, so this is usually a few seconds.
	var up atomic.Int32
	began := time.Now()
	stopStatus := make(chan struct{})
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		tick := time.NewTicker(120 * time.Millisecond)
		defer tick.Stop()
		for {
			u.Status("Connecting to %s · %d of %d up · %ds", label, up.Load(), n, int(time.Since(began).Seconds()))
			select {
			case <-stopStatus:
				return
			case <-tick.C:
			}
		}
	}()
	debug := appname.Getenv("DEBUG") != ""
	vpnLog := func(f string, a ...any) {
		// Each server connecting (or failing to) is detail; anything else is worth saying.
		s := fmt.Sprintf(f, a...)
		if strings.HasPrefix(s, "  ✓ ") {
			up.Add(1)
		}
		if debug || !(strings.HasPrefix(s, "  ✓ ") || strings.HasPrefix(s, "  ✗ ")) {
			u.Logf("%s", s)
		}
	}
	var countries []string
	if c := strings.ToUpper(strings.TrimSpace(cfg.Countries)); c != "" {
		countries = []string{c}
	}
	late := make(chan vpn.Exit, 64)
	done := make(chan struct{})
	pool, err := vpn.OpenWith(ctx, pc, n, vpnLog, vpn.Options{
		// The run goes on as soon as one tunnel is up (a link is looked up through it, and
		// downloads start on it); the rest join through New a few seconds later.
		MinReady:  1,
		Countries: countries,
		OnExit: func(e vpn.Exit) {
			select {
			case late <- e:
			case <-done:
			case <-ctx.Done():
			}
		},
	})
	close(stopStatus)
	<-statusDone
	u.ClearStatus()
	if err != nil {
		return none, fmt.Errorf("%w (use --no-vpn to download without it)", err)
	}
	u.Styled(fmt.Sprintf("  %s %s %s", ui.Green("●"), label,
		ui.Dim(fmt.Sprintf("· %s up, %d more joining", count(len(pool.Exits()), "server"), max(0, n-len(pool.Exits()))))))
	var once sync.Once
	return exitSet{
		Exits: pool.Exits(), Replace: pool.Replace, Healthy: pool.Healthy,
		Blocked: pool.MarkBlocked, Good: pool.MarkGood, New: late,
		Close: func() { once.Do(func() { close(done); pool.Close() }) },
	}, nil
}
