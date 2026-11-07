package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

// MFY_LIVE=1 go test ./internal/engine -run NativeLive -v
func TestNativeLiveFindAlbumAndSearch(t *testing.T) {
	if appname.Getenv("LIVE") == "" {
		t.Skip("set MFY_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// No yt-dlp configured: any fallback would fail loudly, so this only passes natively.
	y := ytdlp{tools: deps.Tools{}}
	saved := []model.Track{{ID: "IluRBvnYMoY", Title: "Give Life Back to Music"}}
	start := time.Now()
	a, err := FindAlbum(ctx, deps.Tools{}, "", "Daft Punk", "Random Access Memories", saved)
	if err != nil || a == nil {
		t.Fatalf("FindAlbum: %v %v", a, err)
	}
	t.Logf("album %q year=%d tracks=%d cover=%s (%v)", a.Title, a.Year, len(a.Tracks), a.Cover, time.Since(start))
	if len(a.Tracks) != 13 || a.Year != 2013 || a.Cover == "" {
		t.Fatalf("unexpected: %+v", a)
	}
	hits, err := y.SearchMusic(ctx, "Daft Punk Get Lucky", 3)
	if err != nil || len(hits) == 0 {
		t.Fatalf("SearchMusic: %v %v", hits, err)
	}
	ts, err := y.Expand(ctx, "https://music.youtube.com/browse/"+"MPREb_K8qWMWVqXGi")
	if err != nil || len(ts) != 13 {
		t.Fatalf("Expand: %d %v", len(ts), err)
	}
}
