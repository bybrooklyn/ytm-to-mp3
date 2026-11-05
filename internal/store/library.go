package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
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
