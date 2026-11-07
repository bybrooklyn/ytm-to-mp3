package names

import (
	"strings"
)

// mfetchy keeps tags under one set of lower-case names everywhere ("canonical" names):
//
//	title artist album album_artist date genre comment composer copyright label barcode
//	track ("N" or "N/T") disc ("N" or "N/T") isrc explicit ("1")
//	musicbrainz_trackid musicbrainz_albumid musicbrainz_artistid musicbrainz_albumartistid
//
// Containers store them differently, and ffmpeg only knows some of them for each one.
// TagsFor turns canonical names into what to hand ffmpeg for a format, and CanonicalTag
// turns what ffprobe reads back into canonical names, so a tag written once compares
// equal the next time it is read (and isn't proposed again).

// TagsFor returns m with keys renamed for format ("mp3", "m4a", "opus", "ogg", "flac";
// a leading dot is fine), dropping what the container can't hold. Empty values are kept
// (an empty value clears a tag).
func TagsFor(format string, m map[string]string) map[string]string {
	f := tagFormat(format)
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		switch f {
		case "vorbis":
			switch k {
			case "track", "disc":
				// Vorbis comments keep the total in its own field: TRACKNUMBER=2, TRACKTOTAL=12.
				n, t, ok := strings.Cut(v, "/")
				out[k] = n
				if ok {
					out[k+"total"] = t
				}
				continue
			case "explicit":
				continue // no common Vorbis field
			}
			out[k] = v
		case "mp3":
			if nk, ok := mp3Keys[k]; ok {
				if nk != "" {
					out[nk] = v
				}
				continue
			}
			out[k] = v
		case "m4a":
			if m4aKeys[k] {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

// TagSupported reports whether format can store the canonical tag key.
func TagSupported(format, key string) bool {
	switch tagFormat(format) {
	case "vorbis":
		return key != "explicit"
	case "mp3":
		nk, ok := mp3Keys[key]
		return !ok || nk != ""
	case "m4a":
		return m4aKeys[key]
	}
	return true
}

// CanonicalTag maps a tag name as ffprobe reports it (any case) to mfetchy's name for it.
func CanonicalTag(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	if c, ok := readKeys[k]; ok {
		return c
	}
	return k
}

func tagFormat(format string) string {
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "opus", "ogg", "oga", "flac":
		return "vorbis"
	case "mp3":
		return "mp3"
	case "m4a", "mp4", "aac", "alac":
		return "m4a"
	}
	return ""
}

// mp3Keys: canonical name -> ID3 name as ffmpeg's ID3v2 writer wants it. A four-letter
// frame id is written as that frame; anything else becomes a TXXX frame of that name
// (the names Picard and most players use). "" = not written.
var mp3Keys = map[string]string{
	"isrc":                      "TSRC",
	"label":                     "publisher", // TPUB
	"barcode":                   "BARCODE",
	"explicit":                  "",
	"lyrics":                    "lyrics-eng", // a USLT frame (plain "lyrics" would be a TXXX most players ignore)
	"musicbrainz_trackid":       "MusicBrainz Track Id",
	"musicbrainz_albumid":       "MusicBrainz Album Id",
	"musicbrainz_artistid":      "MusicBrainz Artist Id",
	"musicbrainz_albumartistid": "MusicBrainz Album Artist Id",
}

// m4aKeys are the canonical names ffmpeg's MP4 writer stores as iTunes atoms; it drops
// any other name (custom "----" atoms aren't something it can write).
var m4aKeys = map[string]bool{
	"title": true, "artist": true, "album": true, "album_artist": true, "date": true,
	"genre": true, "comment": true, "composer": true, "copyright": true,
	"track": true, "disc": true, "lyrics": true,
}

// readKeys: names ffprobe may report (lower-cased) -> canonical name.
var readKeys = map[string]string{
	"tsrc":                        "isrc",
	"publisher":                   "label",
	"organization":                "label",
	"albumartist":                 "album_artist",
	"album artist":                "album_artist",
	"tracknumber":                 "track",
	"discnumber":                  "disc",
	"totaltracks":                 "tracktotal",
	"totaldiscs":                  "disctotal",
	"musicbrainz track id":        "musicbrainz_trackid",
	"musicbrainz album id":        "musicbrainz_albumid",
	"musicbrainz artist id":       "musicbrainz_artistid",
	"musicbrainz album artist id": "musicbrainz_albumartistid",
	"itunesadvisory":              "explicit",
	"lyrics-eng":                  "lyrics", // ID3 USLT as ffprobe reports it
}
