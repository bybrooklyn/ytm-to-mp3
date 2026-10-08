package tag

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
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
	_ = os.MkdirAll(filepath.Dir(journalPath(dir)), 0o755)
	f, err := os.OpenFile(journalPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

func hashString(s string) uint32 { h := fnv.New32a(); h.Write([]byte(s)); return h.Sum32() }

func getURL(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ytm-to-mp3/1 ( https://github.com/bybrooklyn/ytm-to-mp3 )")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	f, err := os.Create(dst)
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
	n := 0
	for i := len(revert) - 1; i >= 0; i-- {
		e := revert[i]
		cur := e.Path
		if e.NewPath != "" {
			if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err == nil && os.Rename(e.NewPath, e.Path) == nil {
				cur = e.Path
			} else {
				cur = e.NewPath
			}
		}
		if err := Write(ctx, o.FFmpeg, cur, e.Old, e.CoverBackup); err != nil {
			o.UI.Printf("✗ %s  (%v)", cur, err)
			continue
		}
		n++
	}
	o.UI.Printf("Reverted %d files from batch %s", n, last)
	// Drop the reverted batch from the journal.
	out, err := os.Create(journalPath(o.Dir))
	if err != nil {
		return err
	}
	defer out.Close()
	for _, e := range keep {
		b, _ := json.Marshal(e)
		out.Write(append(b, '\n'))
	}
	return nil
}
