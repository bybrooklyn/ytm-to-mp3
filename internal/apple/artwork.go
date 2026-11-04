package apple

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple/download"
)

// FetchArtwork downloads the cover art for t at px by px pixels as JPEG bytes.
// Apple artwork URLs are templates containing "{w}x{h}"; returns nil, nil when
// the track has no artwork.
func (c *Client) FetchArtwork(ctx context.Context, t Track, px int) ([]byte, error) {
	if t.ArtworkURL == "" || !strings.Contains(t.ArtworkURL, "{w}x{h}") {
		return nil, nil
	}
	url := strings.ReplaceAll(t.ArtworkURL, "{w}x{h}", fmt.Sprintf("%dx%d", px, px))
	// Template may end with a format suffix like "bb.jpg"; force jpeg.
	if i := strings.LastIndex(url, "."); i >= 0 {
		url = url[:i] + ".jpg"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := download.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artwork: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
