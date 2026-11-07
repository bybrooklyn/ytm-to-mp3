package lyrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
)

// CacheDir is where a music folder's lyrics cache lives.
func CacheDir(musicDir string) string { return filepath.Join(musicDir, ".ytm", "lyrics") }

// Sidecar paths: synced lyrics go in "<song>.lrc" (read by most players, Navidrome and
// Jellyfin), plain ones in "<song>.txt" (also read by Navidrome and Jellyfin; a plain
// ".lrc" would show every line at 0:00 in some players).
func sidecar(audio string, k Kind) string {
	base := strings.TrimSuffix(audio, filepath.Ext(audio))
	if k == KindSynced {
		return base + ".lrc"
	}
	return base + ".txt"
}

// MoveSidecars makes the lyrics files of audio file "from" follow it to "to" (a rename, or
// an upgrade to a better source). A sidecar already at the destination is kept.
func MoveSidecars(from, to string) {
	if from == to {
		return
	}
	for _, k := range []Kind{KindSynced, KindPlain} {
		src, dst := sidecar(from, k), sidecar(to, k)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			_ = os.Remove(src)
			continue
		}
		_ = os.Rename(src, dst)
	}
}

func hasSidecar(audio string) (path string, k Kind) {
	for _, k := range []Kind{KindSynced, KindPlain} {
		if p := sidecar(audio, k); fileExists(p) {
			return p, k
		}
	}
	return "", ""
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }

// Service fetches lyrics for audio files and stores them next to and inside each one.
type Service struct {
	Fetcher *Fetcher
	FFmpeg  string
	FFprobe string
}

// Outcome says what Process did for one file.
type Outcome struct {
	Kind   Kind   // what the file now has ("" = nothing found)
	Source string // provider, or "" when it was already there
	Cached bool   // no request was made
	Had    bool   // the file already had lyrics (embedded or sidecar); nothing fetched
}

// Process gives one audio file its lyrics. Lyrics already in the file (its tag) or beside it
// (a sidecar) are kept as they are and completed (sidecar written from the tag, or tag from
// the sidecar), never replaced. Otherwise they are looked up (cache first) and written as a
// sidecar and into the file's lyrics tag. Nothing found writes nothing.
func (s *Service) Process(ctx context.Context, path string) (Outcome, error) {
	info, err := tag.Probe(ctx, s.FFprobe, path)
	if err != nil {
		return Outcome{}, err
	}
	q := QueryFromInfo(info)
	embedded := strings.TrimSpace(info.Tags["lyrics"])
	if sc, k := hasSidecar(path); sc != "" {
		if embedded == "" {
			b, err := os.ReadFile(sc)
			if err != nil {
				return Outcome{}, err
			}
			if err := s.embed(ctx, path, string(b)); err != nil {
				return Outcome{Had: true, Kind: k}, err
			}
		}
		return Outcome{Had: true, Kind: k}, nil
	}
	if embedded != "" {
		k := KindPlain
		if lines, _ := ParseLRC(embedded); SyncedLines(lines) >= minLines {
			k = KindSynced
		}
		if err := writeFile(sidecar(path, k), embedded+"\n"); err != nil {
			return Outcome{}, err
		}
		return Outcome{Had: true, Kind: k}, nil
	}
	res, err := s.Fetcher.Lookup(ctx, q)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Kind: res.Kind, Source: res.Source, Cached: res.Cached}
	if !res.Found() {
		return out, nil
	}
	text := Text(q, res)
	if err := writeFile(sidecar(path, res.Kind), text); err != nil {
		return out, err
	}
	// a plain result replacing nothing; a stale sidecar of the other kind cannot exist here
	return out, s.embed(ctx, path, text)
}

// Text is what is stored for a result: clean LRC with headers for synced lyrics (the song's
// own title, artist, album and length, and the source credit), plain text otherwise.
func Text(q Query, r Result) string {
	if r.Kind == KindSynced {
		return FormatLRC(Header{Title: q.Title, Artist: q.Artist, Album: q.Album, Length: q.Duration,
			By: "ytm · " + r.Source}, r.Synced)
	}
	return strings.TrimSpace(r.Plain) + "\n"
}

func (s *Service) embed(ctx context.Context, path, text string) error {
	if s.FFmpeg == "" {
		return errors.New("lyrics: no ffmpeg to write the tag")
	}
	if err := tag.Write(ctx, s.FFmpeg, s.FFprobe, path, map[string]string{"lyrics": strings.TrimRight(text, "\n")}, ""); err != nil {
		return fmt.Errorf("embedding lyrics: %w", err)
	}
	return nil
}

func writeFile(path, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ytm-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(text)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	_ = os.Chmod(tmp.Name(), 0o644)
	if err := store.Replace(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// QueryFromInfo builds the lookup from a file's tags: title, artist, album and length.
func QueryFromInfo(info tag.Info) Query {
	artist := strings.TrimSuffix(strings.TrimSpace(info.Tags["artist"]), " - Topic")
	return Query{Title: strings.TrimSpace(info.Tags["title"]), Artist: strings.TrimSpace(artist),
		Album: strings.TrimSpace(info.Tags["album"]), Duration: time.Duration(info.Duration * float64(time.Second))}
}
