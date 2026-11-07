// Package library reads the music library of every account the user has set up (YouTube
// Music, Bandcamp purchases, Apple Music), merges them into one deduplicated library, and
// plans what is missing from the music folder and which account can supply each song at the
// best quality. It does no downloading: cmd/mfy hands the plan to the existing pipelines.
//
// Adding a service (Tidal, say) means implementing LibrarySource and registering it:
//
//	type tidalSource struct{}
//
//	func (tidalSource) Name() string            { return "Tidal" }
//	func (tidalSource) Supplies() catalog.Source { return catalog.Tidal }
//	func (tidalSource) Detect() library.Status   { return library.Status{SetUp: tidalSignedIn()} }
//	func (tidalSource) Read(ctx context.Context, progress func(string)) ([]library.Song, error) {
//		// one Song per library song: Track filled in, From "Tidal", Supply = tidalSource's
//		// Supplies(), Ref = whatever the downloader needs to fetch it
//	}
//
//	func init() { library.Register(tidalSource{}) }
//
// Combine then merges its songs with everyone else's by the usual identity rules, and Plan
// prefers it for a song exactly when catalog.Rank puts its audio above the other accounts'.
// The download step (cmd/mfy) must know how to fetch from the new Supplies() source.
package library

import (
	"context"
	"sync"

	"github.com/bybrooklyn/mfetchy/internal/catalog"
)

// Status is what Detect reports about an account without touching the network or asking
// the user anything: only mfetchy's own settings and which browsers are installed are looked at.
type Status struct {
	SetUp  bool   // the user has set this account up
	Detail string // how it is used when set up ("reads Firefox", "signed in")
	Hint   string // what to do when it is not ("not set up (Connections)")
}

// Song is one song (or, when Whole, one whole album) as an account has it.
type Song struct {
	catalog.Track
	// From is the LibrarySource's Name.
	From string
	// Supply is the best audio this account can itself supply for the song: the original
	// files of a Bandcamp purchase, an Apple Music stream, a YouTube Music stream.
	Supply catalog.Source
	// Whole marks a purchase of a whole album whose songs the account does not list: it
	// supplies every song of that album found in the other accounts, and is downloaded as
	// the album when it covers none.
	Whole bool
	// Ref is the source's own handle, for its downloader (a bandcamp.Item, an apple.Track).
	Ref any
}

// LibrarySource is one account whose library can be read.
type LibrarySource interface {
	// Name is the account as the user knows it ("YouTube Music").
	Name() string
	// Supplies is the best audio source this account can provide.
	Supplies() catalog.Source
	// Detect reports whether the user set the account up. It must not use the network or
	// prompt (it runs before anything is read, and before the user has agreed to reading).
	Detect() Status
	// Read returns the account's songs. progress, if non-nil, is told what is being read.
	Read(ctx context.Context, progress func(string)) ([]Song, error)
}

var (
	regMu    sync.Mutex
	registry []LibrarySource
)

// Register adds a source to the ones Sources returns. The built-in accounts register
// themselves; an additional service calls this from its package init.
func Register(s LibrarySource) {
	regMu.Lock()
	defer regMu.Unlock()
	registry = append(registry, s)
}

// Sources lists every registered account, in registration order.
func Sources() []LibrarySource {
	regMu.Lock()
	defer regMu.Unlock()
	return append([]LibrarySource(nil), registry...)
}

func init() {
	Register(NewYouTube())
	Register(NewBandcamp())
	Register(NewApple())
}

// SourceResult is what happened to one account during ReadAll.
type SourceResult struct {
	Name    string
	Status  Status
	Read    bool // the account was read
	Songs   int
	Err     error
	Skipped bool // not set up: nothing was read
}

// ReadAll reads every set-up source in order and returns all their songs. A source that is
// not set up is skipped without reading anything; one that fails is reported and the others
// still count.
func ReadAll(ctx context.Context, srcs []LibrarySource, progress func(name, what string)) ([]Song, []SourceResult) {
	var all []Song
	var res []SourceResult
	for _, s := range srcs {
		r := SourceResult{Name: s.Name(), Status: s.Detect()}
		if !r.Status.SetUp {
			r.Skipped = true
			res = append(res, r)
			continue
		}
		if ctx.Err() != nil {
			r.Err = ctx.Err()
			res = append(res, r)
			continue
		}
		var say func(string)
		if progress != nil {
			say = func(what string) { progress(r.Name, what) }
		}
		songs, err := s.Read(ctx, say)
		r.Read, r.Err = true, err
		if err == nil {
			r.Songs = len(songs)
			all = append(all, songs...)
		}
		res = append(res, r)
	}
	return all, res
}
