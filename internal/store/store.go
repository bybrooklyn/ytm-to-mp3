// Package store keeps per-track state in a small SQLite database so runs are
// resumable and failures are remembered.
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS tracks (
  id        TEXT PRIMARY KEY,
  title     TEXT NOT NULL DEFAULT '',
  artist    TEXT NOT NULL DEFAULT '',
  status    TEXT NOT NULL DEFAULT 'pending', -- pending | done | failed
  permanent INTEGER NOT NULL DEFAULT 0,      -- failed and not worth retrying
  path      TEXT NOT NULL DEFAULT '',
  error     TEXT NOT NULL DEFAULT '',
  attempts  INTEGER NOT NULL DEFAULT 0,
  updated   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS albums (           -- YouTube Music album lookups (--albums)
  key     TEXT PRIMARY KEY,                   -- artist + album as saved, folded
  data    TEXT NOT NULL DEFAULT '',           -- the album found (JSON); '' = not found
  checked INTEGER NOT NULL DEFAULT 0
);`

// Open opens (creating if needed) <dir>/state.db.
func Open(dir string) (*Store, error) {
	if err := PrivateDir(dir); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; the workload is tiny
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(dbPath+suffix, 0o600) // missing files are fine
	}
	return &Store{db}, nil
}

// migrate adds columns that databases from older versions lack.
func migrate(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('tracks')`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !have["music_tried"] {
		// 1 once a failed track has also been searched for on YouTube Music. Tracks that
		// failed before this existed get that one extra try.
		if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN music_tried INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !have["premium"] {
		// YouTube Premium upgrade pass: 1 = upgraded to 256k, -1 = no Premium stream offered
		if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN premium INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !have["replacement"] {
		// the video YouTube Music has for a song whose own id is dead ("" = none found)
		if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN replacement TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// MarkMusicTried records that the YouTube Music search was tried for id, and the video it
// found ("" if none), so a later run goes straight to it.
func (s *Store) MarkMusicTried(id, replacement string) error {
	if replacement != "" && !model.ValidID(replacement) {
		replacement = ""
	}
	_, err := s.db.Exec(`UPDATE tracks SET music_tried=1, replacement=? WHERE id=?`, replacement, id)
	return err
}

// albumRecheck is how long "not found on YouTube Music" is believed before looking again.
const albumRecheck = 30 * 24 * time.Hour

// Album returns a cached album lookup: data is what was found ("" = nothing), ok is false
// when there is no usable answer (never looked up, or a "not found" that has gone stale).
func (s *Store) Album(key string) (data string, ok bool) {
	var checked int64
	if err := s.db.QueryRow(`SELECT data, checked FROM albums WHERE key=?`, key).Scan(&data, &checked); err != nil {
		return "", false
	}
	if data == "" && time.Since(time.Unix(checked, 0)) > albumRecheck {
		return "", false
	}
	return data, true
}

// SetAlbum caches an album lookup ("" = not found).
func (s *Store) SetAlbum(key, data string) error {
	_, err := s.db.Exec(`INSERT INTO albums(key,data,checked) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET data=excluded.data, checked=excluded.checked`, key, data, time.Now().Unix())
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Pending is the subset of tracks still to download.
type Pending struct {
	Todo      []model.Track
	Done      int
	Skipped   int // permanently unavailable, not retried
	Invalid   int // malformed ids that were dropped
	Preferred map[string]bool
	// MusicFirst are known-dead ids not yet searched for on YouTube Music: the engine
	// skips the normal lookup for them.
	MusicFirst map[string]bool
	// Replacements maps a dead id to the video YouTube Music had for it (found earlier).
	Replacements map[string]string
}

// Plan registers tracks and returns those that still need downloading.
// With retryAll, permanently failed tracks are attempted again.
func (s *Store) Plan(tracks []model.Track, retryAll bool) (Pending, error) {
	var p Pending
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT INTO tracks(id,title,artist,updated) VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE title END,
		artist=CASE WHEN excluded.artist<>'' THEN excluded.artist ELSE artist END`)
	if err != nil {
		return p, err
	}
	defer ins.Close()
	for _, t := range tracks {
		if !model.ValidID(t.ID) {
			p.Invalid++
			continue
		}
		if _, err := ins.Exec(t.ID, names.Clean(t.Title), names.Clean(t.PrimaryArtist()), time.Now().Unix()); err != nil {
			return p, err
		}
		var status, repl string
		var perm, musicTried int
		if err := tx.QueryRow(`SELECT status, permanent, music_tried, replacement FROM tracks WHERE id=?`, t.ID).Scan(&status, &perm, &musicTried, &repl); err != nil {
			return p, err
		}
		if status != "done" && model.ValidID(repl) {
			// found on YouTube Music before but not downloaded yet (an interrupted run):
			// go straight to that video, no search
			if p.Replacements == nil {
				p.Replacements = map[string]string{}
			}
			p.Replacements[t.ID] = repl
			if p.MusicFirst == nil {
				p.MusicFirst = map[string]bool{}
			}
			p.MusicFirst[t.ID] = true
			p.Todo = append(p.Todo, t)
			continue
		}
		switch {
		case status == "done":
			p.Done++
		case perm == 1 && musicTried == 1 && !retryAll:
			// unavailable, and YouTube Music had no copy either
			p.Skipped++
		case perm == 1 && musicTried == 0:
			// unavailable before the YouTube Music search existed: go straight to it
			if p.MusicFirst == nil {
				p.MusicFirst = map[string]bool{}
			}
			p.MusicFirst[t.ID] = true
			p.Todo = append(p.Todo, t)
		default:
			p.Todo = append(p.Todo, t)
		}
	}
	return p, tx.Commit()
}

func (s *Store) MarkDone(id, path string) error {
	_, err := s.db.Exec(`UPDATE tracks SET status='done', permanent=0, path=?, error='', updated=? WHERE id=?`,
		path, time.Now().Unix(), id)
	return err
}

func (s *Store) MarkFailed(id, reason string, permanent bool) error {
	p := 0
	if permanent {
		p = 1
	}
	_, err := s.db.Exec(`UPDATE tracks SET status='failed', permanent=?, error=?, attempts=attempts+1, updated=? WHERE id=?`,
		p, names.Clean(reason), time.Now().Unix(), id)
	return err
}

// Failure is a track that did not download.
type Failure struct {
	ID, Title, Artist, Error string
	// OtherVersion: another id with the same artist and title did download (exports often
	// list a song under several ids: single, album, music video).
	OtherVersion bool
}

func (s *Store) Failures() ([]Failure, error) {
	rows, err := s.db.Query(`SELECT t.id, t.title, t.artist, t.error,
		EXISTS(SELECT 1 FROM tracks d WHERE d.status='done' AND d.id<>t.id AND t.title<>''
			AND lower(d.title)=lower(t.title) AND lower(d.artist)=lower(t.artist))
		FROM tracks t WHERE t.status='failed' ORDER BY t.updated`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Failure
	for rows.Next() {
		var f Failure
		if err := rows.Scan(&f.ID, &f.Title, &f.Artist, &f.Error, &f.OtherVersion); err != nil {
			return nil, err
		}
		f.ID, f.Title, f.Artist, f.Error = names.Clean(f.ID), names.Clean(f.Title), names.Clean(f.Artist), names.Clean(f.Error)
		out = append(out, f)
	}
	return out, rows.Err()
}

var (
	fileIDRe   = regexp.MustCompile(`\[([\w-]{11})\]\.(mp3|m4a|opus|flac|ogg|aac|wav)$`)
	audioExtRe = regexp.MustCompile(`(?i)\.(mp3|m4a|opus|flac|ogg|aac|wav)$`)
)

// ImportDisk marks tracks done for audio files already under dir, so nothing is
// downloaded twice: files named "... [id].ext" (older ytm versions and other tools) by
// their name, others by idOf (which reads the YouTube link ytm writes into the tags; nil
// = names only). idOf is only asked about files the database doesn't know yet, so on a
// normal run it isn't called at all.
func (s *Store) ImportDisk(dir string, idOf func(path string) string) (int, error) {
	known := map[string]bool{}
	if rows, err := s.db.Query(`SELECT path FROM tracks WHERE status='done' AND path<>''`); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				known[p] = true
			}
		}
		rows.Close()
	}
	if len(known) > 0 {
		// Reading tags is for rebuilding a lost or new database. Otherwise files ytm didn't
		// write (no id anywhere) would be probed again on every run.
		idOf = nil
	}
	found := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != dir {
			return filepath.SkipDir
		}
		if d.IsDir() || known[p] || !audioExtRe.MatchString(d.Name()) {
			return nil
		}
		if m := fileIDRe.FindStringSubmatch(d.Name()); m != nil && model.ValidID(m[1]) {
			found[m[1]] = p
		} else if idOf != nil {
			if id := idOf(p); model.ValidID(id) {
				found[id] = p
			}
		}
		return nil
	})
	n := 0
	for id, p := range found {
		var status string
		_ = s.db.QueryRow(`SELECT status FROM tracks WHERE id=?`, id).Scan(&status)
		// A done track's path is updated too (the file was renamed or moved), so it is
		// known next time and never looked at again.
		if _, err := s.db.Exec(`INSERT INTO tracks(id,status,path,updated) VALUES(?, 'done', ?, ?)
			ON CONFLICT(id) DO UPDATE SET status='done', path=excluded.path`,
			id, p, time.Now().Unix()); err != nil {
			return n, err
		}
		if status != "done" {
			n++
		}
	}
	return n, nil
}

// PremiumTrack is a downloaded track the Premium pass may upgrade.
type PremiumTrack struct{ ID, Path, Title string }

// PremiumTodo lists up to limit downloaded tracks not yet upgraded or found to have no
// Premium stream, oldest first.
func (s *Store) PremiumTodo(limit int) ([]PremiumTrack, error) {
	rows, err := s.db.Query(`SELECT id, path, title FROM tracks
		WHERE status='done' AND path<>'' AND premium=0 ORDER BY updated LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PremiumTrack
	for rows.Next() {
		var t PremiumTrack
		if err := rows.Scan(&t.ID, &t.Path, &t.Title); err != nil {
			return nil, err
		}
		if model.ValidID(t.ID) {
			t.Title = names.Clean(t.Title)
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// SetPremium records the Premium pass's result for id: 1 upgraded, -1 none offered.
func (s *Store) SetPremium(id string, v int) error {
	_, err := s.db.Exec(`UPDATE tracks SET premium=? WHERE id=?`, v, id)
	return err
}

// DoneSince lists the files of tracks finished at or after t.
func (s *Store) DoneSince(t time.Time) []string {
	rows, err := s.db.Query(`SELECT path FROM tracks WHERE status='done' AND path<>'' AND updated>=?`, t.Unix())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// Owner is the id of the track whose file is path ("" if none).
func (s *Store) Owner(path string) string {
	var id string
	_ = s.db.QueryRow(`SELECT id FROM tracks WHERE path=? LIMIT 1`, path).Scan(&id)
	return id
}

// SetPath records where id's file is (or is about to be written), without changing its
// status: claiming the name before writing keeps two songs from picking the same one.
func (s *Store) SetPath(id, path string) error {
	_, err := s.db.Exec(`UPDATE tracks SET path=? WHERE id=?`, path, id)
	return err
}

// MovePath records that a file moved (e.g. renamed by `ytm tag`).
func (s *Store) MovePath(from, to string) error {
	_, err := s.db.Exec(`UPDATE tracks SET path=? WHERE path=?`, to, from)
	return err
}
