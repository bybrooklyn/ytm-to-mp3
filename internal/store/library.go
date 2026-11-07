package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/names"
)

// librarySchema is the source-agnostic record of what has been downloaded,
// keyed by a canonical song key (catalog.Track.Key). It is the spine the
// unified downloader uses to skip songs already held and to upgrade a song to
// a better source.
const librarySchema = `
CREATE TABLE IF NOT EXISTS library_tracks (
  key     TEXT PRIMARY KEY,                 -- catalog.Track.Key()
  title   TEXT NOT NULL DEFAULT '',
  artist  TEXT NOT NULL DEFAULT '',
  source  TEXT NOT NULL DEFAULT '',         -- apple | youtube
  quality INTEGER NOT NULL DEFAULT 0,       -- catalog.Rank(source); higher is better
  path    TEXT NOT NULL DEFAULT '',
  updated INTEGER NOT NULL DEFAULT 0
);
-- Which source each tag of a held song came from (internal/meta), as JSON {"isrc":"apple",...}.
CREATE TABLE IF NOT EXISTS library_provenance (
  key  TEXT PRIMARY KEY,                    -- catalog.Track.Key()
  json TEXT NOT NULL DEFAULT '{}'
);`

// LibraryState is what the unified downloader has for one canonical song.
type LibraryState struct {
	Done    bool
	Source  string
	Quality int
	Path    string
}

// Library returns what has been downloaded for canonical key (zero value if
// nothing, or if the recorded file is gone).
func (s *Store) Library(key string) LibraryState {
	var st LibraryState
	err := s.db.QueryRow(`SELECT source, quality, path FROM library_tracks WHERE key=?`, key).
		Scan(&st.Source, &st.Quality, &st.Path)
	if err != nil {
		return LibraryState{}
	}
	st.Done = st.Path != ""
	return st
}

// LibraryPut records that canonical key is held at path, from source (quality
// catalog.Rank(source)).
func (s *Store) LibraryPut(key, title, artist, source string, quality int, path string) error {
	_, err := s.db.Exec(`INSERT INTO library_tracks(key,title,artist,source,quality,path,updated)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET title=excluded.title, artist=excluded.artist,
		source=excluded.source, quality=excluded.quality, path=excluded.path, updated=excluded.updated`,
		key, names.Clean(title), names.Clean(artist), source, quality, path, time.Now().Unix())
	return err
}

// SetProvenance records which source each tag of canonical key came from.
func (s *Store) SetProvenance(key string, from map[string]string) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO library_provenance(key,json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET json=excluded.json`, key, string(b))
	return err
}

// Provenance returns what SetProvenance recorded for key (nil if nothing).
func (s *Store) Provenance(key string) map[string]string {
	var raw string
	if s.db.QueryRow(`SELECT json FROM library_provenance WHERE key=?`, key).Scan(&raw) != nil {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

// AddPending inserts a track row so the engine's MarkDone (an UPDATE) has
// something to update. It is used for songs whose id is not a YouTube video id
// (Spotify/Apple-sourced tracks downloaded from YouTube by search). Existing
// rows are left untouched.
func (s *Store) AddPending(id, title, artist string) error {
	_, err := s.db.Exec(`INSERT INTO tracks(id,title,artist,updated) VALUES(?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, id, names.Clean(title), names.Clean(artist), time.Now().Unix())
	return err
}

// TrackStatus reports whether track id finished and where its file is.
func (s *Store) TrackStatus(id string) (done bool, path string) {
	var status string
	err := s.db.QueryRow(`SELECT status, path FROM tracks WHERE id=?`, id).Scan(&status, &path)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return false, ""
	}
	return status == "done", path
}

// TrackOutcome is how a YouTube library track stands: its status ("done", "failed",
// "pending"), whether a failure is permanent, and the error text.
func (s *Store) TrackOutcome(id string) (status string, permanent bool, errText string) {
	var perm int
	if err := s.db.QueryRow(`SELECT status, permanent, error FROM tracks WHERE id=?`, id).Scan(&status, &perm, &errText); err != nil {
		return "", false, ""
	}
	return status, perm == 1, errText
}

// HeldCopy is one downloaded song as the database knows it, from any source.
type HeldCopy struct {
	ID     string // YouTube video id (or a synthetic id) for youtube; Apple catalog id for apple
	Title  string
	Artist string
	Source string // apple | youtube | soundcloud | bandcamp
	Path   string
}

// Held lists every downloaded song: Apple Music downloads first, then YouTube ones, then
// SoundCloud and Bandcamp ones. A
// YouTube row whose file is now an Apple copy (ytm apple match --replace) is reported
// once, as apple.
func (s *Store) Held() ([]HeldCopy, error) {
	var out []HeldCopy
	seen := map[string]bool{}
	for _, q := range []struct{ source, sql string }{
		{"apple", `SELECT id, title, artist, path FROM apple_tracks WHERE status='done' AND path!=''`},
		{"youtube", `SELECT id, title, artist, path FROM tracks WHERE status='done' AND path!=''`},
		// Songs from other sites are recorded only in the cross-source library table.
		{"soundcloud", `SELECT key, title, artist, path FROM library_tracks WHERE source='soundcloud' AND path!=''`},
		{"bandcamp", `SELECT key, title, artist, path FROM library_tracks WHERE source='bandcamp' AND path!=''`},
		{"bandcamp-purchase", `SELECT key, title, artist, path FROM library_tracks WHERE source='bandcamp-purchase' AND path!=''`},
	} {
		rows, err := s.db.Query(q.sql)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			h := HeldCopy{Source: q.source}
			if err := rows.Scan(&h.ID, &h.Title, &h.Artist, &h.Path); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[h.Path] {
				continue
			}
			seen[h.Path] = true
			out = append(out, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
