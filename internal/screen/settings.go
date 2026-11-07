// Package screen holds ytm's screens as plain state machines that know nothing about how
// they are drawn: they take key names ("up", "enter", "esc", ...) and produce a Screen (rows
// of labels, values and hints). The terminal UI draws a Screen with ANSI colours and the
// desktop app draws the same Screen in a window, so both look and behave the same.
package screen

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// Settings is the settings screen: every setting on one screen, changed with the arrow keys
// and saved the moment it changes.
type Settings struct {
	cfg      config.Config
	signedIn bool
	appleIn  bool   // an Apple Music login is saved
	tidalIn  bool   // a Tidal login is saved
	provider string // the VPN provider's name for the account row ("" = Proton VPN)
	user     string
	cur      int

	editing bool   // typing a folder path
	input   string // the folder being typed
	err     string // validation / save problem for the highlighted row
	saved   bool   // show the "saved" indicator

	action string // "signin", "signout", "test", ...: work for the host to do, then come back
	done   bool

	save  func(config.Config) error
	check func(string) error
}

// NewSettings starts the screen on row cur. save stores the config after every change;
// check validates a typed music folder.
func NewSettings(cfg config.Config, signedIn bool, user string, cur int, save func(config.Config) error, check func(string) error) *Settings {
	return &Settings{cfg: cfg, signedIn: signedIn, user: user, cur: cur, save: save, check: check}
}

// SetProvider tells the screen which VPN provider the account row is about ("" = Proton).
func (m *Settings) SetProvider(name string) { m.provider = name }

// SetApple tells the screen whether an Apple Music login is saved.
func (m *Settings) SetApple(in bool) { m.appleIn = in }

// SetTidal tells the screen whether a Tidal login is saved.
func (m *Settings) SetTidal(in bool) { m.tidalIn = in }

// Cursor is the highlighted row (to come back to after an action).
func (m *Settings) Cursor() int { return m.cur }

// SelectRow puts the cursor on the sign-in row (the first-run landing spot).
func (m *Settings) SelectAccount() { m.cur = int(rowAccount) }

// Config is the settings as they are now.
func (m *Settings) Config() config.Config { return m.cfg }

// Action is the work the host should do now ("signin", "signout", "test", "premium",
// "applein", "appleout"), or "" when the screen just finished.
func (m *Settings) Action() string { return m.action }

// ClearAction forgets a due action (the host decided not to run it, or has run it).
func (m *Settings) ClearAction() { m.action = "" }

// Finished reports whether the screen wants to close (it was left, or an action is due).
func (m *Settings) Finished() bool { return m.done || m.action != "" }

// Editing reports whether a folder path is being typed (keys are then text).
func (m *Settings) Editing() bool { return m.editing }

// Saved reports whether the last change was stored.
func (m *Settings) Saved() bool { return m.saved }

// Err is the message under the highlighted row, if any.
func (m *Settings) Err() string { return m.err }

func (m *Settings) kind() rowKind { return settingsRows[m.cur].kind }

func (m *Settings) vpnUsable() bool { return m.signedIn }

func (m *Settings) persist() {
	if err := m.save(m.cfg); err != nil {
		m.err, m.saved = "couldn't save: "+err.Error(), false
		return
	}
	m.err, m.saved = "", true
}

func (m *Settings) move(d int) {
	m.cur = (m.cur + d + len(settingsRows)) % len(settingsRows)
	m.saved, m.err = false, ""
}

// Paste adds pasted text to the folder being typed.
func (m *Settings) Paste(s string) {
	if m.editing {
		m.input += strings.TrimSpace(s)
	}
}

// Type adds typed text to the folder being typed.
func (m *Settings) Type(s string) {
	if m.editing && s != "" {
		m.input += s
		m.err = ""
	}
}

// Key handles one key by name: "up", "down", "left", "right", "enter", "esc", "tab",
// "space", "backspace", "ctrl+c", "ctrl+u", or a single character.
func (m *Settings) Key(k string) {
	if m.editing {
		m.editKey(k)
		return
	}
	switch k {
	case "ctrl+c", "esc", "q":
		m.done = true
	case "up", "k":
		m.move(-1)
	case "down", "j", "tab":
		m.move(1)
	case "left", "h":
		m.change(-1)
	case "right", "l", "space":
		m.change(1)
	case "enter":
		m.activate()
	}
}

