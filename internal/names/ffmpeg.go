package names

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// FFPath returns p in a form ffmpeg/ffprobe can never mistake for an option or a URL
// scheme: an absolute path with an explicit "file:" prefix.
func FFPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return "file:" + p
}

// ImageDecoder sniffs the file at path and returns the ffmpeg decoder name for the only
// cover formats we accept (mjpeg, png, webp), or "" for anything else.
func ImageDecoder(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	return imageDecoderOf(head[:n])
}

func imageDecoderOf(h []byte) string {
	switch {
	case bytes.HasPrefix(h, []byte{0xFF, 0xD8, 0xFF}):
		return "mjpeg"
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(h) >= 12 && bytes.Equal(h[:4], []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP")):
		return "webp"
	}
	return ""
}
