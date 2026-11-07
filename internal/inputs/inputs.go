// Package inputs turns whatever the user gave ytm (arguments, a pasted block of links, a
// text file of links, paths to exports) into one clean, de-duplicated list, and says which
// lines it could not use, by line, without stopping the rest.
package inputs

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/spotify"
	"github.com/bybrooklyn/mfetchy/internal/takeout"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
)

// Kind says which part of ytm handles an input.
type Kind string

const (
	YouTube Kind = "youtube" // a YouTube / YouTube Music link (video, playlist, album)
	Spotify Kind = "spotify"
	Apple   Kind = "apple"
	Tidal   Kind = "tidal"
	Web     Kind = "web" // SoundCloud or Bandcamp
	File    Kind = "file"
)

// Item is one usable input.
type Item struct {
	Value string // the cleaned link, or the path of a file
	Kind  Kind
	Line  int    // 1-based line it came from (counting across everything given)
	Key   string // what makes two items the same thing
}

// Bad is a piece of input that could not be used.
type Bad struct {
	Line   int
	Text   string
	Reason string
}

func (b Bad) String() string { return fmt.Sprintf("line %d: %q · %s", b.Line, b.Text, b.Reason) }

// Result is a parsed batch.
type Result struct {
	Items []Item
	Bad   []Bad
	Dups  int // inputs dropped because an earlier one was the same
}

// Values returns the cleaned inputs, in order, ready for download().
func (r Result) Values() []string {
	out := make([]string, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.Value
	}
	return out
}

// listExts are files read as a list of links rather than as a library export.
var listExts = map[string]bool{".txt": true, ".list": true, ".urls": true, ".md": true, ".url": false}

// Parse reads args. Each argument may hold several lines and several links; a path to a
// .txt file contributes its lines (links, blank lines and "# comments"). Order is kept.
func Parse(args []string) Result {
	p := &parser{seen: map[string]bool{}}
	for _, a := range args {
		p.text(a, 0)
	}
	return p.r
}

type parser struct {
	r    Result
	seen map[string]bool
	line int
}

func (p *parser) text(s string, depth int) {
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		p.line++
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		// A whole line that is an existing file (spaces and all, as a dragged-in path is).
		if f, ok := existingFile(ln); ok {
			p.file(f, ln, depth)
			continue
		}
		for _, tok := range tokens(ln) {
			p.token(tok, ln, depth)
		}
	}
}

func (p *parser) file(path, raw string, depth int) {
	if listExts[strings.ToLower(filepath.Ext(path))] && depth == 0 {
		b, err := os.ReadFile(path)
		if err != nil {
			p.bad(raw, err.Error())
			return
		}
		p.text(string(b), depth+1)
		return
	}
	p.add(Item{Value: path, Kind: File, Line: p.line, Key: "file:" + path})
}

func (p *parser) token(tok, ln string, depth int) {
	tok = trimPunct(tok)
	if tok == "" {
		return
	}
	if f, ok := existingFile(tok); ok {
		p.file(f, tok, depth)
		return
	}
	if link, ok := Normalize(tok); ok {
		kind, key := classify(link)
		if kind == "" {
			p.bad(tok, "not a site ytm can download from (YouTube, Apple Music, Spotify, Tidal, SoundCloud, Bandcamp)")
			return
		}
		p.add(Item{Value: link, Kind: kind, Line: p.line, Key: key})
		return
	}
	if len(tok) == 11 && model.ValidID(tok) { // a bare video id
		l := "https://www.youtube.com/watch?v=" + tok
		p.add(Item{Value: l, Kind: YouTube, Line: p.line, Key: "yt:" + tok})
		return
	}
	reason := "not a link and no such file"
	if strings.ContainsAny(tok, `/\`) || strings.HasPrefix(tok, "~") {
		reason = "no such file"
	}
	p.bad(tok, reason)
}

func (p *parser) bad(text, reason string) {
	if len(text) > 80 {
		text = text[:79] + "…"
	}
	p.r.Bad = append(p.r.Bad, Bad{Line: p.line, Text: text, Reason: reason})
}

func (p *parser) add(it Item) {
	if p.seen[it.Key] {
		p.r.Dups++
		return
	}
	p.seen[it.Key] = true
	p.r.Items = append(p.r.Items, it)
}

// existingFile resolves a path the way users write them (quotes, ~, file://, escaped spaces).
func existingFile(s string) (string, bool) {
	s = strings.Trim(s, `"'`)
	if s == "" || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", false
	}
	for _, c := range []string{s, takeout.CleanPath(s)} {
		if st, err := os.Stat(c); err == nil && c != "" {
			_ = st
			return c, true
		}
	}
	return "", false
}

// tokens splits a line on spaces and commas, keeping quoted parts together.
func tokens(ln string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range ln {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == ',' || r == ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// trimPunct removes what chat apps and prose put around a pasted link: <...>, (...), a
// trailing full stop, quote or bracket.
func trimPunct(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := s
		switch {
		case strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">"):
			t = t[1 : len(t)-1]
		case strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")"):
			t = t[1 : len(t)-1]
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			t = t[1 : len(t)-1]
		case strings.HasSuffix(t, ")") && strings.Count(t, ")") > strings.Count(t, "("):
			t = t[:len(t)-1]
		case strings.HasSuffix(t, "]") && strings.Count(t, "]") > strings.Count(t, "["):
			t = t[:len(t)-1]
		default:
			t = strings.TrimRight(t, `.,;:!?"'>`)
			t = strings.TrimLeft(t, `"'<(`)
		}
		if t == s {
			return s
		}
		s = t
	}
}

