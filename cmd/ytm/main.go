// Command ytm downloads a YouTube Music library export as tagged audio files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/export"
	"github.com/bybrooklyn/ytm-to-mp3/internal/httpfetch"
	batch "github.com/bybrooklyn/ytm-to-mp3/internal/inputs"
	"github.com/bybrooklyn/ytm-to-mp3/internal/jobs"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/screen"
	"github.com/bybrooklyn/ytm-to-mp3/internal/spotify"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tidal"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

var version = "dev"

const usage = `ytm - your music library from YouTube Music, Apple Music and Spotify,
downloaded as tagged audio through Proton VPN

  ytm                     open ytm: downloads, your library, settings and tools in one place
  ytm <link or file>...   download right away: YouTube, Apple Music, Spotify, SoundCloud or Bandcamp links (songs,
                          albums, playlists), a Takeout .zip, folder or .csv, or a text file
                          of links. Each song comes from the best source you have: Apple Music
                          (256 kbps AAC) when you're signed in there and it has it, else YouTube.

Options for one run (your defaults are in ytm's Settings):
  -o DIR       music folder          -f FORMAT   mp3, m4a, opus, flac
  --no-vpn     skip the VPN this run -j N        max parallel downloads
  --connections N   VPN exits to use this run
  --dry-run    show what would happen, download nothing
  --retry      retry songs that were unavailable before
  --song       just the songs given: a song link normally brings its whole album, and a
               playlist or library the rest of every album it has a song from
               (--albums=false turns that off too; --albums-min N: only albums with N
               or more of your songs; the default is in Settings)
  --source youtube  get Apple Music and Spotify songs from YouTube even when
                    signed in to Apple Music (--maybe: also less certain Apple matches)
  --premium-upgrade   re-download songs at YouTube Premium quality (experimental,
                      slow, uses your account: turn it on in Settings first)
  -y           assume yes
  --completion SHELL  print tab completion for bash, zsh, fish or powershell
  --version, --help
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		if errors.Is(err, errBack) {
			return // Esc on the first screen: nothing to go back to, so leave
		}
		msg := ui.Sanitize(err.Error())
		if st, serr := os.Stderr.Stat(); serr == nil && st.Mode()&os.ModeCharDevice != 0 {
			// "  ✗ what went wrong" then any further lines as dim hints, like ytm's other lines
			first, rest, _ := strings.Cut(msg, "\n")
			fmt.Fprintln(os.Stderr, ui.Mark(ui.Fail("✗"), first))
			for _, l := range strings.Split(rest, "\n") {
				if l != "" {
					fmt.Fprintln(os.Stderr, "      "+ui.Dim(l))
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "ytm:", msg)
		}
		os.Exit(1)
	}
}

// opts are the flags shared by every command.
type opts struct {
	out     string
	format  string
	jobs    int
	dryRun  bool
	retry   bool
	all     bool
	yes     bool
	apply   bool
	rename  bool
	undo    bool
	offline bool
	noVPN   bool
	conns   int // VPN exits for this run (0 = the saved setting)
	// albums (on unless switched off in settings): a song link brings its whole album, and
	// a library or playlist download gets the rest of every album it has a song from
	albums    bool
	albumsMin int  // only albums with at least this many saved songs
	song      bool // --song: just the songs given, for this run (no albums)
	// the YouTube Premium upgrade pass (experimental; see premium.go)
	premiumUpgrade bool
	premiumLimit   int
	// after a download: fix the new songs' tags, and run the Premium upgrade if signed in
	tagAfter, premiumAfter bool
	lyricsAfter            bool // fetch lyrics (a .lrc beside each song and a tag in it) for the new songs
	// ytm apple match: replace the YouTube copies; also take the less certain matches
	replace, maybe bool
	// source forces the audio source for a unified (Spotify) import: "" = best available
	// (Apple when signed in, else YouTube), "youtube" = always YouTube.
	source string
	// preset are tracks already read (a live library): they are downloaded like an export.
	preset     []model.Track
	version    bool   // --version
	completion string // --completion SHELL
	// given is which flags were on the command line: they win over saved settings for the
	// whole run, even after the settings screen re-reads them.
	given map[string]bool
}

func parse(name string, args []string) (opts, []string, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are printed once by main, with a pointer to 'ytm --help'
	fs.Usage = func() {}
	fs.StringVar(&o.out, "o", "", "")
	fs.StringVar(&o.format, "f", "", "")
	fs.IntVar(&o.jobs, "j", 0, "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.retry, "retry", false, "")
	fs.BoolVar(&o.all, "all", false, "")
	fs.BoolVar(&o.yes, "y", false, "")
	fs.BoolVar(&o.apply, "apply", false, "")
	fs.BoolVar(&o.rename, "rename", false, "")
	fs.BoolVar(&o.undo, "undo", false, "")
	fs.BoolVar(&o.offline, "offline", false, "")
	fs.BoolVar(&o.noVPN, "no-vpn", false, "")
	fs.IntVar(&o.conns, "connections", 0, "")
	fs.BoolVar(&o.albums, "albums", false, "")
	fs.IntVar(&o.albumsMin, "albums-min", 1, "")
	fs.BoolVar(&o.song, "song", false, "")
	fs.BoolVar(&o.premiumUpgrade, "premium-upgrade", false, "")
	fs.IntVar(&o.premiumLimit, "premium-limit", 300, "")
	fs.BoolVar(&o.tagAfter, "tag-after", false, "")
	fs.BoolVar(&o.premiumAfter, "premium-after", false, "")
	fs.BoolVar(&o.lyricsAfter, "lyrics-after", false, "")
	fs.BoolVar(&o.replace, "replace", false, "")
	fs.BoolVar(&o.maybe, "maybe", false, "")
	fs.StringVar(&o.source, "source", "", "")
	fs.BoolVar(&o.version, "version", false, "")
	fs.BoolVar(&o.version, "v", false, "")
	fs.StringVar(&o.completion, "completion", "", "")
	// Go's flag package stops at the first positional; keep going so flags may follow it.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return o, nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	cfg, cerr := config.LoadChecked()
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "ytm: warning:", cerr)
	}
	if o.jobs < 0 {
		return o, nil, fmt.Errorf("-j must be 1 or more")
	}
	if o.source = strings.ToLower(o.source); o.source != "" && o.source != "youtube" {
		return o, nil, fmt.Errorf("--source must be youtube (or left out for the best source), not %q", o.source)
	}
	if o.jobs == 0 {
		o.jobs = cfg.Jobs // saved in settings; 0 = automatic
	}
	o.format = strings.ToLower(o.format)
	if o.out == "" {
		o.out = cfg.Out
	}
	if o.out == "" {
		o.out = config.DefaultOut()
	}
	o.out = config.ExpandPath(o.out)
	if o.format == "" {
		o.format = orDefault(cfg.Format, config.DefaultFormat)
	}
	if !engine.ValidFormat(o.format) {
		return o, nil, fmt.Errorf("unknown format %q (use %s)", o.format, strings.Join(engine.Formats, ", "))
	}
	// The extras are on unless switched off in settings; a flag (--albums=false, --retry,
	// ...) decides for this run only.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	o.given = given
	if !given["albums"] {
		o.albums = !cfg.NoAlbums
	}
	if !given["retry"] {
		o.retry = !cfg.NoRetry
	}
	if !given["tag-after"] {
		o.tagAfter = !cfg.NoTagAfter
	}
	if !given["premium-after"] {
		o.premiumAfter = !cfg.NoPremiumAfter
	}
	if !given["lyrics-after"] {
		o.lyricsAfter = !cfg.NoLyricsAfter
	}
	return o, pos, nil
}

// movedInto says where each old subcommand lives now: everything is in ytm itself.
var movedInto = map[string]string{
	"setup":  "Settings",
	"login":  "Settings (VPN account)",
	"logout": "Settings (VPN account)",
	"status": "Settings (Test VPN)",
	"doctor": "More tools > Check that everything works",
	"export": "\"Download my YouTube Music library\" (it helps you get the export)",
	"tag":    "More tools > Fix tags",
	"update": "More tools > Update yt-dlp, ffmpeg and deno",
	"apple":  "Apple Music... (links just work: ytm <link>)",
}

func run(args []string) error {
	o, pos, err := parse("ytm", args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return nil
		}
		return fmt.Errorf("%w (see: ytm --help)", err)
	}
	switch {
	case o.version || (len(pos) == 1 && pos[0] == "version"):
		fmt.Println("ytm", version)
		return nil
	case len(pos) == 1 && pos[0] == "help":
		printUsage()
		return nil
	case o.completion != "":
		s, err := completionScript(o.completion)
		if err != nil {
			return err
		}
		fmt.Print(s)
		return nil
	}
	// The old subcommands: "ytm apple <link>" / "ytm spotify <link>" are just links now; the
	// rest say where they went (unless a file of that name is meant).
	if len(pos) > 0 {
		if _, err := os.Stat(pos[0]); err != nil {
			if (pos[0] == "apple" || pos[0] == "spotify") && len(pos) > 1 {
				pos = pos[1:]
			} else if where, ok := movedInto[pos[0]]; ok {
				return fmt.Errorf("'ytm %s' is now part of ytm itself: run ytm and pick %s", pos[0], where)
			} else if pos[0] == "completion" {
				return errors.New("use: ytm --completion bash|zsh|fish|powershell")
			}
		}
	}
	u := ui.New()
	ctx, stop := signalContext(u)
	defer stop()

	if o.premiumUpgrade {
		return premiumUpgrade(ctx, u, o, false)
	}

	switch len(pos) {
	case 0:
		if u.Interactive() {
			return runHome(ctx, u, o)
		}
		p, err := wizard(ctx, u)
		if err != nil {
			return err
		}
		return download(ctx, u, o, p)
	}
	return download(ctx, u, o, pos...)
}

// signalContext cancels on the first Ctrl-C and exits hard on the second.
func signalContext(u *ui.UI) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		// Through the UI, not straight to the terminal: a stray line under the live block
		// throws off its redraw and leaves a stale copy on screen.
		u.Notice("Stopping... finishing what's in flight (Ctrl-C again to quit now).")
		cancel()
		<-ch
		u.RestoreCursor()
		os.Exit(130)
	}()
	return ctx, cancel
}

// wizard finds an export on disk or guides the user to get one.
func wizard(ctx context.Context, u *ui.UI) (string, error) {
	// An export found lying around is only used after the user has seen what it is and
	// said yes; with no terminal to ask on, it is never adopted automatically.
	if p, ok := findExisting(); ok && u.Interactive() {
		if u.Confirm("Found "+export.Describe(p)+"\n  at "+p+"\nDownload it?", true) {
			return p, nil
		}
	}
	if !u.Interactive() {
		printUsage()
		return "", errors.New("no export given")
	}
	u.Warn("ytm needs your YouTube Music export (a Google Takeout file).")
	if ans := u.Ask("Path to it, or press Enter to get one now: ", ""); ans != "" {
		return strings.Trim(ans, `"' `), nil
	}
	return export.Guide(ctx, u)
}

