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
);`

// Open opens (creating if needed) <dir>/state.db.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "state.db"))+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; the workload is tiny
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Pending is the subset of tracks still to download.
type Pending struct {
	Todo      []model.Track
	Done      int
	Skipped   int // permanently unavailable, not retried
	Preferred map[string]bool
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
		if _, err := ins.Exec(t.ID, t.Title, t.PrimaryArtist(), time.Now().Unix()); err != nil {
			return p, err
		}
		var status string
		var perm int
		if err := tx.QueryRow(`SELECT status, permanent FROM tracks WHERE id=?`, t.ID).Scan(&status, &perm); err != nil {
			return p, err
		}
		switch {
		case status == "done":
			p.Done++
		case perm == 1 && !retryAll:
			p.Skipped++
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
		p, reason, time.Now().Unix(), id)
	return err
}

// Failure is a track that did not download.
type Failure struct{ ID, Title, Artist, Error string }

func (s *Store) Failures() ([]Failure, error) {
	rows, err := s.db.Query(`SELECT id,title,artist,error FROM tracks WHERE status='failed' ORDER BY updated`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Failure
	for rows.Next() {
		var f Failure
		if err := rows.Scan(&f.ID, &f.Title, &f.Artist, &f.Error); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

var fileIDRe = regexp.MustCompile(`\[([\w-]{11})\]\.(mp3|m4a|opus|flac|ogg|aac|wav)$`)

// ImportDisk marks tracks done if a file named "... [id].ext" already exists
// under dir, so libraries from older tools aren't downloaded twice.
func (s *Store) ImportDisk(dir string) (int, error) {
	found := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != dir {
			return filepath.SkipDir
		}
		if m := fileIDRe.FindStringSubmatch(d.Name()); m != nil && !d.IsDir() {
			found[m[1]] = p
		}
		return nil
	})
	n := 0
	for id, p := range found {
		res, err := s.db.Exec(`INSERT INTO tracks(id,status,path,updated) VALUES(?, 'done', ?, ?)
			ON CONFLICT(id) DO UPDATE SET status='done', path=excluded.path WHERE status<>'done'`,
			id, p, time.Now().Unix())
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, nil
}
