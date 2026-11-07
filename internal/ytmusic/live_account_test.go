package ytmusic

import (
	"context"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
)

// Reads the real library of whichever installed browser is signed in to YouTube Music
// (most recently used first). It prints counts only, never cookies or account details.
// Run with: MFY_LIVE_ACCOUNT=1 go test ./internal/ytmusic -run LiveAccount -v
func TestLiveAccountLibrary(t *testing.T) {
	if appname.Getenv("LIVE_ACCOUNT") == "" {
		t.Skip("set MFY_LIVE_ACCOUNT=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, b := range browsercookies.Detect() {
		cs, err := b.Import()
		if err != nil {
			t.Logf("%s: %v", b.Name, err)
			continue
		}
		c, err := New(nil, cs)
		if err != nil {
			t.Logf("%s: %v", b.Name, err)
			continue
		}
		start := time.Now()
		lib, err := c.Library(ctx, Options{Albums: true, Playlists: true})
		if err != nil {
			t.Logf("%s: library read failed: %v", b.Name, err)
			continue
		}
		t.Logf("%s: %d songs (liked %d, saved %d), %d albums, %d playlists, %d unreadable, in %v",
			b.Name, len(lib.Tracks), lib.Liked, lib.Saved, lib.Albums, lib.Playlists, lib.Skipped, time.Since(start).Round(time.Second))
		withAlbum := 0
		for _, tr := range lib.Tracks {
			if tr.Album != "" {
				withAlbum++
			}
		}
		t.Logf("%d of them carry an album name; first song's fields present: %v", withAlbum, len(lib.Tracks) > 0 && lib.Tracks[0].Title != "" && lib.Tracks[0].ID != "")
		return
	}
	t.Fatal("no installed browser gave a working YouTube Music sign-in")
}
