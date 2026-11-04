package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

const appleUsage = `ytm apple - download from Apple Music with your subscription (256 kbps AAC)

  ytm apple login            save your Apple Music login (media-user-token)
  ytm apple logout           forget it
  ytm apple status           show whether a login is saved
  ytm apple <link>...        download an Apple Music album, song or playlist
  ytm apple match [file]     find your YouTube library's songs on Apple Music
                             and download them as AAC (uses a Takeout export)

Apple Music audio is lossless only over FairPlay, which needs a separate
decryption backend ytm does not run; this path gives 256 kbps AAC, decrypted
locally with your account. It still goes through your Proton VPN.`

// runApple dispatches the `ytm apple ...` subcommands.
func runApple(ctx context.Context, u *ui.UI, o opts, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "help", "-h", "--help":
		fmt.Println(appleUsage)
		return nil
	case "login":
		return appleLogin(ctx, u)
	case "logout":
		if err := config.SaveAppleToken(""); err != nil {
			return err
		}
		u.Printf("Forgot your Apple Music login.")
		return nil
	case "status":
		return appleStatus(ctx, u)
	case "match":
		return appleMatch(ctx, u, o, args[1:])
	}
	// Otherwise treat every argument as an Apple Music link.
	return appleDownloadLinks(ctx, u, o, args)
}

// appleLogin prompts for the media-user-token and saves it.
func appleLogin(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("ytm apple login needs a terminal")
	}
	u.Printf("Apple Music login")
	u.Printf("Sign in at https://music.apple.com in your browser, then from its cookies")
	u.Printf("copy the value of the cookie named \"media-user-token\" and paste it here.")
	u.Printf("(DevTools > Application > Cookies > https://music.apple.com)")
	token := strings.TrimSpace(u.Ask("media-user-token: ", ""))
	if token == "" {
		return errors.New("no token entered")
	}
	store := strings.ToLower(strings.TrimSpace(u.Ask("Storefront country code [us]: ", "us")))
	if store == "" {
		store = "us"
	}
	if err := config.SaveAppleToken(token); err != nil {
		return err
	}
	cfg := config.Load()
	cfg.AppleStorefront = store
	if err := cfg.Save(); err != nil {
		return err
	}
	u.Printf("Saved. Test it with: ytm apple status")
	return nil
}

// appleStatus reports whether a login is saved and that the catalog is reachable.
func appleStatus(ctx context.Context, u *ui.UI) error {
	token := config.AppleToken()
	if token == "" {
		u.Printf("No Apple Music login saved. Run: ytm apple login")
		return nil
	}
	cfg := config.Load()
	store := cfg.AppleStorefront
	if store == "" {
		store = "us"
	}
	u.Printf("Apple Music login saved (storefront %s).", store)
	if _, err := apple.New(token, store, cfg.AppleLanguage); err != nil {
		return err
	}
	u.Printf("Run a download to confirm the token still works.")
	return nil
}

// appleClient builds a logged-in client and routes it through a Proton exit
// (direct if the VPN is off). It returns the client and a close func.
func appleClient(ctx context.Context, u *ui.UI, o opts) (*apple.Client, func(), error) {
	token := config.AppleToken()
	if token == "" {
		return nil, nil, errors.New("no Apple Music login (run: ytm apple login)")
	}
	cfg := config.Load()
	store := cfg.AppleStorefront
	if store == "" {
		store = "us"
	}
	c, err := apple.New(token, store, cfg.AppleLanguage)
	if err != nil {
		return nil, nil, err
	}
	xs, err := buildExits(ctx, u, o)
	if err != nil {
		return nil, nil, err
	}
	proxy := ""
	if len(xs.Exits) > 0 {
		proxy = xs.Exits[0].Proxy
	} else {
		u.Printf("(no VPN: Apple Music traffic will use your own address)")
	}
	if err := apple.SetProxy(proxy); err != nil {
		xs.Close()
		return nil, nil, err
	}
	return c, xs.Close, nil
}

// appleDownloadLinks resolves and downloads Apple Music links.
func appleDownloadLinks(ctx context.Context, u *ui.UI, o opts, links []string) error {
	for _, l := range links {
		if !apple.IsURL(strings.Trim(l, `"' `)) {
			return fmt.Errorf("%q is not an Apple Music album, song or playlist link", l)
		}
	}
	c, closeFn, err := appleClient(ctx, u, o)
	if err != nil {
		return err
	}
	defer closeFn()

	var tracks []apple.Track
	for _, l := range links {
		ts, err := c.ResolveURL(ctx, strings.Trim(l, `"' `))
		if err != nil {
			u.Printf("  %s: %v", l, err)
			continue
		}
		tracks = append(tracks, ts...)
	}
	if len(tracks) == 0 {
		return errors.New("nothing to download")
	}
	return appleDownloadTracks(ctx, u, o, c, tracks)
}

