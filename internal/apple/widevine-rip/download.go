package widevinerip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple/download"
	"github.com/bybrooklyn/ytm-to-mp3/internal/apple/fsutil"
)

const (
	streamDownloadConcurrency = 10
	streamDownloadMaxRetries  = 3
)

// Progress, when set, receives the number of bytes read as downloads proceed.
// It lets the host render progress through its own UI; the vendored upstream
// drew a terminal progress bar here, which does not fit ytm's live display.
var Progress func(n int)

func reportProgress(n int) {
	if Progress != nil {
		Progress(n)
	}
}

// progressWriter reports bytes written through the Progress hook.
type progressWriter struct{}

func (progressWriter) Write(p []byte) (int, error) {
	reportProgress(len(p))
	return len(p), nil
}

// Stream is everything needed to download one segmented stream: its URLs
// (init segment first) and, unless it is served in the clear, its content key.
type Stream struct {
	// Key is the content key; empty for a clear stream.
	Key  []byte
	URLs []string
}

// DownloadStream downloads every segment of stream concurrently into
// outputPath as one fragmented MP4, decrypting it first when the stream has
// a key.
func DownloadStream(ctx context.Context, stream Stream, outputPath string) error {
	if len(stream.URLs) == 0 {
		return errors.New("no download URLs")
	}

	tempFile, err := os.CreateTemp("", "enc_stream-*.mp4")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	err = download.DownloadSegments(ctx, download.Client, stream.URLs, tempFile, download.SegmentConfig{
		Concurrency: streamDownloadConcurrency,
		MaxRetries:  streamDownloadMaxRetries,
		Progress:    reportProgress,
	})
	if err != nil {
		return fmt.Errorf("download stream: %w", err)
	}
	if _, err := tempFile.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind temp file: %w", err)
	}

	if len(stream.Key) == 0 {
		return copyToFile(tempFile, outputPath)
	}
	if err := DecryptMP4ToFile(tempFile, stream.Key, outputPath); err != nil {
		return fmt.Errorf("decrypt stream: %w", err)
	}
	return nil
}

// copyToFile writes r to outputPath through a temporary file that is renamed
// into place on success, like DecryptMP4ToFile.
func copyToFile(r io.Reader, outputPath string) error {
	tempFile, err := fsutil.CreateOutputTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tempPath := tempFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := io.Copy(tempFile, r); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("replace output: %w", err)
	}
	committed = true
	return nil
}

// downloadWithProgress downloads url into memory, reporting bytes through the
// Progress hook.
func downloadWithProgress(ctx context.Context, url string) (*bytes.Buffer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create media request: %w", err)
	}
	resp, err := download.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download media: %s", resp.Status)
	}

	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(io.TeeReader(resp.Body, progressWriter{})); err != nil {
		return nil, fmt.Errorf("download media: %w", err)
	}
	return &buffer, nil
}
