// Command mfy (mfetchy) downloads a music library as tagged audio files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

var version = "dev"

const usage = `mfetchy - your music library from YouTube Music, Apple Music and Spotify,
downloaded as tagged audio through Proton VPN

  mfy                     open mfetchy: downloads, your library, settings and tools in one place
  mfy <link or file>...   download right away: YouTube, Apple Music, Spotify, SoundCloud or Bandcamp links (songs,
                          albums, playlists), a Takeout .zip, folder or .csv, or a text file
                          of links. Each song comes from the best source you have: Apple Music
                          (256 kbps AAC) when you're signed in there and it has it, else YouTube.

Options for one run (your defaults are in mfetchy's Settings):
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
			// "  ✗ what went wrong" then any further lines as dim hints, like mfetchy's other lines
			first, rest, _ := strings.Cut(msg, "\n")
			fmt.Fprintln(os.Stderr, ui.Mark(ui.Fail("✗"), first))
			for _, l := range strings.Split(rest, "\n") {
				if l != "" {
					fmt.Fprintln(os.Stderr, "      "+ui.Dim(l))
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "mfy:", msg)
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
	// mfy apple match: replace the YouTube copies; also take the less certain matches
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
	fs.SetOutput(io.Discard) // errors are printed once by main, with a pointer to 'mfy --help'
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
		fmt.Fprintln(os.Stderr, "mfy: warning:", cerr)
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

// movedInto says where each old subcommand lives now: everything is in mfetchy itself.
var movedInto = map[string]string{
	"setup":  "Settings",
	"login":  "Settings (VPN account)",
	"logout": "Settings (VPN account)",
	"status": "Settings (Test VPN)",
	"doctor": "More tools > Check that everything works",
	"export": "\"Download my YouTube Music library\" (it helps you get the export)",
	"tag":    "More tools > Fix tags",
	"update": "More tools > Update yt-dlp, ffmpeg and deno",
	"apple":  "Apple Music... (links just work: mfy <link>)",
}

func run(args []string) error {
	o, pos, err := parse("mfy", args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return nil
		}
		return fmt.Errorf("%w (see: mfy --help)", err)
	}
	switch {
	case o.version || (len(pos) == 1 && pos[0] == "version"):
		fmt.Println("mfy", version)
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
	// The old subcommands: "mfy apple <link>" / "mfetchy spotify <link>" are just links now; the
	// rest say where they went (unless a file of that name is meant).
	if len(pos) > 0 {
		if _, err := os.Stat(pos[0]); err != nil {
			if (pos[0] == "apple" || pos[0] == "spotify") && len(pos) > 1 {
				pos = pos[1:]
			} else if where, ok := movedInto[pos[0]]; ok {
				return fmt.Errorf("'mfy %s' is now part of mfetchy itself: run mfy and pick %s", pos[0], where)
			} else if pos[0] == "completion" {
				return errors.New("use: mfy --completion bash|zsh|fish|powershell")
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
	// the plain command-line prompter: this runs when there is no menu to show
	p, err := core.Wizard(ctx, u, core.TerminalPrompter{U: u})
	if err != nil && !u.Interactive() {
		printUsage()
	}
	return p, err
}