func (m *Settings) editKey(k string) {
	switch k {
	case "ctrl+c":
		m.done = true
	case "esc":
		m.editing, m.err = false, ""
	case "enter":
		if err := m.check(m.input); err != nil {
			m.err = err.Error()
			return
		}
		m.cfg.Out = config.ExpandPath(m.input)
		m.editing = false
		m.persist()
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.input = ""
	default:
		if r := []rune(k); len(r) == 1 {
			m.Type(k)
		}
	}
}

func (m *Settings) activate() {
	switch m.kind() {
	case rowFolder:
		m.editing = true
		m.input = orDefault(m.cfg.Out, config.DefaultOut())
		m.err, m.saved = "", false
	case rowAccount:
		m.action = "signin"
		if m.signedIn {
			m.action = "signout"
		}
	case rowTest:
		if !m.signedIn {
			m.err = "set up a VPN first"
			return
		}
		m.action = "test"
	case rowPremium:
		m.action = "premium"
	case rowApple:
		m.action = "applein"
		if m.appleIn {
			m.action = "appleout"
		}
	case rowTidal:
		m.action = "tidalin"
		if m.tidalIn {
			m.action = "tidalout"
		}
	}
}

// change steps the highlighted value by d (-1 or +1) and saves it.
func (m *Settings) change(d int) {
	switch m.kind() {
	case rowFormat:
		m.cfg.Format = cycle(engine.Formats, orDefault(m.cfg.Format, config.DefaultFormat), d)
	case rowAlbums:
		m.cfg.NoAlbums = !m.cfg.NoAlbums
	case rowRetry:
		m.cfg.NoRetry = !m.cfg.NoRetry
	case rowTagAfter:
		m.cfg.NoTagAfter = !m.cfg.NoTagAfter
	case rowPremiumAfter:
		m.cfg.NoPremiumAfter = !m.cfg.NoPremiumAfter
	case rowLyricsAfter:
		m.cfg.NoLyricsAfter = !m.cfg.NoLyricsAfter
	case rowLyricsLRCLIB:
		m.cfg.NoLyricsLRCLIB = !m.cfg.NoLyricsLRCLIB
	case rowLyricsNetEase:
		m.cfg.LyricsNetEase = !m.cfg.LyricsNetEase
	case rowFolderJpg:
		m.cfg.NoFolderJpg = !m.cfg.NoFolderJpg
	case rowNFO:
		m.cfg.NoNFO = !m.cfg.NoNFO
	case rowCoverPNG:
		m.cfg.CoverPNG = !m.cfg.CoverPNG
	case rowPrivate:
		m.cfg.Private = !m.cfg.Private
	case rowVPN:
		if !m.vpnUsable() {
			m.err = "set up a VPN first"
			return
		}
		m.cfg.NoVPN = !m.cfg.NoVPN
	case rowConns:
		n := m.cfg.Connections
		if n == 0 {
			n = 3 // unset
		}
		if n = clamp(n+d, -1, MaxConns); n == 0 {
			n += d // 0 means "unset": step over it, to 1 or to Auto (-1)
		}
		m.cfg.Connections = clamp(n, -1, MaxConns)
	case rowJobs:
		m.cfg.Jobs = clamp(m.cfg.Jobs+d, 0, MaxJobs)
	case rowCountries:
		m.cfg.Countries = cycle(countryChoices, m.cfg.Countries, d)
	case rowTidalQuality:
		m.cfg.TidalQuality = cycle([]string{"", "best"}, m.cfg.TidalQuality, d)
	default:
		return
	}
	m.persist()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

type rowKind int

const (
	rowFolder rowKind = iota
	rowFormat
	rowAlbums // the extras of a library download, all on by default
	rowRetry
	rowTagAfter
	rowPremiumAfter
	rowLyricsAfter
	rowLyricsLRCLIB
	rowLyricsNetEase
	rowVPN
	rowAccount
	rowConns
	rowJobs
	rowCountries
	rowPremium
	rowApple
	rowTidal
	rowTidalQuality
	rowFolderJpg
	rowNFO
	rowCoverPNG
	rowPrivate
	rowTest
)

const (
	MaxConns = 12
	MaxJobs  = 64
)

// countryChoices is the short cycle list for the exit-country row; "" means automatic.
var countryChoices = []string{"", "US", "CA", "GB", "NL", "DE", "CH", "SE", "FR", "JP"}

var settingsRows = []struct {
	kind  rowKind
	label string
}{
	{rowFolder, "Music folder"},
	{rowFormat, "Format"},
	{rowAlbums, "Complete albums"},
	{rowRetry, "Retry unavailable"},
	{rowTagAfter, "Fix tags after"},
	{rowPremiumAfter, "Premium upgrade after"},
	{rowLyricsAfter, "Fetch lyrics after"},
	{rowLyricsLRCLIB, "Lyrics from LRCLIB"},
	{rowLyricsNetEase, "Lyrics from NetEase"},
	{rowVPN, "VPN"},
	{rowAccount, "VPN account"},
	{rowConns, "VPN connections"},
	{rowJobs, "Parallel downloads"},
	{rowCountries, "Exit countries"},
	{rowPremium, "YouTube Premium"},
	{rowApple, "Apple Music"},
	{rowTidal, "Tidal"},
	{rowTidalQuality, "Tidal quality"},
	{rowFolderJpg, "Album art file (folder.jpg)"},
	{rowNFO, "Album .nfo files"},
	{rowCoverPNG, "Album art as cover.png"},
	{rowPrivate, "Private mode"},
	{rowTest, "Test VPN"},
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func cycle(opts []string, cur string, d int) string {
	i := -1
	for j, o := range opts {
		if o == cur {
			i = j
		}
	}
	if i < 0 {
		return opts[0]
	}
	return opts[(i+d+len(opts))%len(opts)]
}

func clamp(n, lo, hi int) int {
	return max(lo, min(hi, n))
}

// ytm's palette (internal/ui), under the names the screens here use.
var (
	cDim    = ui.CDim
	cBold   = ui.CBold
	cAccent = ui.CAccent
	cGreen  = ui.CGreen
	cYellow = ui.CYellow
	cReset  = ui.CReset
)

func (m *Settings) value(k rowKind) (val, hint string, dim bool) {
	switch k {
	case rowFolder:
		return config.Tilde(config.ExpandPath(orDefault(m.cfg.Out, config.DefaultOut()))), "enter to type a new folder", false
	case rowFormat:
		f := orDefault(m.cfg.Format, config.DefaultFormat)
		if f == "opus" {
			return f, "YouTube's own audio, copied untouched (others are re-encoded)", false
		}
		return f, "re-encoded from YouTube's audio; opus keeps it untouched", false
	case rowAlbums:
		return onOff(!m.cfg.NoAlbums), "a song link brings its whole album; a library or playlist the rest of every album it has a song from", false
	case rowRetry:
		return onOff(!m.cfg.NoRetry), "songs that were unavailable last time are tried again (incl. on YouTube Music)", false
	case rowTagAfter:
		return onOff(!m.cfg.NoTagAfter), "the songs just downloaded get MusicBrainz tags (only when it is certain)", false
	case rowLyricsAfter:
		return onOff(!m.cfg.NoLyricsAfter), "new songs get line-synced lyrics (a .lrc beside the file and a tag in it), plain ones if that is all there is", false
	case rowLyricsLRCLIB:
		return onOff(!m.cfg.NoLyricsLRCLIB), "lrclib.net: free, community lyrics; it is told a song's title, artist, album and length, nothing else", false
	case rowLyricsNetEase:
		return onOff(m.cfg.LyricsNetEase), "unofficial: music.163.com, asked only if LRCLIB has nothing; it is told the title and artist", false
	case rowPremiumAfter:
		hint := "after downloading, upgrade songs to Premium quality (slow; uses your account)"
		if !m.cfg.Premium {
			hint = "needs a YouTube Premium sign-in: set it up in the YouTube Premium row below"
		}
		return onOff(!m.cfg.NoPremiumAfter), hint, !m.cfg.Premium // greyed: inert until the sign-in
	case rowVPN:
		if !m.signedIn {
			return "off", "set up a VPN below to use it (Proton, Mullvad or your own WireGuard config)", true
		}
		if m.cfg.NoVPN {
			return "off", "", false
		}
		return "on", "", false
	case rowAccount:
		if m.signedIn {
			who := m.user
			if m.provider != "" {
				who = m.provider + " · " + m.user
			}
			return "signed in as " + who, "enter to sign out", false
		}
		return "not signed in", "enter to choose Proton, Mullvad or your own WireGuard config", false
	case rowConns:
		if m.cfg.Connections < 0 {
			return "Auto", "your VPN account's device limit, minus one kept as a spare", !m.signedIn || m.cfg.NoVPN
		}
		return fmt.Sprint(orInt(m.cfg.Connections, 3)), "exits used at once; below 1 is Auto (all your plan allows)", !m.signedIn || m.cfg.NoVPN
	case rowJobs:
		if m.cfg.Jobs == 0 {
			return "Auto", "", false
		}
		return fmt.Sprint(m.cfg.Jobs), "", false
	case rowCountries:
		if m.cfg.Countries == "" {
			return "Auto", "best available servers", !m.signedIn || m.cfg.NoVPN
		}
		return m.cfg.Countries, "", !m.signedIn || m.cfg.NoVPN
	case rowPremium:
		if m.cfg.Premium {
			return "on (upgrade pass only)", "enter to turn off", false
		}
		return "off", "experimental: enter to read the warning and turn on", false
	case rowApple:
		if m.appleIn {
			return "signed in (store " + strings.ToUpper(orDefault(m.cfg.AppleStorefront, "us")) + ")", "enter to sign out", false
		}
		return "not signed in", "optional: 256 kbps AAC with your Apple Music subscription; enter to sign in", false
	case rowFolderJpg:
		return onOff(!m.cfg.NoFolderJpg), "a folder.jpg with the album cover in each album folder, for Jellyfin, Plex, Kodi and other players", false
	case rowNFO:
		return onOff(!m.cfg.NoNFO), "an album.nfo in each album folder and an artist.nfo in each artist folder (Kodi and Jellyfin)", false
	case rowCoverPNG:
		return onOff(m.cfg.CoverPNG), "also a cover.png of the same album art", false
	case rowPrivate:
		return onOff(m.cfg.Private), "no MusicBrainz tag or cover lookups at all (see PRIVACY.md); downloading still contacts your music source", false
	case rowTidal:
		if m.tidalIn {
			return "signed in", "lossless FLAC with your own subscription; enter to sign out", false
		}
		return "not signed in", "optional: lossless FLAC with your own Tidal subscription (their terms don't allow it: you'll read the warning first); enter to sign in", false
	case rowTidalQuality:
		if m.cfg.TidalQuality == "best" {
			return "best plain tier", "hi-res only when Tidal serves it as a plain file, else lossless; nothing encrypted is ever fetched", !m.tidalIn
		}
		return "lossless", "16-bit/44.1 kHz FLAC only; a song Tidal won't serve that way comes from Apple Music or YouTube", !m.tidalIn
	case rowTest:
		if !m.signedIn {
			return "", "sign in first", true
		}
		return "press enter to connect and show each exit", "", false
	}
	return "", "", false
}

// labelWidth is the label column's width: the longest label, so values line up.
func labelWidth() int {
	w := 0
	for _, r := range settingsRows {
		w = max(w, len([]rune(r.label)))
	}
	return w
}

// Screen describes the screen as it is now.
func (m *Settings) Screen() Screen {
	sc := Screen{Title: "settings", Subtitle: config.Tilde(config.Path()), Saved: m.saved}
	if m.editing {
		sc.Footer = "type a folder · enter save · esc cancel"
	} else {
		sc.Footer = "↑↓ select · ←→ change · enter edit · esc done"
	}
	for i, r := range settingsRows {
		sel := i == m.cur
		val, hint, dim := m.value(r.kind)
		row := Row{
			ID: fmt.Sprint(int(r.kind)), Label: r.label, Value: val, Selected: sel, Dim: dim,
			Gap:     r.kind == rowVPN || r.kind == rowPremium, // groups: downloads / VPN / accounts
			Stepper: isStepRow(r.kind), Action: r.kind == rowTest,
			Good: val == "on" || strings.HasPrefix(val, "signed in"),
		}
		m.describe(&row, r.kind)
		if sel {
			row.Error = m.err
			switch {
			case m.editing && r.kind == rowFolder:
				row.Editing, row.Value = true, m.input
			}
			switch {
			case m.err != "":
				row.Message = m.err
			case m.editing:
				row.Message = "enter to save · esc to cancel"
			default:
				row.Message = hint
			}
		}
		sc.Rows = append(sc.Rows, row)
	}
	return sc
}

// describe fills in the hints a graphical host uses: which control a row wants, its options,
// the section it starts and a line of help that is always shown.
func (m *Settings) describe(row *Row, k rowKind) {
	toggle := func(help string) {
		row.Kind, row.On, row.Help = KindToggle, row.Value == "on", help
	}
	switch k {
	case rowFolder:
		row.Group, row.Kind = "Library", KindText
		row.Help = "Where downloaded music is kept."
	case rowFormat:
		row.Kind, row.Options = KindChoice, engine.Formats
		row.Help = "opus keeps YouTube's own audio untouched; the others are re-encoded from it."
	case rowAlbums:
		toggle("A song link brings its whole album. A library or playlist brings the rest of every album it has a song from.")
	case rowRetry:
		toggle("Try songs that were unavailable last time again, including on YouTube Music.")
	case rowTagAfter:
		toggle("Give the songs just downloaded their MusicBrainz tags, only when the match is certain.")
	case rowPremiumAfter:
		toggle("After downloading, upgrade songs to Premium quality. Slow, and uses your YouTube Premium sign-in.")
	case rowLyricsAfter:
		toggle("New songs get line-synced lyrics: a .lrc file beside the song and a lyrics tag in it. Plain lyrics if that is all there is.")
	case rowLyricsLRCLIB:
		toggle("lrclib.net: free, community lyrics. It is told a song's title, artist, album and length, nothing else.")
	case rowLyricsNetEase:
		toggle("Unofficial (music.163.com). Only asked when LRCLIB has nothing; it is told the title and artist.")
	case rowVPN:
		row.Group = "Network"
		toggle("Send downloads through several VPN servers at once.")
	case rowAccount:
		row.Kind, row.Button = KindAction, "Sign in"
		if m.signedIn {
			row.Button = "Sign out"
		}
		row.Help = "Your VPN account: Proton, Mullvad, or your own WireGuard config."
	case rowConns:
		row.Kind, row.Help = KindNumber, "Servers used at once. Auto uses all your plan allows, minus one kept spare."
	case rowJobs:
		row.Kind, row.Help = KindNumber, "Songs downloaded at the same time. Auto lets ytm decide."
	case rowCountries:
		row.Kind, row.Help = KindChoice, "Where downloads appear to come from."
		row.Options = nil
		for _, c := range countryChoices {
			row.Options = append(row.Options, orDefault(c, "Auto"))
		}
	case rowPremium:
		row.Group, row.Kind, row.Button = "Services", KindAction, "Set up..."
		if m.cfg.Premium {
			row.Button = "Turn off"
		}
		row.Help = "Experimental. Re-downloads songs at Premium quality using your Google account."
	case rowApple:
		row.Kind, row.Button = KindAction, "Sign in"
		if m.appleIn {
			row.Button = "Sign out"
		}
		row.Help = "Optional. 256 kbps AAC with your own Apple Music subscription."
	case rowTidal:
		row.Kind, row.Button = KindAction, "Sign in"
		if m.tidalIn {
			row.Button = "Sign out"
		}
		row.Help = "Optional. Lossless FLAC with your own Tidal subscription. Tidal's terms don't allow downloading: you read the warning first."
	case rowTidalQuality:
		row.Kind, row.Options = KindChoice, []string{"lossless", "best plain tier"}
		row.Help = "Hi-res only when Tidal serves it as a plain file, else lossless. Nothing encrypted is ever fetched."
	case rowFolderJpg:
		toggle("Write the album cover as folder.jpg in each album folder. A folder.jpg you made yourself is never touched.")
	case rowNFO:
		toggle("Write album.nfo and artist.nfo files that Kodi and Jellyfin read. One you made yourself is never touched.")
	case rowCoverPNG:
		toggle("Also write the album cover as cover.png.")
	case rowPrivate:
		toggle("No MusicBrainz tag or cover lookups at all. Downloading still contacts your music source. See PRIVACY.md.")
	case rowTest:
		row.Kind, row.Button, row.Help = KindAction, "Run test", "Connect and show each server downloads would use."
		row.Status = "Not run yet" // Value is the terminal's "press enter..." text
		if !m.signedIn {
			row.Status = "Needs a VPN set up"
		}
	}
}

// Render draws the screen with ANSI colours, as the terminal UI shows it.
func (m *Settings) Render() string { return m.Screen().ANSI() }

// SelectIndex moves the cursor to row i (a mouse click).
func (m *Settings) SelectIndex(i int) {
	if i >= 0 && i < len(settingsRows) && !m.editing {
		m.cur = i
		m.saved, m.err = false, ""
	}
}

func isStepRow(k rowKind) bool {
	switch k {
	case rowFormat, rowAlbums, rowRetry, rowTagAfter, rowPremiumAfter, rowLyricsAfter, rowLyricsLRCLIB, rowLyricsNetEase, rowVPN, rowConns, rowJobs, rowCountries, rowPrivate, rowTidalQuality, rowFolderJpg, rowNFO, rowCoverPNG:
		return true
	}
	return false
}

// CheckFolder makes sure a music folder can be created and written, so a typo shows up when
// it is typed instead of after a long download.
func CheckFolder(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	dir := config.ExpandPath(s)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't create that folder: %v", err)
	}
	f, err := os.CreateTemp(dir, ".mfy-write-test-*")
	if err != nil {
		return fmt.Errorf("can't write there: %v", err)
	}
	f.Close()
	return os.Remove(f.Name())
}
