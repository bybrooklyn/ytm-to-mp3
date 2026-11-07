package tidal

import (
	"time"

	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bybrooklyn/ytm-to-mp3/internal/httpfetch"
	"github.com/bybrooklyn/ytm-to-mp3/internal/jobs"
	"net/url"
	"strconv"
	"strings"
)

// Quality is how much the user asks for.
type Quality string

const (
	// Lossless is 16-bit/44.1 kHz FLAC and nothing else.
	Lossless Quality = "lossless"
	// Best also tries the hi-res tier first, and takes it only when Tidal serves it as a
	// plain FLAC file; otherwise it is Lossless.
	Best Quality = "best"
)

// tiers are the playbackinfo audioquality values asked for, best first.
func (q Quality) tiers() []string {
	if q == Best {
		return []string{"HI_RES_LOSSLESS", "LOSSLESS"}
	}
	return []string{"LOSSLESS"}
}

// Refusals ytm makes on purpose: what it will not fetch.
var (
	ErrEncrypted = errors.New("Tidal served this stream encrypted, which ytm does not fetch")
	ErrDASH      = errors.New("Tidal served this stream as a DASH manifest, which ytm does not fetch")
	ErrLossy     = errors.New("Tidal served a lossy stream, not lossless")
)

// Stream is one plain audio file ready to download.
type Stream struct {
	URL     string
	Tier    string // the audioquality asked for
	Quality string // what Tidal says it is serving (LOSSLESS, HI_RES_LOSSLESS)
	Codec   string
}

// Label says what the user gets, for the output.
func (s Stream) Label() string {
	if strings.HasPrefix(s.Quality, "HI_RES") {
		return "FLAC hi-res"
	}
	return "FLAC 16-bit/44.1 kHz"
}

// NoStreamError says why no plain lossless stream was available, tier by tier.
type NoStreamError struct{ Reasons []string }

func (e *NoStreamError) Error() string {
	return "no plain lossless stream from Tidal (" + strings.Join(e.Reasons, "; ") + ")"
}

type playbackInfo struct {
	AudioQuality string `json:"audioQuality"`
	MimeType     string `json:"manifestMimeType"`
	Manifest     string `json:"manifest"`
}

type btsManifest struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	KeyID          string   `json:"keyId"`
	URLs           []string `json:"urls"`
}

// ParseManifest reads a playbackinfo answer into a Stream, refusing what ytm won't fetch:
// DASH manifests, encrypted streams, and anything that is not FLAC.
func ParseManifest(mime, manifest string) (Stream, error) {
	switch {
	case strings.Contains(mime, "dash+xml"):
		return Stream{}, ErrDASH
	case !strings.Contains(mime, "vnd.tidal.bts"):
		return Stream{}, fmt.Errorf("unknown Tidal manifest type %q", mime)
	}
	raw, err := base64.StdEncoding.DecodeString(manifest)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(manifest)
	}
	if err != nil {
		return Stream{}, errors.New("unreadable Tidal manifest")
	}
	var m btsManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Stream{}, errors.New("unreadable Tidal manifest")
	}
	if e := strings.ToUpper(strings.TrimSpace(m.EncryptionType)); e != "" && e != "NONE" || strings.TrimSpace(m.KeyID) != "" {
		return Stream{}, ErrEncrypted
	}
	codec := strings.ToLower(strings.SplitN(m.Codecs, ".", 2)[0])
	if codec != "flac" && !strings.Contains(strings.ToLower(m.MimeType), "flac") {
		return Stream{}, ErrLossy
	}
	if len(m.URLs) == 0 || !strings.HasPrefix(m.URLs[0], "https://") && !strings.HasPrefix(m.URLs[0], "http://") {
		return Stream{}, errors.New("Tidal's manifest had no download address")
	}
	return Stream{URL: m.URLs[0], Codec: "flac"}, nil
}

// Stream resolves a song to a plain FLAC file, trying the tiers q allows from the best
// down. A tier that comes back encrypted, as DASH or lossy is skipped, never worked around;
// when none is usable the error says why for each.
func (c *Client) Stream(ctx context.Context, id int64, q Quality) (Stream, error) {
	var reasons []string
	for _, tier := range q.tiers() {
		var pi playbackInfo
		err := c.get(ctx, "/tracks/"+strconv.FormatInt(id, 10)+"/playbackinfopostpaywall", url.Values{
			"audioquality": {tier}, "playbackmode": {"STREAM"}, "assetpresentation": {"FULL"},
		}, &pi)
		if err != nil {
			if errors.Is(err, ErrSignedOut) {
				return Stream{}, err
			}
			reasons = append(reasons, tier+": "+err.Error())
			continue
		}
		if !strings.Contains(pi.AudioQuality, "LOSSLESS") {
			reasons = append(reasons, tier+": your subscription served "+firstNonEmpty(pi.AudioQuality, "a lower tier"))
			continue
		}
		s, err := ParseManifest(pi.MimeType, pi.Manifest)
		if err != nil {
			reasons = append(reasons, tier+": "+err.Error())
			continue
		}
		s.Tier, s.Quality = tier, pi.AudioQuality
		return s, nil
	}
	return Stream{}, &NoStreamError{Reasons: reasons}
}

// fetchBackoff is the first delay between download retries (tests make it tiny).
var fetchBackoff = time.Second

// Download fetches the stream to dst through a resumable ".part" file next to it: a dropped
// connection continues where it stopped (the CDN supports Range), a download that is
// cancelled keeps its part for the next run, and dst only appears once the whole file has
// arrived and looks like FLAC. A download that fails for good leaves nothing at dst.
// progress, when set, gets bytes done and the total (0 if unknown).
func (c *Client) Download(ctx context.Context, s Stream, dst string, progress func(done, total int64)) error {
	// The address is a signed CDN link: no Authorization header goes to it.
	hc := *c.HTTP
	hc.Timeout = 0 // a lossless song takes longer than the API timeout; ctx bounds it
	err := httpfetch.Download(ctx, s.URL, dst, httpfetch.Options{
		Client:   &hc,
		Progress: progress,
		Backoff:  fetchBackoff,
		Check: func(first []byte) error {
			if !bytes.HasPrefix(first, []byte("fLaC")) && !bytes.HasPrefix(first, []byte("ID3")) {
				return errors.New("the download isn't a FLAC file")
			}
			return nil
		},
	})
	var se *jobs.StatusError
	if errors.As(err, &se) {
		return &APIError{Status: se.Status, Message: "the audio download was refused"}
	}
	return err
}
