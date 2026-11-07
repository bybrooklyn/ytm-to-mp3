package ytmusic

import (
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

// Everything here works on the decoded JSON as any (maps and slices), looking for a few
// renderer objects by name.

type obj = map[string]any

func asObj(v any) obj { o, _ := v.(obj); return o }

func asList(v any) []any { l, _ := v.([]any); return l }

// get follows a path of object keys and list indexes ("a", "b", 0, "c"); nil if it breaks.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = asObj(v)[k]
		case int:
			l := asList(v)
			if k < 0 || k >= len(l) {
				return nil
			}
			v = l[k]
		}
		if v == nil {
			return nil
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

// walk calls f on every object under v (v included).
func walk(v any, f func(key string, o obj)) {
	switch x := v.(type) {
	case obj:
		for k, c := range x {
			if o := asObj(c); o != nil {
				f(k, o)
			}
			walk(c, f)
		}
	case []any:
		for _, c := range x {
			walk(c, f)
		}
	}
}

// firstString finds the first value of key anywhere under v (breadth is not guaranteed,
// which is fine for ids that appear once per item).
func firstString(v any, key string) string {
	var found string
	var rec func(any) bool
	rec = func(v any) bool {
		switch x := v.(type) {
		case obj:
			if s := str(x[key]); s != "" {
				found = s
				return true
			}
			for _, c := range x {
				if rec(c) {
					return true
				}
			}
		case []any:
			for _, c := range x {
				if rec(c) {
					return true
				}
			}
		}
		return false
	}
	rec(v)
	return found
}

// continuation returns the token that asks for the next page, "" when there is none.
func continuation(resp any) string {
	var tok string
	walk(resp, func(k string, o obj) {
		if tok != "" {
			return
		}
		switch k {
		case "nextContinuationData":
			tok = str(o["continuation"])
		case "continuationItemRenderer":
			tok = str(get(o, "continuationEndpoint", "continuationCommand", "token"))
		}
	})
	return tok
}

// songs returns the songs in the named shelves of resp. A continuation response has no
// shelf wrapper that is worth telling apart, so when none of the shelves is present the
// list items are taken from wherever they are.
func songs(resp any, shelves ...string) []model.Track {
	want := map[string]bool{}
	for _, s := range shelves {
		want[s] = true
	}
	var out []model.Track
	var any_ bool
	walk(resp, func(k string, o obj) {
		if want[k] {
			any_ = true
		}
	})
	collect := func(o obj) {
		for _, it := range asList(o["contents"]) {
			if r := asObj(asObj(it)["musicResponsiveListItemRenderer"]); r != nil {
				if t, ok := song(r); ok {
					out = append(out, t)
				}
			}
		}
	}
	if any_ {
		walk(resp, func(k string, o obj) {
			if want[k] {
				collect(o)
			}
		})
		return out
	}
	// Continuations: shelf continuations, or appended items.
	walk(resp, func(k string, o obj) {
		switch k {
		case "musicShelfContinuation", "musicPlaylistShelfContinuation":
			collect(o)
		case "appendContinuationItemsAction":
			for _, it := range asList(o["continuationItems"]) {
				if r := asObj(asObj(it)["musicResponsiveListItemRenderer"]); r != nil {
					if t, ok := song(r); ok {
						out = append(out, t)
					}
				}
			}
		}
	})
	return out
}

// song reads one list row. Rows without a video id (greyed-out, removed songs) are skipped.
func song(r obj) (model.Track, bool) {
	id := str(get(r, "playlistItemData", "videoId"))
	if id == "" {
		id = firstString(r["overlay"], "videoId")
	}
	if id == "" {
		id = firstString(r["flexColumns"], "videoId")
	}
	if !model.ValidID(id) {
		return model.Track{}, false
	}
	cols := asList(r["flexColumns"])
	t := model.Track{ID: id, Title: runsText(columnRuns(cols, 0))}
	if t.Title == "" {
		return model.Track{}, false
	}
	var loose []string
	for i := 1; i < len(cols); i++ {
		for _, run := range columnRuns(cols, i) {
			text := strings.TrimSpace(str(run["text"]))
			if text == "" || isSeparator(text) {
				continue
			}
			bid := str(get(run, "navigationEndpoint", "browseEndpoint", "browseId"))
			switch {
			case strings.HasPrefix(bid, "MPREb"):
				t.Album = text
			case strings.HasPrefix(bid, "UC"), strings.HasPrefix(bid, "MPLA"):
				t.Artists = append(t.Artists, text)
			case bid == "" && i == 1:
				loose = append(loose, text)
			}
		}
	}
	if len(t.Artists) == 0 && len(loose) > 0 {
		for _, l := range loose {
			t.Artists = append(t.Artists, names.SplitArtists(l)...)
		}
	}
	return t, true
}

func columnRuns(cols []any, i int) []obj {
	if i >= len(cols) {
		return nil
	}
	var out []obj
	for _, r := range asList(get(cols[i], "musicResponsiveListItemFlexColumnRenderer", "text", "runs")) {
		if o := asObj(r); o != nil {
			out = append(out, o)
		}
	}
	return out
}

func runsText(runs []obj) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(str(r["text"]))
	}
	return strings.TrimSpace(b.String())
}

func isSeparator(s string) bool {
	switch strings.TrimSpace(s) {
	case "•", "&", ",", "·", "-", "–":
		return true
	}
	return false
}

// card is an album or playlist tile from a library list.
type card struct {
	ID    string // browse id: MPREb… for an album, VL… for a playlist
	Title string
}

// cards returns the tiles whose browse id starts with prefix.
func cards(resp any, prefix string) []card {
	var out []card
	seen := map[string]bool{}
	walk(resp, func(k string, o obj) {
		if k != "musicTwoRowItemRenderer" {
			return
		}
		id := str(get(o, "navigationEndpoint", "browseEndpoint", "browseId"))
		if !strings.HasPrefix(id, prefix) || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, card{ID: id, Title: runsText(runsOf(get(o, "title", "runs")))})
	})
	return out
}

func runsOf(v any) []obj {
	var out []obj
	for _, r := range asList(v) {
		if o := asObj(r); o != nil {
			out = append(out, o)
		}
	}
	return out
}

// header is what an album page says about itself.
type header struct{ title, artist string }

func pageHeader(resp any) header {
	var h header
	walk(resp, func(k string, o obj) {
		if h.title != "" {
			return
		}
		switch k {
		case "musicResponsiveHeaderRenderer", "musicDetailHeaderRenderer", "musicImmersiveHeaderRenderer":
			h.title = runsText(runsOf(get(o, "title", "runs")))
			for _, key := range []string{"straplineTextOne", "subtitle"} {
				for _, r := range runsOf(get(o, key, "runs")) {
					bid := str(get(r, "navigationEndpoint", "browseEndpoint", "browseId"))
					if strings.HasPrefix(bid, "UC") || strings.HasPrefix(bid, "MPLA") {
						h.artist = strings.TrimSpace(str(r["text"]))
						return
					}
				}
			}
		}
	})
	return h
}
