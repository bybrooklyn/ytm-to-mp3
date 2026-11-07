package tidal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

// Download fetches the stream to dst. The file is removed again if anything goes wrong
// (including cancellation), so a partial file is never left looking finished.
// progress, when set, gets bytes done and the total (0 if unknown).
func (c *Client) Download(ctx context.Context, s Stream, dst string, progress func(done, total int64)) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	// The address is a signed CDN link: no Authorization header goes to it.
	hc := *c.HTTP
	hc.Timeout = 0 // a lossless song takes longer than the API timeout; ctx bounds it
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Message: "the audio download was refused"}
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 128<<10)
	first := true
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if first {
				first = false
				if !bytes.HasPrefix(buf[:n], []byte("fLaC")) && !bytes.HasPrefix(buf[:n], []byte("ID3")) {
					return errors.New("the download isn't a FLAC file")
				}
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if total > 0 && done != total {
		return fmt.Errorf("the download stopped early (%d of %d bytes)", done, total)
	}
	return ctx.Err()
}
