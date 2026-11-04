package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

const appleSchema = `
CREATE TABLE IF NOT EXISTS apple_tracks (     -- songs downloaded (or tried) from Apple Music
  id        TEXT PRIMARY KEY,                 -- Apple catalog id
  title     TEXT NOT NULL DEFAULT '',
  artist    TEXT NOT NULL DEFAULT '',
  status    TEXT NOT NULL DEFAULT 'pending',  -- done | failed
  permanent INTEGER NOT NULL DEFAULT 0,       -- failed and not worth retrying
  path      TEXT NOT NULL DEFAULT '',
  error     TEXT NOT NULL DEFAULT '',
  attempts  INTEGER NOT NULL DEFAULT 0,
  updated   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS apple_matches (    -- library songs looked up on Apple Music
  yt_id   TEXT PRIMARY KEY,                   -- the YouTube video id of the library song
  data    TEXT NOT NULL DEFAULT '',           -- the match (JSON); '' = not found
  score   REAL NOT NULL DEFAULT 0,
  checked INTEGER NOT NULL DEFAULT 0
);`

// appleRecheck is how long "not on Apple Music" is believed before searching again
// (catalogs grow).
const appleRecheck = 30 * 24 * time.Hour

// AppleState is what is known about one Apple song.
type AppleState struct {
	Done, Permanent bool // downloaded / failed for good
	Path            string
	Attempts        int
}

// Apple returns the state of Apple song id (zero value if never tried).
func (s *Store) Apple(id string) AppleState {
	var st AppleState
	var status string
	var perm int
	var updated int64
	err := s.db.QueryRow(`SELECT status, permanent, path, attempts, updated FROM apple_tracks WHERE id=?`, id).
		Scan(&status, &perm, &st.Path, &st.Attempts, &updated)
	if err != nil {
		return AppleState{}
	}
	st.Done = status == "done"
	// "not offered" is believed for a while, then tried again (rights change).
	st.Permanent = status == "failed" && perm == 1 && time.Since(time.Unix(updated, 0)) < appleRecheck
	return st
}

// AppleDone records that Apple song id is at path.
func (s *Store) AppleDone(id, title, artist, path string) error {
	_, err := s.db.Exec(`INSERT INTO apple_tracks(id,title,artist,status,permanent,path,error,attempts,updated)
		VALUES(?,?,?,'done',0,?,'',1,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, artist=excluded.artist, status='done',
		permanent=0, path=excluded.path, error='', attempts=attempts+1, updated=excluded.updated`,
		id, names.Clean(title), names.Clean(artist), path, time.Now().Unix())
	return err
}

// AppleFailed records a failed Apple download (permanent: don't try it again).
func (s *Store) AppleFailed(id, title, artist, reason string, permanent bool) error {
	p := 0
	if permanent {
		p = 1
	}
	_, err := s.db.Exec(`INSERT INTO apple_tracks(id,title,artist,status,permanent,error,attempts,updated)
		VALUES(?,?,?,'failed',?,?,1,?)
		ON CONFLICT(id) DO UPDATE SET status='failed', permanent=excluded.permanent,
		error=excluded.error, attempts=attempts+1, updated=excluded.updated`,
		id, names.Clean(title), names.Clean(artist), p, names.Clean(reason), time.Now().Unix())
	return err
}

// AppleMatch returns the cached Apple lookup of library song ytID: data is the match
// ("" = not on Apple Music), ok is false when there is no usable answer (never looked
// up, or a "not found" that has gone stale).
func (s *Store) AppleMatch(ytID string) (data string, score float64, ok bool) {
	var checked int64
	err := s.db.QueryRow(`SELECT data, score, checked FROM apple_matches WHERE yt_id=?`, ytID).Scan(&data, &score, &checked)
	if err != nil {
		return "", 0, false
	}
	if data == "" && time.Since(time.Unix(checked, 0)) > appleRecheck {
		return "", 0, false
	}
	return data, score, true
}

// SetAppleMatch caches an Apple lookup of library song ytID ("" = not found).
func (s *Store) SetAppleMatch(ytID, data string, score float64) error {
	_, err := s.db.Exec(`INSERT INTO apple_matches(yt_id,data,score,checked) VALUES(?,?,?,?)
		ON CONFLICT(yt_id) DO UPDATE SET data=excluded.data, score=excluded.score, checked=excluded.checked`,
		ytID, data, score, time.Now().Unix())
	return err
}

// Path is where YouTube track id's file is ("" if it isn't downloaded).
func (s *Store) Path(id string) string {
	var p string
	err := s.db.QueryRow(`SELECT path FROM tracks WHERE id=? AND status='done'`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	return p
}

// ReplaceWithApple records that YouTube track id's file is now the Apple download at
// path: it counts as downloaded (a library run won't fetch it from YouTube again) and
// the Premium pass leaves it alone. A track not in the database yet is added.
func (s *Store) ReplaceWithApple(id, title, artist, path string) error {
	_, err := s.db.Exec(`INSERT INTO tracks(id,title,artist,status,path,updated,premium) VALUES(?,?,?,'done',?,?,1)
		ON CONFLICT(id) DO UPDATE SET status='done', permanent=0, path=excluded.path, error='',
		updated=excluded.updated, premium=1`,
		id, names.Clean(title), names.Clean(artist), path, time.Now().Unix())
	return err
}
