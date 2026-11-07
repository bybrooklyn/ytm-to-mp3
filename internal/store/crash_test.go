package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

// The helper process writes as a download run would (plan, mark done, library puts) until
// it is killed.
func TestMain(m *testing.M) {
	if dir := appname.Getenv("STORE_CRASH_HELPER"); dir != "" {
		crashHelper(dir)
		return
	}
	os.Exit(m.Run())
}

func crashID(i int) string { return fmt.Sprintf("%011d", i) }

func crashHelper(dir string) {
	st, err := Open(dir)
	if err != nil {
		os.Exit(2)
	}
	var tracks []model.Track
	for i := 0; i < 5000; i++ {
		tracks = append(tracks, model.Track{ID: crashID(i), Title: fmt.Sprint("t", i)})
	}
	if _, err := st.Plan(tracks, false); err != nil {
		os.Exit(3)
	}
	for i := 0; ; i++ {
		id := crashID(i % 5000)
		if err := st.MarkDone(id, "/m/"+id+".opus"); err != nil {
			os.Exit(4)
		}
		if err := st.LibraryPut("k"+id, "t", "a", "youtube", 1, "/m/"+id+".opus"); err != nil {
			os.Exit(5)
		}
		if i == 50 {
			os.WriteFile(filepath.Join(dir, "..", "ready"), []byte("1"), 0o644)
		}
	}
}

// kill -9 in the middle of a run leaves a database that opens, passes SQLite's own
// integrity check, has whole rows only, and goes on working: nothing is corrupt and the
// next run continues from what was recorded.
func TestKill9LeavesConsistentState(t *testing.T) {
	for round := 0; round < 3; round++ {
		base := t.TempDir()
		dir := appname.StateDir(base)
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "MFY_STORE_CRASH_HELPER="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(base, "ready")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatal("helper never got going")
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(time.Duration(round*7) * time.Millisecond) // different moments of the write cycle
		_ = cmd.Process.Kill()                                // SIGKILL: no cleanup runs
		_ = cmd.Wait()

		st, err := Open(dir)
		if err != nil {
			t.Fatalf("round %d: reopen after kill -9: %v", round, err)
		}
		var res string
		if err := st.db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
			t.Fatalf("round %d: integrity_check = %q, %v", round, res, err)
		}
		var bad int
		st.db.QueryRow(`SELECT count(*) FROM tracks WHERE status='done' AND path=''`).Scan(&bad)
		if bad != 0 {
			t.Errorf("round %d: %d done tracks without a path", round, bad)
		}
		// A song marked done in the library has its path too.
		st.db.QueryRow(`SELECT count(*) FROM library_tracks WHERE path=''`).Scan(&bad)
		if bad != 0 {
			t.Errorf("round %d: %d library rows without a path", round, bad)
		}
		var done int
		st.db.QueryRow(`SELECT count(*) FROM tracks WHERE status='done'`).Scan(&done)
		if done == 0 {
			t.Errorf("round %d: nothing recorded survived", round)
		}
		// The next run continues: a plan skips what is done and a write works.
		var all []model.Track
		for i := 0; i < 5000; i++ {
			all = append(all, model.Track{ID: crashID(i)})
		}
		p, err := st.Plan(all, false)
		if err != nil || p.Done != done || len(p.Todo) != 5000-done {
			t.Errorf("round %d: plan after crash: err=%v done=%d/%d todo=%d", round, err, p.Done, done, len(p.Todo))
		}
		if err := st.MarkDone(crashID(4999), "/m/x.opus"); err != nil {
			t.Errorf("round %d: write after crash: %v", round, err)
		}
		st.Close()
	}
}

// Two connections writing at once (two stages of one run each open the database) wait for
// each other instead of failing with "database is locked".
func TestConcurrentOpensDoNotLockOut(t *testing.T) {
	dir := appname.StateDir(t.TempDir())
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	errs := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		go func() {
			for i := 0; i < 300; i++ {
				if err := s.LibraryPut(fmt.Sprint("k", i), "t", "a", "youtube", 1, "/p"); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
