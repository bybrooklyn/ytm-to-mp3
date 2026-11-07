package library

import (
	"sort"

	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
)

// State is where one song of the combined library stands against the music folder.
type State int

const (
	New     State = iota // not in the music folder
	Upgrade              // held, but from a source ranked below what an account can supply
	Held                 // held at the best quality the accounts can supply (or better)
)

// HeldCopy is a copy of a song already in the music folder.
type HeldCopy struct {
	Source catalog.Source
	Rank   int // catalog.Rank(Source)
}

// HeldFunc looks an entry up in the music folder, given the copy chosen as the best
// supplier; ok is false when nothing is held. cmd/ytm backs it with the library table, the
// Apple and YouTube download records and the title/artist index.
type HeldFunc func(e Entry, best Song) (h HeldCopy, ok bool)

// Item is one song of the plan.
type Item struct {
	Entry Entry
	Best  Song // the copy to download from
	State State
	Have  HeldCopy // what is held now (Upgrade and Held)
}

// Plan is the combined library compared against the music folder.
type Plan struct {
	Items []Item
}

// MakePlan compares entries with what is held.
func MakePlan(entries []Entry, held HeldFunc) Plan {
	p := Plan{Items: make([]Item, 0, len(entries))}
	for _, e := range entries {
		it := Item{Entry: e, Best: e.Best()}
		if held != nil {
			if h, ok := held(e, it.Best); ok {
				it.Have = h
				if h.Rank >= catalog.Rank(it.Best.Supply) {
					it.State = Held
				} else {
					it.State = Upgrade
				}
			}
		}
		p.Items = append(p.Items, it)
	}
	return p
}

// Todo is the items to download: the new ones, then the upgrades.
func (p Plan) Todo() []Item {
	var out []Item
	for _, want := range []State{New, Upgrade} {
		for _, it := range p.Items {
			if it.State == want {
				out = append(out, it)
			}
		}
	}
	return out
}

// Counts summarises a plan.
type Counts struct {
	Total, New, Upgrades, Held int
	// PerAccount is how many songs each account has (a song on two accounts counts for both).
	PerAccount map[string]int
	// OnlyOn is how many songs only that account has.
	OnlyOn map[string]int
	// Download is how many songs to download from each audio source, by Todo.
	Download map[catalog.Source]int
	// EstimateBytes is a rough size of the download, from the songs' lengths; EstimateOf says
	// for how many of the songs a length was known (an album purchase has none).
	EstimateBytes int64
	EstimateOf    int
}

// kbps is a typical bit rate per audio source, for the size estimate only.
func kbps(s catalog.Source) int {
	switch s {
	case catalog.BandcampPurchase:
		return 900 // FLAC, roughly
	case catalog.Apple:
		return 256
	case catalog.YouTube:
		return 130
	}
	return 128
}

// Counts totals the plan.
func (p Plan) Counts() Counts {
	c := Counts{Total: len(p.Items), PerAccount: map[string]int{}, OnlyOn: map[string]int{}, Download: map[catalog.Source]int{}}
	for _, it := range p.Items {
		accts := it.Entry.Accounts()
		for _, a := range accts {
			c.PerAccount[a]++
		}
		if len(accts) == 1 {
			c.OnlyOn[accts[0]]++
		}
		switch it.State {
		case New:
			c.New++
		case Upgrade:
			c.Upgrades++
		default:
			c.Held++
			continue
		}
		c.Download[it.Best.Supply]++
		if ms := it.Entry.Track.DurationMillis; ms > 0 && !it.Entry.Whole {
			c.EstimateBytes += int64(ms) / 1000 * int64(kbps(it.Best.Supply)) * 1000 / 8
			c.EstimateOf++
		}
	}
	return c
}

// Accounts is the names in PerAccount, sorted.
func (c Counts) Accounts() []string {
	var out []string
	for a := range c.PerAccount {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
