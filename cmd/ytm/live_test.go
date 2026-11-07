package main

import (
	"context"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// A live library arrives as preset tracks and goes through the same download path as an
// export. A dry run with no database must plan it without touching the network.
func TestPresetTracksAreDownloadedLikeAnExport(t *testing.T) {
	sandboxConfig(t)
	o, _, err := parse("ytm", []string{"--dry-run", "--albums=false", "-o", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	o.preset = []model.Track{
		{ID: "aaaaaaaaaaa", Title: "One", Artists: []string{"A"}},
		{ID: "bbbbbbbbbbb", Title: "Two", Artists: []string{"B"}},
	}
	if err := download(context.Background(), ui.New(), o); err != nil {
		t.Fatal(err)
	}
}
