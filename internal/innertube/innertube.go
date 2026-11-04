// Package innertube asks YouTube's own player API for a video's audio streams, in one
// request, without yt-dlp: no Python process, no watch-page download. It only uses
// "JS-less" app clients, whose stream URLs are plain (no signature to decipher, no "n"
// challenge), so anything it can't handle is reported as an error and the caller falls
// back to yt-dlp. The client constants come from yt-dlp and need refreshing when YouTube
// changes them; see Clients.
package innertube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Client is an InnerTube client identity.
type Client struct {
	Name, Version, UserAgent string
	DeviceMake, DeviceModel  string
	OSName, OSVersion        string
	ID                       int // X-YouTube-Client-Name
}

// Clients are tried in order. Copied from yt-dlp 2026.10.26
// (yt_dlp/extractor/youtube/_base.py, INNERTUBE_CLIENTS), whose JS-less fallback is
// visionos. When lookups start failing, compare with the current yt-dlp release.
var Clients = []Client{
	{
		Name: "VISIONOS", Version: "1.02", ID: 101,
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
		DeviceMake: "Apple", DeviceModel: "RealityDevice17,1", OSName: "visionOS", OSVersion: "26.5.23O471",
	},
}

// Format is one audio stream with a plain URL.
type Format struct {
	Itag          int
	URL           string
	MimeType      string // e.g. `audio/webm; codecs="opus"`
	Bitrate       int
	ContentLength int64
}

// Codec is the format's codec ("opus", "mp4a.40.2", ...).
func (f Format) Codec() string {
	if _, c, ok := strings.Cut(f.MimeType, `codecs="`); ok {
		c, _, _ = strings.Cut(c, `"`)
		return c
	}
	return ""
}

// Ext is the container the stream comes in ("webm", "m4a").
func (f Format) Ext() string {
	switch {
	case strings.HasPrefix(f.MimeType, "audio/webm"):
		return "webm"
	case strings.HasPrefix(f.MimeType, "audio/mp4"):
		return "m4a"
	}
	return ""
}

// Video is what the player API says about a video.
type Video struct {
	ID, Title, Author string
	Duration          float64
	Description       string
	Thumbnail         string // largest
	Audio             []Format
	VisitorData       string // to send with later requests on the same exit
	UserAgent         string // the client's: stream requests should carry it too
}

// Unplayable is a player response that isn't "OK" (login, age gate, region, removed...).
type Unplayable struct{ Status, Reason string }

func (e *Unplayable) Error() string {
	return strings.TrimSpace("youtube: " + strings.ToLower(e.Status) + ": " + e.Reason)
}

// ErrNoAudio means the response had no audio stream with a plain URL.
var ErrNoAudio = errors.New("youtube: no plain audio stream")

// HTTPError is a non-200 answer from the API (429 = rate limited).
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("youtube player API: HTTP %d", e.Status) }

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Player asks c's player endpoint about id through hc (the exit the stream will be
// downloaded through: stream URLs are bound to the requesting IP).
func Player(ctx context.Context, hc *http.Client, c Client, id, visitorData string) (*Video, error) {
	if !idRe.MatchString(id) {
		return nil, errors.New("invalid video id")
	}
	body, _ := json.Marshal(requestBody(c, id, visitorData))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.youtube.com/youtubei/v1/player?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Origin", "https://www.youtube.com")
	req.Header.Set("X-YouTube-Client-Name", strconv.Itoa(c.ID))
	req.Header.Set("X-YouTube-Client-Version", c.Version)
	if visitorData != "" {
		req.Header.Set("X-Goog-Visitor-Id", visitorData)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, &HTTPError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	v, err := parse(b)
	if v != nil {
		v.UserAgent = c.UserAgent
	}
	return v, err
}

func requestBody(c Client, id, visitorData string) map[string]any {
	client := map[string]any{
		"clientName": c.Name, "clientVersion": c.Version, "userAgent": c.UserAgent,
		"hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0,
	}
	for k, v := range map[string]string{"deviceMake": c.DeviceMake, "deviceModel": c.DeviceModel, "osName": c.OSName, "osVersion": c.OSVersion, "visitorData": visitorData} {
		if v != "" {
			client[k] = v
		}
	}
	return map[string]any{
		"context": map[string]any{"client": client},
		"videoId": id,
		"playbackContext": map[string]any{
			"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"},
		},
		"contentCheckOk": true,
		"racyCheckOk":    true,
	}
}

