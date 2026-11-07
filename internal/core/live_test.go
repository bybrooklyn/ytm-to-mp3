package core

import (
	"context"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

// A live library arrives as preset tracks and goes through the same download path as an
// export. A dry run with no database must plan it without touching the network.
func TestPresetTracksAreDownloadedLikeAnExport(t *testing.T) {
	sandboxConfig(t)
	o := Options{
		Out: t.TempDir(), Format: "mp3", DryRun: true, NoVPN: true,
		Preset: []model.Track{
			{ID: "aaaaaaaaaaa", Title: "One", Artists: []string{"A"}},
			{ID: "bbbbbbbbbbb", Title: "Two", Artists: []string{"B"}},
		},
	}
	if _, err := Download(context.Background(), nil, o, Deps{}); err != nil {
		t.Fatal(err)
	}
}
