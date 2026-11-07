package core

import (
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

// Options are the settings of one run: the folder, the format and the switches. The terminal
// builds them from flags and Settings, the window from Settings and what you typed.
type Options struct {
	Out    string // music folder
	Format string // mp3, m4a, opus, flac
	Jobs   int    // max parallel downloads (0 = automatic)

	DryRun  bool // show what would happen, download nothing
	Retry   bool // retry songs that were unavailable before
	All     bool // take every song of an export, not only the liked ones
	Yes     bool // assume yes
	Offline bool
	NoVPN   bool
	Conns   int // VPN exits for this run (0 = the saved setting)

	// Albums: a song link brings its whole album, and a library or playlist download gets the
	// rest of every album it has a song from. AlbumsMin: only albums with that many saved songs.
	Albums    bool
	AlbumsMin int
	Song      bool // just the songs given for this run (no albums)

	PremiumUpgrade bool
	PremiumLimit   int

	// After a download: MusicBrainz tags, the Premium upgrade, lyrics.
	TagAfter, PremiumAfter, LyricsAfter bool

	Replace, Maybe bool // mfy apple match
	// Source forces the audio source for a catalog import: "" = best available, "youtube".
	Source string
	// Preset are tracks already read (a live library): downloaded like an export.
	Preset []model.Track
	// Given names the command-line flags that were set (they win over saved settings).
	Given map[string]bool

	Apply, Rename, Undo bool
}

// Extras says, in one line, what a download does besides downloading.
func (o Options) Extras() string { return extras(o.internal(nil)) }

func extras(o opts) string {
	var on []string
	if o.albums {
		on = append(on, "complete albums")
	}
	if o.retry {
		on = append(on, "retry unavailable songs")
	}
	if o.tagAfter {
		on = append(on, "fix tags")
	}
	if o.lyricsAfter {
		on = append(on, "fetch lyrics")
	}
	if o.premiumAfter {
		if config.Load().Premium {
			on = append(on, "Premium upgrade")
		} else {
			on = append(on, "Premium upgrade (once you sign in, in settings)")
		}
	}
	if len(on) == 0 {
		return "just download (extras are off in settings)"
	}
	return "also: " + strings.Join(on, ", ") + "  (change in settings)"
}

// opts is Options as the pipeline code uses it.
type opts struct {
	out     string
	format  string
	jobs    int
	dryRun  bool
	retry   bool
	all     bool
	yes     bool
	apply   bool
	rename  bool
	undo    bool
	offline bool
	noVPN   bool
	conns   int

	albums    bool
	albumsMin int
	song      bool

	premiumUpgrade bool
	premiumLimit   int

	tagAfter, premiumAfter bool
	lyricsAfter            bool

	replace, maybe bool
	source         string
	preset         []model.Track
	given          map[string]bool

	// prompt answers the questions a run asks (never nil inside the pipeline).
	prompt Prompter
}

func (o Options) internal(p Prompter) opts {
	return opts{
		out: o.Out, format: o.Format, jobs: o.Jobs, dryRun: o.DryRun, retry: o.Retry, all: o.All,
		yes: o.Yes, apply: o.Apply, rename: o.Rename, undo: o.Undo, offline: o.Offline, noVPN: o.NoVPN,
		conns: o.Conns, albums: o.Albums, albumsMin: o.AlbumsMin, song: o.Song,
		premiumUpgrade: o.PremiumUpgrade, premiumLimit: o.PremiumLimit,
		tagAfter: o.TagAfter, premiumAfter: o.PremiumAfter, lyricsAfter: o.LyricsAfter,
		replace: o.Replace, maybe: o.Maybe, source: o.Source, preset: o.Preset, given: o.Given,
		prompt: p,
	}
}
