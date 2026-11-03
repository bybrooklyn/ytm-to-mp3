// Command ytm downloads a YouTube Music library export as tagged audio files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/cookies"
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

const usage = `ytm - download your YouTube Music library as tagged audio files

Usage:
  ytm                         guided: find or fetch your export, then download it
  ytm <input>...              download; each <input> is a video/playlist/album URL,
                              a Takeout .zip, folder or .csv, or a text file of links
  ytm export                  walk through getting your export from Google Takeout
  ytm tag <folder>            clean up and improve metadata (preview; --apply writes)
  ytm update                  refresh yt-dlp
  ytm vpn add <file.conf>...  save WireGuard configs (any provider) for --vpn
  ytm vpn list|test|clean     show saved configs / bring them up and show each exit IP / remove stale namespaces

Options:
  -o DIR       output folder (default ./Music)
  -f FORMAT    mp3 (default), m4a, opus, flac
  -j N         max parallel downloads (default: adapts automatically)
  --no-cookies don't use browser cookies
  --dry-run    show what would happen
  --retry      also retry tracks that were unavailable before
  --all        include ID-only playlists from a Takeout export
  -y           assume yes
  --vpn        spread downloads over all saved WireGuard configs (Linux, needs sudo);
               each config runs in its own network namespace, only ytm uses it
  --vpn-conf P WireGuard .conf files/folders (comma separated) instead of the saved ones
  --proxy URL  extra exits: socks5://host:port or http://host:port (comma separated)
  --direct     also use your normal connection as an exit when VPNs/proxies are given
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
	noCookies bool
	dryRun    bool
	retry     bool
	all       bool
	yes       bool
	apply     bool
	rename    bool
	undo      bool
	offline   bool
	vpn       bool
	vpnConf   string
	proxy     string
	direct    bool
	socket    string
}

func parse(name string, args []string) (opts, []string, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.StringVar(&o.out, "o", "Music", "")
	fs.StringVar(&o.format, "f", "mp3", "")
	fs.IntVar(&o.jobs, "j", 0, "")
	fs.BoolVar(&o.noCookies, "no-cookies", false, "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.retry, "retry", false, "")
	fs.BoolVar(&o.all, "all", false, "")
	fs.BoolVar(&o.yes, "y", false, "")
	fs.BoolVar(&o.apply, "apply", false, "")
	fs.BoolVar(&o.rename, "rename", false, "")
	fs.BoolVar(&o.undo, "undo", false, "")
	fs.BoolVar(&o.offline, "offline", false, "")
	fs.BoolVar(&o.vpn, "vpn", false, "")
	fs.StringVar(&o.vpnConf, "vpn-conf", "", "")
	fs.StringVar(&o.proxy, "proxy", "", "")
	fs.BoolVar(&o.direct, "direct", false, "")
	fs.StringVar(&o.socket, "socket", "", "")
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
	if !engine.ValidFormat(o.format) {
		return o, nil, fmt.Errorf("unknown format %q (use %s)", o.format, strings.Join(engine.Formats, ", "))
	}
	return o, pos, nil
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "export", "tag", "update", "version", "help", "vpn", "vpn-proxy":
			cmd, args = args[0], args[1:]
		}
	}
	o, pos, err := parse("ytm", args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if cmd == "vpn-proxy" {
		return vpn.ProxyMain(o.socket)
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
	case "vpn":
		return runVPN(ctx, u, o, pos)
	case "tag":
		if len(pos) != 1 {
			return errors.New("usage: ytm tag <folder> [--apply] [--rename] [--undo] [--offline] [-y]")
		}
		return runTag(ctx, u, o, pos[0])
	}

	switch len(pos) {
	case 0:
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
	if p, ok := findExisting(); ok {
		if u.Confirm("Found your export at "+p+"\nDownload it?", true) {
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

func download(ctx context.Context, u *ui.UI, o opts, inputs ...string) error {
	logf := func(f string, a ...any) { u.Printf(f, a...) }
	var tools deps.Tools
	var jar cookies.Result
	cfg := config.Load()

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
		if isURL(input) {
			var err error
			if tools.YtDlp == "" {
				if tools, err = deps.Ensure(ctx, logf); err != nil {
					return err
				}
				jar = cookies.Setup(ctx, u, &cfg, tools, o.noCookies)
			}
			ts, err := engine.ExpandURL(ctx, tools, jar.File, input)
			if err != nil {
				return err
			}
			add(ts)
		} else if _, err := os.Stat(input); err != nil {
			if id, ok := takeout.ExtractID(input); ok && len(input) == 11 {
				add([]model.Track{{ID: id}})
				continue
			}
			return err
		} else {
			ts, err := takeout.Load(input, o.all)
			if err != nil {
				return err
			}
			add(ts)
		}
	}
	u.Printf("Found %s", takeout.Describe(tracks))

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
		jar = cookies.Setup(ctx, u, &cfg, tools, o.noCookies)
	}
	exits, closeExits, err := buildExits(ctx, u, o)
	if err != nil {
		return err
	}
	defer closeExits()
	cache, _ := config.CacheDir()
	sum := engine.Run(ctx, engine.Options{
		Out: o.out, Format: o.format, Jobs: o.jobs, Cookies: jar.File, Tools: tools,
		Store: st, UI: u, CacheDir: filepath.Join(cache, "yt-dlp"), Exits: exits,
	}, plan.Todo)

	u.Printf("\n%d downloaded, %d failed, %s in %s", sum.Done, sum.Failed, engine.HumanBytes(sum.Bytes), sum.Elapsed.Round(time.Second))
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

// buildExits turns --vpn / --vpn-conf / --proxy into the exits the engine spreads over.
func buildExits(ctx context.Context, u *ui.UI, o opts) ([]vpn.Exit, func(), error) {
	noop := func() {}
	var exits []vpn.Exit
	closer := noop
	if o.proxy != "" {
		px, err := vpn.Proxies([]string{o.proxy})
		if err != nil {
			return nil, noop, err
		}
		exits = append(exits, px...)
	}
	if o.vpn || o.vpnConf != "" {
		var paths []string
		if o.vpnConf != "" {
			paths = []string{o.vpnConf}
		}
		files, err := vpn.ConfFiles(paths)
		if err != nil {
			return nil, noop, err
		}
		u.Printf("Bringing up %d VPN tunnel(s)...", len(files))
		pool, err := vpn.Up(ctx, files, func(f string, a ...any) { u.Printf(f, a...) })
		if err != nil {
			return nil, noop, err
		}
		closer = pool.Close
		exits = append(exits, pool.Exits...)
	}
	if len(exits) == 0 {
		return nil, noop, nil
	}
	if o.direct {
		exits = append(exits, vpn.Direct)
	}
	u.Printf("Spreading downloads over %d exit(s)", len(exits))
	return exits, closer, nil
}

func runVPN(ctx context.Context, u *ui.UI, o opts, pos []string) error {
	sub := ""
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	switch sub {
	case "add":
		if len(pos) == 0 {
			return errors.New("usage: ytm vpn add <file.conf>...")
		}
		added, err := vpn.Add(pos)
		for _, a := range added {
			u.Printf("saved %s", a)
		}
		return err
	case "list":
		files, err := vpn.ConfFiles(nil)
		if err != nil {
			return err
		}
		for _, f := range files {
			u.Printf("%s", f)
		}
		return nil
	case "clean":
		vpn.Clean(func(f string, a ...any) { u.Printf(f, a...) })
		return nil
	case "test":
		var paths []string
		if o.vpnConf != "" {
			paths = []string{o.vpnConf}
		}
		files, err := vpn.ConfFiles(paths)
		if err != nil {
			return err
		}
		pool, err := vpn.Up(ctx, files, func(f string, a ...any) { u.Printf(f, a...) })
		if err != nil {
			return err
		}
		defer pool.Close()
		for _, x := range pool.Exits {
			c, _ := vpn.NewClient(x.Proxy)
			c.Timeout = 20 * time.Second
			ip := "?"
			if resp, err := c.Get("https://api.ipify.org"); err == nil {
				b := make([]byte, 64)
				n, _ := resp.Body.Read(b)
				resp.Body.Close()
				ip = strings.TrimSpace(string(b[:n]))
			}
			u.Printf("  %-24s exit IP %s", x.Name, ip)
		}
		return nil
	}
	return errors.New("usage: ytm vpn add|list|test|clean")
}
