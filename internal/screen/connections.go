package screen

import (
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
)

// Connection is one account or source ytm can use, and what state it is in.
type Connection struct {
	ID     string
	Name   string
	Status string
	Hint   string
	Good   bool   // set up (shown green)
	Dim    bool   // nothing to set up
	Action string // what Enter does ("" = nothing): the host runs it, then comes back

	// For graphical hosts (the terminal shows Hint): a plain description and the button's label.
	Help   string
	Button string
}

// DetectConnections reads the current state of every account and source. It only looks at
// files ytm owns and at which browsers are installed: no network, no keyring prompts.
func DetectConnections() []Connection {
	cfg := config.Load()
	var out []Connection

	yt := Connection{ID: "youtube", Name: "YouTube Music library", Action: "youtube", Button: "Choose browser...",
		Help: "Your library is read from the browser that is signed in to music.youtube.com. Nothing is exported."}
	if name := browserName(cfg.YouTubeBrowser); name != "" {
		yt.Button = "Change browser..."
		yt.Status, yt.Good = "reads "+name, true
		yt.Hint = "your library is read straight from that browser's sign-in, no export; enter to pick another browser"
	} else {
		yt.Status = "not set up"
		yt.Hint = "pick the browser that is signed in to music.youtube.com, and ytm reads your library from it; enter to choose"
	}
	out = append(out, yt)

	ap := Connection{ID: "apple", Name: "Apple Music", Action: "applein", Button: "Sign in",
		Help: "Optional. Songs come as 256 kbps AAC, with your own Apple Music subscription."}
	if config.AppleToken() != "" {
		ap.Button = "Sign out"
		ap.Status, ap.Good, ap.Action = "signed in (store "+strings.ToUpper(orDefault(cfg.AppleStorefront, "us"))+")", true, "appleout"
		ap.Hint = "songs come as 256 kbps AAC; enter to sign out"
	} else {
		ap.Status = "not signed in"
		ap.Hint = "optional: 256 kbps AAC with your own subscription; enter to sign in"
	}
	out = append(out, ap)

	ti := Connection{ID: "tidal", Name: "Tidal", Action: "tidalin", Button: "Sign in",
		Help: "Optional. Lossless FLAC with your own subscription. Tidal's terms don't allow downloading: you read the warning first."}
	if p, err := tidal.SessionPath(); err == nil && tidal.New(p).SignedIn() {
		q := "lossless"
		if cfg.TidalQuality == "best" {
			q = "best plain tier"
		}
		ti.Button = "Sign out"
		ti.Status, ti.Good, ti.Action = "signed in ("+q+")", true, "tidalout"
		ti.Hint = "songs come as lossless FLAC when Tidal serves them plainly; enter to sign out"
	} else {
		ti.Status = "not signed in"
		ti.Hint = "optional: lossless FLAC with your own subscription (Tidal's terms don't allow downloading: you read the warning first); enter to sign in"
	}
	out = append(out, ti)

	vp := DetectVPN(cfg)
	pr := Connection{ID: "vpn", Name: "VPN", Action: "signin", Button: "Set up",
		Help: "Optional, but much faster: downloads go through several servers at once. Proton, Mullvad, or any WireGuard config."}
	if vp.SignedIn {
		pr.Button = "Sign out"
		pr.Status, pr.Good, pr.Action = vp.Provider+" · "+vp.Who, true, "signout"
		pr.Hint = "downloads go through several servers at once; enter to sign out"
	} else {
		pr.Status = "not set up"
		pr.Hint = "optional but much faster; enter to choose Proton, Mullvad or your own WireGuard config"
	}
	out = append(out, pr)

	pm := Connection{ID: "premium", Name: "YouTube Premium upgrade", Action: "premium", Button: "Set up...",
		Help: "Experimental. Re-downloads songs at Premium quality using your Google account."}
	if cfg.Premium {
		pm.Button = "Turn off"
		pm.Status, pm.Good = "on (experimental)", true
		pm.Hint = "re-downloads songs at Premium quality, slowly; enter to turn off"
	} else {
		pm.Status = "off"
		pm.Hint = "experimental, uses your Google account; enter to read the warning"
	}
	out = append(out, pm)

	out = append(out,
		Connection{ID: "spotify", Name: "Spotify", Status: "ready", Dim: true, Hint: "no sign-in: public albums, playlists and songs; the audio comes from Apple Music or YouTube",
			Help: "No sign-in. Public albums, playlists and songs; the audio comes from Apple Music or YouTube."},
		Connection{ID: "soundcloud", Name: "SoundCloud", Status: "ready", Dim: true, Hint: "no sign-in: paste a song, playlist or artist link (stream quality)",
			Help: "No sign-in. Paste a song, playlist or artist link. Stream quality."},
	)
	bc := Connection{ID: "bandcamp", Name: "Bandcamp", Action: "bandcamp", Button: "Choose browser...",
		Help: "Optional. Pick the browser signed in to bandcamp.com and music you bought downloads as the original files."}
	if name := browserName(cfg.BandcampBrowser); name != "" {
		bc.Button = "Change browser..."
		bc.Status, bc.Good = "reads "+name+" for your purchases", true
		bc.Hint = "music you bought downloads as the original files (FLAC when offered), the rest streams; enter to pick another browser"
	} else {
		bc.Status = "streams (128 kbps)"
		bc.Hint = "optional: pick the browser signed in to bandcamp.com and music you bought downloads in its original quality; enter to choose"
	}
	out = append(out, bc)
	return out
}

