package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// Songs that finish downloading are handed to the tag/lyrics step while later songs are
// still downloading; every song is handled exactly once; and the whole thing finishes
// sooner than "download everything, then post-process everything".
func TestPostRunnerOverlapsDownloads(t *testing.T) {
	old := postTick
	postTick = 15 * time.Millisecond
	defer func() { postTick = old }()

	const n = 60
	const perSongDownload = 20 * time.Millisecond
	const perSongPost = 15 * time.Millisecond

	st, err := store.Open(appname.StateDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var tracks []model.Track
	for i := 0; i < n; i++ {
		tracks = append(tracks, model.Track{ID: fmt.Sprintf("%011d", i), Title: fmt.Sprint("t", i)})
	}
	if _, err := st.Plan(tracks, false); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	handled := map[string]int{}
	var batches int
	hook := func(files []string) error {
		mu.Lock()
		batches++
		for _, f := range files {
			handled[f]++
		}
		mu.Unlock()
		time.Sleep(time.Duration(len(files)) * perSongPost)
		return nil
	}
	began := time.Now()
	pr := startPost(context.Background(), ui.New(), opts{}, st, began.Add(-time.Second), hook)
	for i := 0; i < n; i++ { // the downloads
		time.Sleep(perSongDownload)
		id := tracks[i].ID
		if err := st.MarkDone(id, "/m/"+id+".opus"); err != nil {
			t.Fatal(err)
		}
	}
	if err := pr.Finish(); err != nil {
		t.Fatal(err)
	}
	overlapped := time.Since(began)
	serial := n*perSongDownload + n*perSongPost

	if len(handled) != n {
		t.Fatalf("handled %d songs, want %d", len(handled), n)
	}
	for f, c := range handled {
		if c != 1 {
			t.Errorf("%s handled %d times", f, c)
		}
	}
	if batches < 2 {
		t.Errorf("everything was left to the end (%d batch)", batches)
	}
	t.Logf("%d songs: download %v + post %v. one after the other %v, overlapped %v (saves %v)", n,
		n*perSongDownload, n*perSongPost, serial.Round(time.Millisecond), overlapped.Round(time.Millisecond),
		(serial - overlapped).Round(time.Millisecond))
	if overlapped > serial-200*time.Millisecond {
		t.Errorf("overlapped %v is not meaningfully faster than serial %v", overlapped, serial)
	}
}
