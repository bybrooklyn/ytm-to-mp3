// Package verify is the integrity check every download passes before it is filed in the
// library: not empty, the right kind of file (by its first bytes), and, when the expected
// length is known, a duration close to it. A file that fails is a jobs.CorruptError and
// must be discarded, never filed.
package verify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/jobs"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
)

// Want describes what the file should be. Zero fields are not checked.
type Want struct {
	Ext      string  // ".flac", ".m4a", ".mp3", ".opus", ".ogg", ".wav": the container expected
	Duration float64 // expected seconds
	// Tolerance is how far the real duration may be from Duration, in seconds
	// (default: 5 percent of it, at least 3 s, since catalogs and encoders disagree a bit).
	Tolerance float64
}

// Probe is how the duration is read; tests replace it. It returns seconds.
var Probe = func(ctx context.Context, ffprobe, path string) (float64, error) {
	info, err := tag.Probe(ctx, ffprobe, path)
	return info.Duration, err
}

// File checks path. ffprobe may be "" to skip the duration check.
func File(ctx context.Context, ffprobe, path string, w Want) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < 1024 {
		return &jobs.CorruptError{Msg: fmt.Sprintf("the file is only %d bytes", st.Size())}
	}
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	if err := magic(strings.ToLower(w.Ext), head[:n]); err != nil {
		return &jobs.CorruptError{Msg: err.Error()}
	}
	if w.Duration > 0 && ffprobe != "" {
		got, err := Probe(ctx, ffprobe, path)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &jobs.CorruptError{Msg: "the file can't be read: " + err.Error()}
		}
		tol := w.Tolerance
		if tol <= 0 {
			tol = max(3, w.Duration*0.05)
		}
		if got <= 0 || got < w.Duration-tol || got > w.Duration+tol*3 {
			return &jobs.CorruptError{Msg: fmt.Sprintf("it is %.0f s long but should be about %.0f s (truncated?)", got, w.Duration)}
		}
	}
	return nil
}

// magic checks the first bytes against the container ext promises. Unknown extensions pass.
func magic(ext string, h []byte) error {
	ok := func(b bool, what string) error {
		if b {
			return nil
		}
		return fmt.Errorf("it doesn't start like a %s file", what)
	}
	switch ext {
	case ".flac":
		return ok(bytes.HasPrefix(h, []byte("fLaC")) || bytes.HasPrefix(h, []byte("ID3")), "FLAC")
	case ".m4a", ".mp4", ".aac":
		return ok(len(h) >= 8 && string(h[4:8]) == "ftyp", "MP4/M4A")
	case ".mp3":
		return ok(bytes.HasPrefix(h, []byte("ID3")) || (len(h) >= 2 && h[0] == 0xFF && h[1]&0xE0 == 0xE0), "MP3")
	case ".opus", ".ogg":
		return ok(bytes.HasPrefix(h, []byte("OggS")), "Ogg")
	case ".wav":
		return ok(bytes.HasPrefix(h, []byte("RIFF")), "WAV")
	}
	return nil
}
