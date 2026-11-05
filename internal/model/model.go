// Package model holds the types shared across ytm packages.
package model

import "strings"

// ValidID reports whether s is a well-formed YouTube video id: exactly 11 ASCII letters,
// digits, '-' or '_'. IDs end up in file names and yt-dlp output templates, so nothing
// else may ever get that far.
func ValidID(s string) bool {
	if len(s) != 11 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Track is one song to download, as described by the user's export.
type Track struct {
	ID      string
	Title   string
	Album   string
	Artists []string
	// Position on its album when known (from the YouTube Music album), for the file name
	// "NN - Title" and the track tag. 0 = unknown.
	TrackNo, TrackTotal int

	// What a catalog (Spotify, Apple Music) already knows about the song. These beat what
	// the YouTube video says: a release date over an upload date, square album art over a
	// video thumbnail. All optional.
	AlbumArtist string
	Disc        int
	DiscTotal   int
	Date        string // "2019" or "2019-05-31"
	Genre       string
	ISRC        string
	Cover       string // https URL of the album art
}

// PrimaryArtist returns the first listed artist, or "".
func (t Track) PrimaryArtist() string {
	if len(t.Artists) > 0 {
		return t.Artists[0]
	}
	return ""
}

// Label is a short human-readable name for logs.
func (t Track) Label() string {
	var parts []string
	if a := t.PrimaryArtist(); a != "" {
		parts = append(parts, a)
	}
	if t.Title != "" {
		parts = append(parts, t.Title)
	}
	if len(parts) == 0 {
		return t.ID
	}
	return strings.Join(parts, " - ")
}