// trackingParams are dropped from every link: they identify the sharer, not the music.
var trackingParams = map[string]bool{
	"si": true, "feature": true, "pp": true, "fbclid": true, "gclid": true, "igshid": true, "ref": true,
	"context": true, "utm_source": true, "utm_medium": true, "utm_campaign": true, "utm_term": true,
	"utm_content": true, "utm_id": true, "uo": true, "at": true, "ct": true, "app": true, "ls": true,
	"l": true, "t": true, "start_radio": true, "index": true, "si_": true, "ab_channel": true,
	"mc_cid": true, "mc_eid": true, "_branch_match_id": true, "dl_branch": true, "nd": true,
	"trackid": true, "fromurl": true,
}

// Normalize makes a pasted link canonical: a scheme, a lowercase host, no tracking
// parameters or fragment, youtu.be and music.youtube.com written the usual way. It
// returns false when s isn't a link (or a spotify: URI / scheme-less YouTube link).
func Normalize(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "spotify:"); ok { // spotify:track:ID
		parts := strings.Split(rest, ":")
		if len(parts) == 2 && parts[1] != "" {
			return "https://open.spotify.com/" + parts[0] + "/" + parts[1], true
		}
		return "", false
	}
	if low := strings.ToLower(s); strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		s = low[:strings.Index(low, "://")] + s[strings.Index(low, "://"):]
	} else {
		ok := false
		for _, h := range []string{"youtube.com/", "youtu.be/", "music.youtube.com/", "www.youtube.com/", "m.youtube.com/",
			"open.spotify.com/", "music.apple.com/", "tidal.com/", "listen.tidal.com/", "soundcloud.com/", "on.soundcloud.com/"} {
			if strings.HasPrefix(strings.ToLower(s), h) {
				ok = true
				break
			}
		}
		if strings.Contains(s, ".bandcamp.com") && !strings.ContainsAny(s, " ") && strings.Contains(s, "/") {
			ok = true
		}
		if !ok {
			if len(s) >= 18 && (strings.HasPrefix(s, "PL") || strings.HasPrefix(s, "OLAK5uy_") || strings.HasPrefix(s, "RDCLAK")) &&
				!strings.ContainsAny(s, `/\. `) {
				return "https://www.youtube.com/playlist?list=" + s, true
			}
			return "", false
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	u.Scheme = "https"
	u.Fragment = ""
	host := strings.ToLower(u.Hostname())
	u.Host = host
	q := u.Query()
	for k := range q {
		if trackingParams[strings.ToLower(k)] || strings.HasPrefix(strings.ToLower(k), "utm_") {
			// Apple's i= (the song within an album) and YouTube's v= / list= matter: not in the set.
			q.Del(k)
		}
	}
	h := strings.TrimPrefix(host, "www.")
	switch {
	case h == "youtu.be":
		id := strings.Trim(u.Path, "/")
		nq := url.Values{"v": {id}}
		if l := q.Get("list"); l != "" {
			nq.Set("list", l)
		}
		u = &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: nq.Encode()}
		q = nil
	case h == "youtube.com" || h == "m.youtube.com" || h == "music.youtube.com":
		if h != "music.youtube.com" {
			u.Host = "www.youtube.com"
		}
	case h == "open.spotify.com":
		u.Path = intlRe(u.Path)
		q = url.Values{}
	}
	if q != nil {
		u.RawQuery = q.Encode()
	}
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String(), true
}

// intlRe drops Spotify's /intl-xx/ language prefix.
func intlRe(p string) string {
	if strings.HasPrefix(p, "/intl-") {
		if _, rest, ok := strings.Cut(p[1:], "/"); ok {
			return "/" + rest
		}
	}
	return p
}

// classify names the handler for a normalised link and a key for de-duplicating it.
func classify(link string) (Kind, string) {
	u, err := url.Parse(link)
	if err != nil {
		return "", ""
	}
	key := strings.ToLower(u.Hostname() + u.Path)
	switch {
	case spotify.IsURL(link):
		return Spotify, "spotify:" + strings.TrimPrefix(key, "open.spotify.com")
	case apple.IsURL(link):
		return Apple, "apple:" + key + "?i=" + u.Query().Get("i")
	case tidal.IsURL(link):
		return Tidal, "tidal:" + strings.TrimPrefix(strings.TrimPrefix(key, "listen.tidal.com"), "tidal.com")
	case engine.WebSite(link) != "":
		return Web, "web:" + key
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if h == "youtube.com" || h == "music.youtube.com" || h == "m.youtube.com" || h == "youtu.be" {
		if v := u.Query().Get("v"); v != "" && model.ValidID(v) && u.Query().Get("list") == "" {
			return YouTube, "yt:" + v
		}
		if l := u.Query().Get("list"); l != "" {
			return YouTube, "ytlist:" + l
		}
		if strings.HasPrefix(u.Path, "/shorts/") {
			return YouTube, "yt:" + strings.TrimPrefix(u.Path, "/shorts/")
		}
		if v := u.Query().Get("v"); v != "" {
			return YouTube, "yt:" + v
		}
		return YouTube, "yt:" + key + "?" + u.RawQuery
	}
	return "", ""
}
