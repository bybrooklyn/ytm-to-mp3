package tag

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
)

type journalEntry struct {
	Batch       string            `json:"batch"`
	Path        string            `json:"path"`
	NewPath     string            `json:"new_path,omitempty"`
	Old         map[string]string `json:"old"`
	CoverBackup string            `json:"cover_backup,omitempty"`
}

func journalPath(dir string) string { return filepath.Join(dir, ".ytm", "tag-journal.jsonl") }

func appendJournal(dir string, e journalEntry) error {
	_ = store.PrivateDir(filepath.Dir(journalPath(dir)))
	f, err := os.OpenFile(journalPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

// writeJournal replaces the whole journal atomically (temp file + rename), so a crash
// never leaves a truncated journal behind.
func writeJournal(dir string, entries []journalEntry) error {
	jp := journalPath(dir)
	if err := store.PrivateDir(filepath.Dir(jp)); err != nil {
		return err
	}
	tf, err := os.CreateTemp(filepath.Dir(jp), "tag-journal-*.tmp") // 0600, O_EXCL
	if err != nil {
		return err
	}
	tmp := tf.Name()
	defer os.Remove(tmp)
	w := bufio.NewWriter(tf)
	for _, e := range entries {
		b, _ := json.Marshal(e)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Sync(); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	return store.Replace(tmp, jp)
}

// coverBackupName is a collision-resistant file name for a track's cover backup.
func coverBackupName(batch, path string) string {
	sum := sha256.Sum256([]byte(path))
	return batch + "-" + hex.EncodeToString(sum[:])[:16] + ".jpg"
}

// inside reports whether p is root or lies below it, judged lexically (no '..' escapes).
// Relative paths never count: the journal only ever stores absolute ones.
func inside(root, p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkEntry refuses a journal entry that points outside the tag root: the journal is a
// plain file in the library and must not be able to steer renames or writes elsewhere.
func checkEntry(root string, e journalEntry) error {
	for _, p := range []string{e.Path, e.NewPath, e.CoverBackup} {
		if p != "" && !inside(root, p) {
			return fmt.Errorf("refusing to undo: journal path %q is outside %s", p, root)
		}
	}
	if e.Path == "" {
		return fmt.Errorf("refusing to undo: journal entry without a path")
	}
	return nil
}

func getURL(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ytm-to-mp3/1 ( https://github.com/bybrooklyn/ytm-to-mp3 )")
	client := &http.Client{Timeout: 30 * time.Second, Transport: Transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	_ = os.Remove(dst)
	f, err := store.CreateExcl(dst, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 20<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// undo reverts the most recent applied batch: tags, cover art and file locations.
func undo(ctx context.Context, o Options) error {
	f, err := os.Open(journalPath(o.Dir))
	if err != nil {
		return fmt.Errorf("nothing to undo (no tag journal in %s)", o.Dir)
	}
	var all []journalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	f.Close()
	if len(all) == 0 {
		return fmt.Errorf("nothing to undo")
	}
	last := all[len(all)-1].Batch
	var keep, revert []journalEntry
	for _, e := range all {
		if e.Batch == last {
			revert = append(revert, e)
		} else {
			keep = append(keep, e)
		}
	}
	// Validate everything before touching anything.
	for _, e := range revert {
		if err := checkEntry(o.Dir, e); err != nil {
			return err
		}
	}
	n := 0
	for i := len(revert) - 1; i >= 0; i-- {
		e := revert[i]
		cur := e.Path
		if e.NewPath != "" {
			if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err == nil && os.Rename(e.NewPath, e.Path) == nil {
				cur = e.Path
				if o.OnMove != nil {
					o.OnMove(e.NewPath, e.Path)
				}
			} else {
				cur = e.NewPath
			}
		}
		if len(e.Old) == 0 && e.CoverBackup == "" {
			n++ // a pure move (folder merge): nothing was written to the file
			continue
		}
		if err := Write(ctx, o.FFmpeg, o.FFprobe, cur, e.Old, e.CoverBackup); err != nil {
			o.UI.Bad("%s · %v", cur, err)
			continue
		}
		n++
	}
	o.UI.Good("Reverted %d files · batch %s", n, last)
	// Drop the reverted batch from the journal.
	return writeJournal(o.Dir, keep)
}