// appleMatch loads a YouTube library export, matches each song against the
// Apple catalog and downloads the matches as AAC.
func appleMatch(ctx context.Context, u *ui.UI, o opts, args []string) error {
	var path string
	if len(args) > 0 {
		path = strings.Trim(args[0], `"' `)
	} else {
		p, ok := findExisting()
		if !ok {
			return errors.New("give a Takeout export: ytm apple match <takeout.zip>")
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	lib, err := takeout.Load(path, o.all)
	if err != nil {
		return err
	}
	u.Printf("Loaded %s", takeout.Describe(lib))

	c, closeFn, err := appleClient(ctx, u, o)
	if err != nil {
		return err
	}
	defer closeFn()

	var tracks []apple.Track
	var missed int
	for _, t := range lib {
		if t.Title == "" {
			missed++
			continue
		}
		m, ok, err := c.Match(ctx, t.Title, strings.Join(t.Artists, " "), t.Album, 0)
		if err != nil {
			u.Printf("  %s: %v", t.Title, err)
			missed++
			continue
		}
		if !ok {
			missed++
			continue
		}
		tracks = append(tracks, m)
	}
	u.Printf("Matched %d of %d songs on Apple Music (%d not found)", len(tracks), len(lib), missed)
	if len(tracks) == 0 {
		return nil
	}
	return appleDownloadTracks(ctx, u, o, c, tracks)
}

// appleDownloadTracks downloads, tags and files every track under o.out.
func appleDownloadTracks(ctx context.Context, u *ui.UI, o opts, c *apple.Client, tracks []apple.Track) error {
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, func(f string, a ...any) { u.Printf(f, a...) })
	if err != nil {
		return err
	}
	done, failed := 0, 0
	for i, t := range tracks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		u.Printf("[%d/%d] %s - %s", i+1, len(tracks), t.Artist, t.Title)
		if err := appleSaveTrack(ctx, c, ffmpeg, ffprobe, o.out, t); err != nil {
			if errors.Is(err, errAppleExists) {
				done++
				continue
			}
			u.Printf("    failed: %v", err)
			failed++
			continue
		}
		done++
	}
	u.Printf("\n%d downloaded, %d failed -> %s (m4a / AAC 256)", done, failed, o.out)
	return nil
}

var errAppleExists = errors.New("already downloaded")

// appleSaveTrack downloads one track to <out>/<artist>/<album>/<NN title>.m4a,
// embeds cover art and writes tags.
func appleSaveTrack(ctx context.Context, c *apple.Client, ffmpeg, ffprobe, out string, t apple.Track) error {
	artist := t.AlbumArtist
	if artist == "" {
		artist = t.Artist
	}
	dir := filepath.Join(out, safeName(artist), safeName(t.Album))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := safeName(t.Title)
	if t.TrackNumber > 0 {
		base = fmt.Sprintf("%02d %s", t.TrackNumber, base)
	}
	dest := filepath.Join(dir, base+".m4a")
	if _, err := os.Stat(dest); err == nil {
		return errAppleExists
	}
	tmp := dest + ".dl.m4a"
	_ = os.Remove(tmp)
	if err := c.Download(ctx, t, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	cover := ""
	if art, err := c.FetchArtwork(ctx, t, 1200); err == nil && len(art) > 0 {
		cp := dest + ".cover.jpg"
		if werr := os.WriteFile(cp, art, 0o644); werr == nil {
			cover = cp
			defer os.Remove(cp)
		}
	}

	set := map[string]string{
		"title":        t.Title,
		"artist":       t.Artist,
		"album":        t.Album,
		"album_artist": artist,
	}
	if t.TrackNumber > 0 {
		set["track"] = fmt.Sprint(t.TrackNumber)
	}
	if t.DiscNumber > 0 {
		set["disc"] = fmt.Sprint(t.DiscNumber)
	}
	if t.ReleaseDate != "" {
		set["date"] = t.ReleaseDate
	}
	if err := tag.Write(ctx, ffmpeg, ffprobe, tmp, set, cover); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tag: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

var unsafeNameChars = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)

// safeName makes s a safe single path component.
func safeName(s string) string {
	s = unsafeNameChars.ReplaceAllString(s, "_")
	s = strings.Trim(strings.TrimSpace(s), ".")
	if s == "" {
		return "Unknown"
	}
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}
