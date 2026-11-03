// Command ytm downloads a YouTube Music library export as tagged audio files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
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
  --dry-run    show what would happen
  --retry      retry tracks that were unavailable before
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
		o.format = orDefault(cfg.Format, "mp3")
	}
	if !engine.ValidFormat(o.format) {
		return o, nil, fmt.Errorf("unknown format %q (use %s)", o.format, strings.Join(engine.Formats, ", "))
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
	ctx, stop := signalContext()
	defer stop()
	u := ui.New()

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
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		fmt.Fprintln(os.Stderr, "\nStopping... finishing what's in flight (Ctrl-C again to quit now).")
		cancel()
		<-ch
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
			add(ts)
		}
	}
	u.Printf("Found %s", takeout.Describe(tracks))

	if o.dryRun {
		if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr != nil {
			// Nothing downloaded here yet and a dry run must not create anything.
			u.Printf("0 already downloaded, %d to go -> %s (%s)", len(tracks), o.out, o.format)
			for _, t := range tracks {
				u.Printf("  %s  %s", t.ID, t.Label())
			}
			return nil
		}
	}
	st, err := store.Open(filepath.Join(o.out, ".ytm"))
	if err != nil {
		return err
	}
	defer st.Close()
	if n, _ := st.ImportDisk(o.out); n > 0 {
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
	if len(plan.Todo) == 0 {
		return nil
	}
	if o.dryRun {
		for _, t := range plan.Todo {
			u.Printf("  %s  %s", t.ID, t.Label())
		}
		return nil
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
		OnBlocked: xs.Blocked, OnGood: xs.Good, NewExits: xs.New,
	}, plan.Todo)

	u.Printf("\n%d downloaded, %d failed, %s in %s", sum.Done, sum.Failed, engine.HumanBytes(sum.Bytes), sum.Elapsed.Round(time.Second))
	if sum.Swaps > 0 {
		u.Printf("%d exit(s) were blocked and replaced with other locations", sum.Swaps)
	}
	if len(sum.PerExit) > 1 {
		for _, x := range sum.PerExit {
			u.Printf("  %-24s %4d tracks  %s", x.Name, x.Done, engine.HumanBytes(x.Bytes))
		}
	}
	if fails, _ := st.Failures(); len(fails) > 0 {
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
	return nil
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
	n := orInt(cfg.Connections, 3)
	u.Printf("Connecting %d VPN tunnel(s)...", n)
	var countries []string
	if c := strings.ToUpper(strings.TrimSpace(cfg.Countries)); c != "" {
		countries = []string{c}
	}
	late := make(chan vpn.Exit, 64)
	done := make(chan struct{})
	pool, err := vpn.OpenWith(ctx, pc, n, func(f string, a ...any) { u.Printf(f, a...) }, vpn.Options{
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
	if err != nil {
		return none, fmt.Errorf("%w (use --no-vpn to download without it)", err)
	}
	var once sync.Once
	return exitSet{
		Exits: pool.Exits(), Replace: pool.Replace, Healthy: pool.Healthy,
		Blocked: pool.MarkBlocked, Good: pool.MarkGood, New: late,
		Close: func() { once.Do(func() { close(done); pool.Close() }) },
	}, nil
}
