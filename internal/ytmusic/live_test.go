package ytmusic

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live checks against the real YouTube Music (anonymous: no account, public searches).
// Run with YTM_LIVE=1 go test ./internal/ytmusic -run Live -v
func TestLiveSearchAlbumPlaylist(t *testing.T) {
	if os.Getenv("YTM_LIVE") == "" {
		t.Skip("set YTM_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := NewAnonymous(nil)

	songs, err := c.SearchSongs(ctx, "Daft Punk Get Lucky", 3)
	if err != nil || len(songs) == 0 {
		t.Fatalf("songs: %v %v", songs, err)
	}
	t.Logf("songs: %+v", songs)

	albums, err := c.SearchAlbums(ctx, "Daft Punk Random Access Memories", 3)
	if err != nil || len(albums) == 0 {
		t.Fatalf("albums: %v %v", albums, err)
	}
	t.Logf("albums: %+v", albums)

	a, err := c.Album(ctx, albums[0].BrowseID)
	if err != nil {
		t.Fatalf("album: %v", err)
	}
	t.Logf("album %q year=%d tracks=%d thumbs=%d first=%+v", a.Title, a.Year, len(a.Tracks), len(a.Thumbs), a.Tracks[0])
	if len(a.Tracks) < 5 {
		t.Fatalf("too few tracks: %+v", a.Tracks)
	}
}