type playerResponse struct {
	ResponseContext struct {
		VisitorData string `json:"visitorData"`
	} `json:"responseContext"`
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		VideoID          string `json:"videoId"`
		Title            string `json:"title"`
		Author           string `json:"author"`
		LengthSeconds    string `json:"lengthSeconds"`
		ShortDescription string `json:"shortDescription"`
		Thumbnail        struct {
			Thumbnails []struct {
				URL           string `json:"url"`
				Width, Height int
			} `json:"thumbnails"`
		} `json:"thumbnail"`
	} `json:"videoDetails"`
	StreamingData struct {
		AdaptiveFormats []struct {
			Itag            int    `json:"itag"`
			URL             string `json:"url"`
			SignatureCipher string `json:"signatureCipher"`
			MimeType        string `json:"mimeType"`
			Bitrate         int    `json:"bitrate"`
			ContentLength   string `json:"contentLength"`
		} `json:"adaptiveFormats"`
	} `json:"streamingData"`
}

func parse(b []byte) (*Video, error) {
	var pr playerResponse
	if err := json.Unmarshal(b, &pr); err != nil {
		return nil, fmt.Errorf("youtube player API: unreadable answer: %w", err)
	}
	v := &Video{VisitorData: pr.ResponseContext.VisitorData}
	if pr.PlayabilityStatus.Status != "OK" {
		return v, &Unplayable{pr.PlayabilityStatus.Status, pr.PlayabilityStatus.Reason}
	}
	d := pr.VideoDetails
	v.ID, v.Title, v.Author, v.Description = d.VideoID, d.Title, d.Author, d.ShortDescription
	v.Duration, _ = strconv.ParseFloat(d.LengthSeconds, 64)
	area := 0
	for _, t := range d.Thumbnail.Thumbnails {
		if a := t.Width * t.Height; a >= area && strings.HasPrefix(t.URL, "https://") {
			v.Thumbnail, area = t.URL, a
		}
	}
	for _, f := range pr.StreamingData.AdaptiveFormats {
		if !strings.HasPrefix(f.MimeType, "audio/") || f.URL == "" || f.SignatureCipher != "" {
			continue
		}
		u, err := url.Parse(f.URL)
		if err != nil || u.Scheme != "https" || !isGoogleVideo(u.Hostname()) {
			continue
		}
		n, _ := strconv.ParseInt(f.ContentLength, 10, 64)
		v.Audio = append(v.Audio, Format{Itag: f.Itag, URL: f.URL, MimeType: f.MimeType, Bitrate: f.Bitrate, ContentLength: n})
	}
	if len(v.Audio) == 0 {
		return v, ErrNoAudio
	}
	return v, nil
}

func isGoogleVideo(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
}

// BestAudio picks the stream to download: the first preferred itag present, else the
// highest bitrate of the preferred codec ("opus"/"mp4a", "" = any).
func BestAudio(v *Video, preferItags []int, codec string) (Format, bool) {
	for _, it := range preferItags {
		for _, f := range v.Audio {
			if f.Itag == it {
				return f, true
			}
		}
	}
	var best Format
	found := false
	for _, f := range v.Audio {
		if codec != "" && !strings.HasPrefix(f.Codec(), codec) {
			continue
		}
		if !found || f.Bitrate > best.Bitrate {
			best, found = f, true
		}
	}
	if !found && codec != "" {
		return BestAudio(v, nil, "")
	}
	return best, found
}

// Music is what YouTube Music's auto-generated description says about a song:
//
//	Provided to YouTube by DistroKid
//
//	Wet Hands · C418
//
//	Minecraft - Volume Alpha
//
//	℗ 2011 C418
//
//	Released on: 2011-03-04
type Music struct {
	Title   string
	Artists []string
	Album   string
	Date    string // YYYY-MM-DD
	Year    int
}

var (
	releasedRe = regexp.MustCompile(`(?m)^Released on: (\d{4}-\d{2}-\d{2})\s*$`)
	phonoRe    = regexp.MustCompile(`(?m)^℗ (\d{4})\b`)
)

// ParseMusic reads an auto-generated description; ok is false for anything else.
func ParseMusic(desc string) (m Music, ok bool) {
	if !strings.HasPrefix(desc, "Provided to YouTube by ") {
		return m, false
	}
	var paras []string
	for _, p := range strings.Split(strings.ReplaceAll(desc, "\r", ""), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			paras = append(paras, p)
		}
	}
	if len(paras) < 2 {
		return m, false
	}
	parts := strings.Split(paras[1], " · ")
	m.Title = strings.TrimSpace(parts[0])
	seen := map[string]bool{}
	for _, a := range parts[1:] {
		if a = strings.TrimSpace(a); a != "" && !seen[strings.ToLower(a)] {
			seen[strings.ToLower(a)] = true
			m.Artists = append(m.Artists, a)
		}
	}
	if len(paras) > 2 && !strings.HasPrefix(paras[2], "℗") && !strings.HasPrefix(paras[2], "Released on:") {
		m.Album = paras[2]
	}
	if x := releasedRe.FindStringSubmatch(desc); x != nil {
		m.Date = x[1]
		m.Year, _ = strconv.Atoi(x[1][:4])
	} else if x := phonoRe.FindStringSubmatch(desc); x != nil {
		m.Year, _ = strconv.Atoi(x[1])
	}
	return m, m.Title != ""
}
