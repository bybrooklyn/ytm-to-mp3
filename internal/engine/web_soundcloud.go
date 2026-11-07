package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/soundcloud"
	"github.com/bybrooklyn/mfetchy/internal/vpn"
)

// nativeSoundCloud reports whether SoundCloud links try mfetchy's own client first. yt-dlp is
// the fallback whenever the native path errors or finds nothing, so a change on
// SoundCloud's side costs speed, not results. MFY_SOUNDCLOUD=yt-dlp turns it off.
func nativeSoundCloud() bool { return !strings.EqualFold(appname.Getenv("SOUNDCLOUD"), "yt-dlp") }

// scClients keeps one client per exit, so its client_id is found once per session.
var (
	scMu      sync.Mutex
	scClients = map[string]*soundcloud.Client{}
	// scFactory builds the client for an exit; tests replace it.
	scFactory = func(proxy string) (*soundcloud.Client, error) {
		hc, err := vpn.NewClient(proxy)
		if err != nil {
			return nil, err
		}
		return soundcloud.New(hc), nil
	}
)

func scClient(proxy string) (*soundcloud.Client, error) {
	scMu.Lock()
	defer scMu.Unlock()
	if c, ok := scClients[proxy]; ok {
		return c, nil
	}
	c, err := scFactory(proxy)
	if err != nil {
		return nil, err
	}
	scClients[proxy] = c
	return c, nil
}

// nativeListSoundCloud is ListWeb for SoundCloud without yt-dlp. ok=false means "ask yt-dlp".
func nativeListSoundCloud(ctx context.Context, proxy, link string) ([]WebTrack, bool) {
	if !nativeSoundCloud() {
		return nil, false
	}
	c, err := scClient(proxy)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*nativeTimeout)
	defer cancel()
	ts, err := c.Resolve(ctx, link)
	if err != nil || len(ts) == 0 {
		return nil, false
	}
	out := make([]WebTrack, 0, len(ts))
	for _, t := range ts {
		out = append(out, WebTrack{
			Site: "soundcloud", URL: t.URL, ID: strconv.FormatInt(t.ID, 10), Title: t.Title, Artist: t.Artist,
			Album: t.Album, AlbumArtist: t.AlbumArtist, Genre: t.Genre, Date: t.Date,
			TrackNo: t.TrackNo, TrackTotal: t.TrackTotal, Duration: t.Duration, Cover: t.Cover,
			ISRC: t.ISRC, Downloadable: t.Downloadable, Policy: t.Policy,
		})
	}
	return out, true
}

// nativeFetchSoundCloud is FetchWeb for SoundCloud without yt-dlp. ok=false means "ask
// yt-dlp" (any error, or a format the native path leaves alone).
func nativeFetchSoundCloud(ctx context.Context, proxy string, t WebTrack, dir string, onProgress func(got, total int64)) (string, bool) {
	if !nativeSoundCloud() {
		return "", false
	}
	c, err := scClient(proxy)
	if err != nil {
		return "", false
	}
	sum := sha1.Sum([]byte(t.URL))
	base := filepath.Join(dir, "web-"+hex.EncodeToString(sum[:6]))
	path, err := c.Fetch(ctx, soundcloud.Track{URL: t.URL}, base, onProgress)
	if err != nil {
		return "", false
	}
	return path, true
}