func browserName(id string) string {
	if id == "" {
		return ""
	}
	for _, b := range browsercookies.Detect() {
		if b.ID == id {
			return b.Name
		}
	}
	return ""
}

// Connections is the screen listing every account and source.
type Connections struct {
	items  []Connection
	cur    int
	action string
	done   bool
}

// NewConnections shows items with the cursor on row cur.
func NewConnections(items []Connection, cur int) *Connections {
	if cur < 0 || cur >= len(items) {
		cur = 0
	}
	return &Connections{items: items, cur: cur}
}

func (c *Connections) Cursor() int      { return c.cur }
func (c *Connections) Editing() bool    { return false }
func (c *Connections) Finished() bool   { return c.done || c.action != "" }
func (c *Connections) Action() string   { return c.action }
func (c *Connections) ClearAction()     { c.action = "" }
func (c *Connections) Type(string)      {}
func (c *Connections) Paste(string)     {}
func (c *Connections) Item() Connection { return c.items[c.cur] }
func (c *Connections) SelectIndex(i int) {
	if i >= 0 && i < len(c.items) {
		c.cur = i
	}
}

func (c *Connections) Key(k string) {
	switch k {
	case "ctrl+c", "esc", "q":
		c.done = true
	case "up", "k":
		c.cur = (c.cur - 1 + len(c.items)) % len(c.items)
	case "down", "j", "tab":
		c.cur = (c.cur + 1) % len(c.items)
	case "enter", "right", "l", "space":
		c.action = c.items[c.cur].Action
	}
}

func (c *Connections) Screen() Screen {
	sc := Screen{Title: "connections", Footer: "↑↓ select · enter set up or change · esc done"}
	for i, it := range c.items {
		row := Row{
			ID: it.ID, Label: it.Name, Value: it.Status, Selected: i == c.cur, Dim: it.Dim, Good: it.Good,
			Gap: it.ID == "spotify", Message: it.Hint, Help: it.Help,
			Kind: KindAction, Button: it.Button,
		}
		if it.Action == "" {
			row.Kind, row.Button = KindInfo, ""
		} else if row.Button == "" {
			row.Button = "Set up"
		}
		if i == 0 {
			row.Group = "Accounts"
		}
		if it.ID == "spotify" {
			row.Group = "No sign-in needed"
		}
		sc.Rows = append(sc.Rows, row)
	}
	return sc
}

var _ Model = (*Connections)(nil)
