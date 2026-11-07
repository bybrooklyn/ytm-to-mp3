package lyrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NetEaseName and NetEaseBase: NetEase Cloud Music's public web API. This is UNOFFICIAL
// (undocumented, no key, no promises) and a third party outside the music services ytm
// otherwise uses, so it is off until switched on in Settings. It gets the title and artist
// as the search text and, for the three best hits, their song ids; nothing else.
const (
	NetEaseName = "NetEase (unofficial)"
	NetEaseBase = "https://music.163.com"
	topHits     = 3
)

type NetEase struct {
	Client *http.Client
	Base   string
}

func (p *NetEase) Name() string { return NetEaseName }

func (p *NetEase) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return newClient()
}

func (p *NetEase) base() string {
	if p.Base != "" {
		return p.Base
	}
	return NetEaseBase
}

type neteaseSong struct {
	ID       int64
	Title    string
	Artist   string
	Album    string
	Duration time.Duration
}

func (p *NetEase) Fetch(ctx context.Context, q Query) ([]Candidate, error) {
	text := strings.TrimSpace(searchTitle(q.Title) + " " + primaryArtist(q.Artist))
	if text == "" {
		return nil, nil
	}
	body, err := get(ctx, p.client(), NetEaseName, p.base(), "/api/search/get",
		url.Values{"s": {text}, "type": {"1"}, "limit": {"5"}})
	if err != nil || body == nil {
		return nil, err
	}
	songs := parseNetEaseSearch(body)
	if len(songs) > topHits {
		songs = songs[:topHits]
	}
	var out []Candidate
	for _, s := range songs {
		b, err := get(ctx, p.client(), NetEaseName, p.base(), "/api/song/lyric",
			url.Values{"id": {strconv.FormatInt(s.ID, 10)}, "lv": {"1"}, "kv": {"1"}, "tv": {"-1"}})
		if err != nil {
			return out, err
		}
		if c, ok := parseNetEaseLyric(s, b); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// parseNetEaseSearch reads {"result":{"songs":[{"id":1,"name":"..","artists":[{"name":".."}],
// "album":{"name":".."},"duration":213000}]}}.
func parseNetEaseSearch(body []byte) []neteaseSong {
	var v struct {
		Result struct {
			Songs []struct {
				ID       int64 `json:"id"`
				Name     string
				Duration float64
				Artists  []struct{ Name string }
				Album    struct{ Name string }
			}
		}
	}
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	var out []neteaseSong
	for _, s := range v.Result.Songs {
		var names []string
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		out = append(out, neteaseSong{ID: s.ID, Title: s.Name, Artist: strings.Join(names, ", "), Album: s.Album.Name,
			Duration: time.Duration(s.Duration * float64(time.Millisecond))})
	}
	return out
}

// parseNetEaseLyric reads {"lrc":{"lyric":"[00:01.00].."},"nolyric":false}. The translation
// (tlyric) is ignored: mixing it in would double every line. NetEase puts credit lines
// ("作词 : ...") at the top; they are not lyrics.
func parseNetEaseLyric(s neteaseSong, body []byte) (Candidate, bool) {
	var v struct {
		NoLyric     bool `json:"nolyric"`
		Uncollected bool `json:"uncollected"`
		LRC         struct{ Lyric string }
	}
	if body == nil || json.Unmarshal(body, &v) != nil || v.NoLyric || v.Uncollected || v.LRC.Lyric == "" {
		return Candidate{}, false
	}
	lines, _ := ParseLRC(v.LRC.Lyric)
	lines = dropCredits(lines)
	c := Candidate{Provider: NetEaseName, Title: s.Title, Artist: s.Artist, Album: s.Album, Duration: s.Duration, Synced: lines}
	if len(lines) == 0 {
		return Candidate{}, false
	}
	return c, true
}

var creditKeys = setOf("作词", "作曲", "编曲", "制作", "制作人", "词", "曲", "混音", "母带", "监制")

func dropCredits(lines []Line) []Line {
	skip := 0
	for _, l := range lines {
		t := strings.TrimSpace(l.Text)
		k, _, ok := strings.Cut(strings.ReplaceAll(t, "：", ":"), ":")
		if t != "" && !(ok && creditKeys[strings.TrimSpace(k)]) {
			break
		}
		skip++
	}
	return lines[skip:]
}
