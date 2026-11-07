package lyrics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Retry-after periods for answers that were "no lyrics".
const (
	NoneRetry         = 14 * 24 * time.Hour // nobody had them: look again in two weeks
	InstrumentalRetry = 90 * 24 * time.Hour
)

// Cache keeps what was found per song as small JSON files in one directory (the music
// folder's .ytm/lyrics). The key is the song's normalised title and artist, not its file
// path, so the entry survives renames and upgrades to a better source: the new file just
// gets the cached lyrics. A song asked about is never fetched twice; "no lyrics found" is
// remembered too, until its retry-after has passed.
type Cache struct {
	Dir string
	Now func() time.Time // default time.Now
	mu  sync.Mutex
}

// entry is one cached answer.
type entry struct {
	V          int    `json:"v"`
	Status     string `json:"status"` // synced | plain | instrumental | none
	Source     string `json:"source,omitempty"`
	Title      string `json:"title,omitempty"` // the record's own names
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	LRC        string `json:"lrc,omitempty"` // synced lyrics as LRC lines
	Plain      string `json:"plain,omitempty"`
	Checked    int64  `json:"checked"`
	RetryAfter int64  `json:"retry_after,omitempty"` // unix; 0 = never (found lyrics)
}

// Key identifies a song for the cache: normalised title, primary artist and version markers.
func Key(q Query) string {
	s := Normalize(q.Title) + "\x00" + Normalize(primaryArtist(q.Artist)) + "\x00" + strings.Join(Versions(q.Title), ",")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:10])
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) path(q Query) string { return filepath.Join(c.Dir, Key(q)+".json") }

// Get returns the cached answer for q. ok is false when there is none worth believing: never
// asked, a "none" whose retry-after has passed, or an entry for another cut of the song.
func (c *Cache) Get(q Query) (Result, bool) {
	if c == nil || c.Dir == "" {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(c.path(q))
	if err != nil {
		return Result{}, false
	}
	var e entry
	if json.Unmarshal(b, &e) != nil || e.V != 1 {
		return Result{}, false
	}
	if e.RetryAfter != 0 && c.now().Unix() >= e.RetryAfter {
		return Result{}, false
	}
	r := Result{Cached: true, Source: e.Source}
	switch e.Status {
	case "none":
		r.Kind = ""
		return r, true
	case "instrumental":
		r.Kind = KindInstrumental
		return r, true
	case "synced", "plain":
		// found lyrics belong to a cut of a given length: a much longer or shorter file of
		// the same name gets a fresh look instead
		if d := time.Duration(e.DurationMS) * time.Millisecond; d > 0 && q.Duration > 0 {
			diff := d - q.Duration
			if diff < 0 {
				diff = -diff
			}
			if (e.Status == "synced" && diff > SyncedTolerance) || diff > PlainTolerance {
				return Result{}, false
			}
		}
		r.Kind = Kind(e.Status)
		r.Header = Header{Title: e.Title, Artist: e.Artist, Album: e.Album}
		r.Plain = e.Plain
		if e.LRC != "" {
			r.Synced, _ = ParseLRC(e.LRC)
		}
		if (r.Kind == KindSynced && len(r.Synced) == 0) || (r.Kind == KindPlain && r.Plain == "") {
			return Result{}, false
		}
		return r, true
	}
	return Result{}, false
}

// Put stores r as the answer for q. r.Kind "" means nothing was found.
func (c *Cache) Put(q Query, r Result, matchedDuration time.Duration) error {
	if c == nil || c.Dir == "" {
		return nil
	}
	e := entry{V: 1, Status: string(r.Kind), Source: r.Source, Checked: c.now().Unix()}
	switch r.Kind {
	case "":
		e.Status = "none"
		e.RetryAfter = c.now().Add(NoneRetry).Unix()
	case KindInstrumental:
		e.RetryAfter = c.now().Add(InstrumentalRetry).Unix()
	case KindSynced:
		e.LRC = FormatLRC(Header{}, r.Synced)
	case KindPlain:
		e.Plain = r.Plain
	}
	e.Title, e.Artist, e.Album = r.Header.Title, r.Header.Artist, r.Header.Album
	e.DurationMS = matchedDuration.Milliseconds()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	if err := os.Rename(tmp.Name(), c.path(q)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