func findExisting() (string, bool) {
	if p, ok := export.Find(time.Time{}); ok {
		return p, true
	}
	for _, d := range export.SearchDirs() {
		for _, n := range []string{"music library songs.csv", "Takeout"} {
			p := filepath.Join(d, n)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	return "", false
}

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

// download runs one batch of inputs of any mix. When songs failed for a reason that may pass
// (a connection drop, a rate limit) it offers, on a terminal, to try just those again: the
// state database already holds what arrived, so a second pass only redoes the rest.
func download(ctx context.Context, u *ui.UI, o opts, inputs ...string) error {
	for round := 0; ; round++ {
		led := &jobs.Ledger{}
		err := downloadOnce(ctx, u, o, led, inputs)
		c := led.Counts()
		if ctx.Err() != nil || c.Retryable == 0 || round >= 2 || !u.Interactive() {
			return err
		}
		if !u.Confirm(fmt.Sprintf("%s failed for a reason that may pass. Try those again now?", count(c.Retryable, "song")), false) {
			return err
		}
	}
}

func downloadOnce(ctx context.Context, u *ui.UI, o opts, led *jobs.Ledger, inputs []string) (rerr error) {
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
	ex := &runExits{ctx: ctx, u: u, o: o, led: led, sink: jobs.Multi{led, u.Sink()}}
	// However the run ends (finished, failed, Ctrl-C), say what happened to each song.
	defer func() { printRunSummary(u, led, ctx.Err() != nil) }()
	ex.tools = sync.OnceValues(func() (deps.Tools, error) { return deps.Ensure(ctx, logf) })
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
	// SoundCloud and Bandcamp links: yt-dlp finds the songs, ytm files and tags them.
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
	if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr == nil || !o.dryRun {
		s, err := store.Open(filepath.Join(o.out, ".ytm"))
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
	httpfetch.Sweep(filepath.Join(o.out, ".ytm", "partial"), 3*24*time.Hour)
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
		return afterDownload(ctx, u, o, st, began)
	}

	if err := ensure(); err != nil {
		return err
	}
	if err := connect(); err != nil {
		return err
	}
	cache, _ := config.CacheDir()
	sum := engine.Run(ctx, engine.Options{
		Out: o.out, Format: o.format, Jobs: o.jobs, Tools: tools,
		Store: st, UI: u, CacheDir: filepath.Join(cache, "yt-dlp"), Exits: xs.Exits, Replace: xs.Replace, Healthy: xs.Healthy,
		OnBlocked: xs.Blocked, OnGood: xs.Good, NewExits: xs.New, MusicFirst: plan.MusicFirst, Replacements: plan.Replacements,
	}, plan.Todo)

	ex.recordYouTube(st, plan.Todo, nil)
	u.Styled(runSummary(sum.Done, sum.Failed, sum.Bytes, sum.Elapsed, ""))
	if os.Getenv("YTM_DEBUG") != "" { // per-exit detail is for debugging, not everyday output
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
				ui.Dim("· run ytm again to retry the temporary ones")))
			for _, l := range failureLines(fails, 15) {
				u.Styled(l)
			}
		}
	}
	if sum.Interrupted {
		return context.Canceled
	}
	return afterDownload(ctx, u, o, st, began)
}

