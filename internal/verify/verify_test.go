package verify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/jobs"
)

func write(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFile(t *testing.T) {
	flac := append([]byte("fLaC"), make([]byte, 4096)...)
	m4a := append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p'}, make([]byte, 4096)...)
	old := Probe
	defer func() { Probe = old }()
	dur := 200.0
	Probe = func(context.Context, string, string) (float64, error) { return dur, nil }

	cases := []struct {
		name string
		file []byte
		ext  string
		want Want
		bad  bool
	}{
		{"good flac", flac, ".flac", Want{Ext: ".flac", Duration: 200}, false},
		{"good m4a, duration 3% off", m4a, ".m4a", Want{Ext: ".m4a", Duration: 206}, false},
		{"empty", nil, ".flac", Want{Ext: ".flac"}, true},
		{"tiny", []byte("fLaC"), ".flac", Want{Ext: ".flac"}, true},
		{"html where flac expected", append([]byte("<html>"), make([]byte, 4096)...), ".flac", Want{Ext: ".flac"}, true},
		{"truncated: half the length", flac, ".flac", Want{Ext: ".flac", Duration: 400}, true},
		{"unknown duration is not checked", flac, ".flac", Want{Ext: ".flac"}, false},
	}
	for _, c := range cases {
		err := File(context.Background(), "ffprobe", write(t, "a"+c.ext, c.file), c.want)
		var ce *jobs.CorruptError
		if c.bad != errors.As(err, &ce) {
			t.Errorf("%s: err = %v, bad = %v", c.name, err, c.bad)
		}
	}
	_ = bytes.MinRead
}
