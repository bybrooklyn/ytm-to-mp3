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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/export"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

var version = "dev"

const usage = `ytm - download YouTube / YouTube Music as tagged audio, through Proton VPN

  ytm setup               one-time setup: Proton sign-in, music folder, format
  ytm <link>...           download videos, playlists or albums
  ytm [takeout.zip]       download your YouTube Music library (guides you if you have no export)
  ytm status              show settings and test the VPN exits
  ytm doctor              check tools, network, folders and login; says how to fix problems
  ytm logout              forget your Proton login
  ytm completion SHELL    tab completion (bash, zsh, fish, powershell)

Options (for one run; ytm setup saves your defaults):
  -o DIR       music folder          -f FORMAT   mp3, m4a, opus, flac
  --no-vpn     skip the VPN this run -j N        max parallel downloads
  --connections N   VPN exits to use this run
  --dry-run    show what would happen
  --retry      retry tracks that were unavailable before
  --premium-upgrade   re-download songs at YouTube Premium quality (experimental,
                      slow, uses your account: turn it on in ytm setup first)
  --albums     also download the rest of every album you have a song from
               (--albums-min N: only albums with N or more of your songs)
  -y           assume yes

Other: ytm export (get your Takeout), ytm tag <folder> (fix metadata), ytm update
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "ytm:", err)
		os.Exit(1)
	}
}

// opts are the flags shared by every command.
type opts struct {
	out       string
	format    string
	jobs      int
	dryRun    bool
	retry     bool
	all       bool
	yes       bool
	apply     bool
	rename    bool
	undo      bool
	offline   bool
	noVPN     bool
	conns     int  // VPN exits for this run (0 = the saved setting)
	albums    bool // also download the rest of every album the library has a song from
	albumsMin int  // only albums with at least this many saved songs
	// albumsAsked: --albums was given, so links get their albums completed too (otherwise
	// only library imports do)
	albumsAsked bool
	// the YouTube Premium upgrade pass (experimental; see premium.go)
	premiumUpgrade bool
	premiumLimit   int
	// after a download: fix the new songs' tags, and run the Premium upgrade if signed in
	tagAfter, premiumAfter bool
}

func parse(name string, args []string) (opts, []string, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are printed once by main, with a pointer to 'ytm help'
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
	fs.BoolVar(&o.premiumUpgrade, "premium-upgrade", false, "")
	fs.IntVar(&o.premiumLimit, "premium-limit", 300, "")
	fs.BoolVar(&o.tagAfter, "tag-after", false, "")
	fs.BoolVar(&o.premiumAfter, "premium-after", false, "")
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
	o.albumsAsked = given["albums"] && o.albums
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
	return o, pos, nil
}

// commands are the words that act as subcommands when they come first (flags may precede them).
var commands = map[string]bool{"export": true, "tag": true, "update": true, "version": true, "help": true,
	"setup": true, "login": true, "logout": true, "status": true, "doctor": true, "completion": true}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		args = []string{"version"}
	}
	o, pos, err := parse("ytm", args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w (see: ytm help)", err)
	}
	cmd := ""
	if len(pos) > 0 && commands[pos[0]] {
		cmd, pos = pos[0], pos[1:]
		if len(pos) > 0 && cmd != "tag" && cmd != "export" && cmd != "completion" {
			return fmt.Errorf("'ytm %s' takes no arguments (got %q)", cmd, pos[0])
		}
	}
	u := ui.New()
	ctx, stop := signalContext(u)
	defer stop()

	switch cmd {
	case "help":
		fmt.Print(usage)
		return nil
	case "version":
		fmt.Println("ytm", version)
		return nil
	case "update":
		return deps.Update(ctx, func(f string, a ...any) { u.Printf(f, a...) })
	case "export":
		p, err := export.Guide(ctx, u)
		if err != nil {
			return err
		}
		return download(ctx, u, o, p)
	case "setup":
		return runSetup(ctx, u, false)
	case "login":
		return runSetup(ctx, u, true)
	case "logout":
		pc := protonClient()
		if !pc.SignedIn() {
			u.Printf("you weren't signed in to Proton")
			return nil
		}
		pc.Revoke(ctx)
		u.Printf("signed out of Proton (and removed ytm's saved login). The ytm device key stays listed in your Proton account until you remove it at account.protonvpn.com.")
		return nil
	case "status":
		return runStatus(ctx, u)
	case "doctor":
		return runDoctor(ctx, u, o)
	case "completion":
		if len(pos) != 1 {
			return errors.New("usage: ytm completion bash|zsh|fish|powershell")
		}
		s, err := completionScript(pos[0])
		if err != nil {
			return err
		}
		fmt.Print(s)
		return nil
	case "tag":
		if len(pos) != 1 {
			return errors.New("usage: ytm tag <folder> [--apply] [--rename] [--undo] [--offline] [-y]")
		}
		return runTag(ctx, u, o, pos[0])
	}
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
		fmt.Print(usage)
		return "", errors.New("no export given")
	}
	u.Printf("ytm needs your YouTube Music export (a Google Takeout file).")
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

func download(ctx context.Context, u *ui.UI, o opts, inputs ...string) error {
	logf := func(f string, a ...any) { u.Printf(f, a...) }
	var tools deps.Tools
	removeLegacyCookies()

	// The VPN is brought up the first time anything needs the network, so even looking up
	// a pasted playlist goes out through it and not from your own address.
	var (
		xs        = exitSet{Close: func() {}}
		connected bool
	)
	defer func() { xs.Close() }()
	connect := func() error {
		if connected {
			return nil
		}
		connected = true
		var err error
		xs, err = buildExits(ctx, u, o)
		return err
	}

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
	for _, input := range inputs {
		input = strings.Trim(input, `"' `)
		if _, ok := asURL(input); !ok {
			if _, err := os.Stat(input); err != nil {
				input = takeout.CleanPath(input) // ~/..., file:// links, My\ File.zip
			}
		}
		if link, ok := asURL(input); ok {
			input = link
			// A single video needs no lookup here: its id is in the link, and the download
			// looks it up anyway. (Each lookup is a yt-dlp run through the VPN, one after
			// another, so 40 pasted links used to take minutes before anything started.)
			if id, ok := singleVideoID(link); ok {
				add([]model.Track{{ID: id}})
				continue
			}
			var err error
			if tools.YtDlp == "" {
				if tools, err = deps.Ensure(ctx, logf); err != nil {
					return err
				}
			}
			if err := connect(); err != nil {
				return err
			}
			proxy := ""
			if len(xs.Exits) > 0 {
				proxy = xs.Exits[0].Proxy
			}
			ts, err := engine.ExpandURL(ctx, tools, proxy, input)
			if err != nil {
				return err
			}
			add(ts)
		} else if _, err := os.Stat(input); err != nil {
			if id, ok := takeout.ExtractID(input); ok && len(input) == 11 {
				add([]model.Track{{ID: id}})
				continue
			}
			return fmt.Errorf("%q is not a YouTube link or an existing file.\nPaste a video/playlist link (in quotes, since & and ? confuse shells) or the path to your Takeout .zip", input)
		} else {
			ts, err := takeout.Load(input, o.all)
			if err != nil {
				return err
			}
			fromExport = true
			add(ts)
		}
	}
	u.Printf("Found %s", takeout.Describe(tracks))

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
	// Completing albums is for a library import (a pasted song link shouldn't quietly bring
	// its whole album), unless --albums was given.
	if o.albums && (fromExport || o.albumsAsked) {
		var err error
		if tools.YtDlp == "" {
			if tools, err = deps.Ensure(ctx, logf); err != nil {
				return err
			}
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
		u.Printf("0 already downloaded, %d to go -> %s (%s)", len(tracks), o.out, o.format)
		for _, t := range tracks {
			u.Printf("  %s  %s", t.ID, t.Label())
		}
		return nil
	}
	numberFromAlbums(st, tracks)
	if n, _ := st.ImportDisk(o.out, idFromTags(ctx, logf)); n > 0 {
		u.Printf("Recognised %d files already in %s", n, o.out)
	}
	plan, err := st.Plan(tracks, o.retry)
	if err != nil {
		return err
	}
	u.Printf("%d already downloaded, %d to go -> %s (%s)", plan.Done, len(plan.Todo), o.out, o.format)
	if plan.Skipped > 0 {
		u.Printf("%d tracks were unavailable last time and are skipped (use --retry to try again)", plan.Skipped)
	}
	if n := len(plan.MusicFirst); n > 0 {
		u.Printf("%d tracks that were unavailable last time will be looked for on YouTube Music", n)
	}
	if o.dryRun {
		for _, t := range plan.Todo {
			u.Printf("  %s  %s", t.ID, t.Label())
		}
		return nil
	}
	began := time.Now()
	if len(plan.Todo) == 0 {
		return afterDownload(ctx, u, o, st, began)
	}

	if tools.YtDlp == "" {
		if tools, err = deps.Ensure(ctx, logf); err != nil {
			return err
		}
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

	u.Printf("\n%d downloaded, %d failed, %s in %s", sum.Done, sum.Failed, engine.HumanBytes(sum.Bytes), sum.Elapsed.Round(time.Second))
	if os.Getenv("YTM_DEBUG") != "" { // per-exit detail is for debugging, not everyday output
		if sum.Swaps > 0 {
			u.Printf("%d exit(s) were blocked and replaced with other locations", sum.Swaps)
		}
		for _, x := range sum.PerExit {
			u.Printf("  %-24s %4d tracks  %s", x.Name, x.Done, engine.HumanBytes(x.Bytes))
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
			u.Printf("%d unavailable track(s) are other versions of songs you already have", dups)
		}
		sort.Slice(fails, func(i, j int) bool { return fails[i].Error < fails[j].Error })
		u.Printf("Not downloaded (%d); run ytm again to retry the temporary ones:", len(fails))
		for i, f := range fails {
			if i == 15 {
				u.Printf("  ...and %d more", len(fails)-i)
				break
			}
			name := f.Title
			if name == "" {
				name = f.ID
			}
			u.Printf("  %s: %s", name, f.Error)
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
	if o.tagAfter {
		if files := st.DoneSince(since); len(files) > 0 {
			u.Printf("\nFixing the tags of %d new song(s) with MusicBrainz (turn off in settings)...", len(files))
			t := o
			t.apply, t.rename, t.undo, t.offline = true, false, false, false
			if err := runTag(ctx, u, t, o.out, files...); err != nil {
				if ctx.Err() != nil {
					return context.Canceled
				}
				u.Printf("tags: %v", err)
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

// buildExits connects the Proton VPN tunnels when you're signed in (unless --no-vpn).
// It never silently falls back to your own IP: if the VPN was meant to be on and can't
// connect, it stops.
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
// Downloads start as soon as the first two are up; the rest join while it runs. It never
// silently falls back to your own IP: if the VPN was meant to be on and can't connect,
// it stops.
// connections is how many VPN exits to open: the saved number (3 when unset), or with
// "auto" (-1) the Proton plan's device limit minus one kept free for a spare. Auto is not
// the default: in a 120-song test 10 exits were no faster than 6 (2026-11-04).
func connections(cfg config.Config, pc *proton.Client) int {
	if cfg.Connections > 0 {
		return cfg.Connections
	}
	if cfg.Connections == 0 {
		return 3
	}
	account := ""
	if pc.Session != nil {
		account = vpn.AccountID(pc.Session.Username)
	}
	if _, limit := vpn.LoadCache(vpn.DefaultCachePath(), account).Servers(); limit > 1 {
		return min(limit-1, maxConns)
	}
	return 3
}

// connectionsLabel is connections for display: "auto (10)" or the saved number.
func connectionsLabel(cfg config.Config, pc *proton.Client) string {
	if cfg.Connections >= 0 {
		return fmt.Sprint(connections(cfg, pc))
	}
	return fmt.Sprintf("auto (%d)", connections(cfg, pc))
}

func buildExits(ctx context.Context, u *ui.UI, o opts) (exitSet, error) {
	none := exitSet{Close: func() {}}
	cfg := config.Load()
	pc := protonClient()
	if o.noVPN || cfg.NoVPN || !pc.SignedIn() {
		if !o.noVPN && !cfg.NoVPN {
			u.Printf("(no VPN: run 'ytm setup' to sign in to Proton and download faster)")
		}
		return none, nil
	}
	n := connections(cfg, pc)
	if o.conns > 0 {
		n = min(o.conns, maxConns)
	}
	u.Status("Connecting to Proton VPN (%d servers near you)...", n)
	debug := os.Getenv("YTM_DEBUG") != ""
	vpnLog := func(f string, a ...any) {
		// Each server connecting (or failing to) is detail; anything else is worth saying.
		if s := fmt.Sprintf(f, a...); debug || !(strings.HasPrefix(s, "  ✓ ") || strings.HasPrefix(s, "  ✗ ")) {
			u.Printf("%s", s)
		}
	}
	var countries []string
	if c := strings.ToUpper(strings.TrimSpace(cfg.Countries)); c != "" {
		countries = []string{c}
	}
	late := make(chan vpn.Exit, 64)
	done := make(chan struct{})
	pool, err := vpn.OpenWith(ctx, pc, n, vpnLog, vpn.Options{
		MinReady:  min(n, 2),
		Countries: countries,
		OnExit: func(e vpn.Exit) {
			select {
			case late <- e:
			case <-done:
			case <-ctx.Done():
			}
		},
	})
	u.ClearStatus()
	if err != nil {
		return none, fmt.Errorf("%w (use --no-vpn to download without it)", err)
	}
	u.Printf("Connected through Proton VPN (%d servers so far; more join while it runs)", len(pool.Exits()))
	var once sync.Once
	return exitSet{
		Exits: pool.Exits(), Replace: pool.Replace, Healthy: pool.Healthy,
		Blocked: pool.MarkBlocked, Good: pool.MarkGood, New: late,
		Close: func() { once.Do(func() { close(done); pool.Close() }) },
	}, nil
}