// afterDownload runs the extras that follow a download (each on unless switched off in
// settings): MusicBrainz tags for the songs just downloaded (not the whole library, which
// would take an hour at MusicBrainz's one request a second), then the Premium upgrade if
// a sign-in is set up.
func afterDownload(ctx context.Context, u *ui.UI, o opts, st *store.Store, since time.Time) error {
	if o.tagAfter && !config.Load().Private {
		if files := st.DoneSince(since); len(files) > 0 {
			u.Head("Fixing the tags of %s with MusicBrainz · turn off in settings", count(len(files), "new song"))
			t := o
			t.apply, t.rename, t.undo, t.offline = true, false, false, false
			if err := runTag(ctx, u, t, o.out, files...); err != nil {
				if ctx.Err() != nil {
					return context.Canceled
				}
				u.Bad("tags: %v", err)
			}
		}
	}
	if o.lyricsAfter {
		if files := st.DoneSince(since); len(files) > 0 {
			u.Head("Fetching lyrics for %s · turn off in settings", count(len(files), "new song"))
			if err := fetchLyrics(ctx, u, o.out, files); err != nil {
				if ctx.Err() != nil {
					return context.Canceled
				}
				u.Bad("lyrics: %v", err)
			}
		}
	}
	if o.premiumAfter && config.Load().Premium {
		st.Close() // the upgrade opens the database itself
		return premiumUpgrade(ctx, u, o, true)
	}
	return nil
}

// idFromTags reads the video id ytm writes into every file's comment tag
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

// removeLegacyCookies deletes the cookie jar older ytm versions may have left behind.
// ytm never uses a Google account login: when YouTube blocks an exit, ytm moves to a
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
	once sync.Once
	done chan struct{}
	xs   exitSet
	err  error
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
	return deps.Ensure(ctx, u.Logf)
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
			u.Note("no VPN · set one up in ytm's Settings to download faster")
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
	debug := os.Getenv("YTM_DEBUG") != ""
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
