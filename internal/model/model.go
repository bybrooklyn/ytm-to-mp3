// Package model holds the types shared across ytm packages.
package model

import "strings"

// Track is one song to download, as described by the user's export.
type Track struct {
	ID      string
	Title   string
	Album   string
	Artists []string
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
