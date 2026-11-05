package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/spotify"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
)

// homeItem is one thing ytm can do, as listed on the home screen.
type homeItem struct {
	key, label, hint string
}

// homeSection is a heading over a few items.
type homeSection struct {
	title string
	items []homeItem
}

// homeSections is everything ytm does, on one screen: no submenus, no subcommands.
func homeSections(appleIn, premium bool) []homeSection {
	apple := "256 kbps AAC copies of the songs you have (asks you to sign in first)"
	if appleIn {
		apple = "find your songs on Apple Music and get them as 256 kbps AAC"
	}
	prem := "re-download at Premium quality with your account (experimental; set it up first)"
	if premium {
		prem = "re-download at Premium quality with your account (experimental, slow)"
	}
	return []homeSection{
		{"Download", []homeItem{
			{"library", "Download my YouTube Music library", "from your Google Takeout export; ytm finds it, or shows you how to get one"},
			{"link", "Download links", "paste YouTube, Apple Music or Spotify links in the box above"},
		}},
		{"Improve what you have", []homeItem{
			{"apple", "Upgrade my library to Apple Music", apple},
			{"premium", "YouTube Premium upgrade", prem},
			{"tag", "Fix tags", "MusicBrainz: release dates, track numbers, corrected names (preview first)"},
			{"tidy", "Tidy file names", "rename to Artist/Album/01 - Title (preview first)"},
		}},
		{"ytm", []homeItem{
			{"settings", "Settings", "music folder, format, extras, VPN, and the Proton, Apple Music and Premium sign-ins"},
			{"doctor", "Check that everything works", "tools, network, folders and sign-ins, with how to fix what isn't"},
			{"update", "Update yt-dlp, ffmpeg and deno", ""},
			{"quit", "Quit", ""},
		}},
	}
}

// homeStatus is what the home screen says about your setup and library.
type homeStatus struct {
	out, format string
	vpn         string // "Proton VPN, 6 connections" / "off ..."
	vpnOK       bool
	apple       string
	appleOK     bool
	library     string // "1,234 songs (1,100 from YouTube, 134 from Apple Music)" or ""
}

func readHomeStatus(o opts) homeStatus {
	cfg := config.Load()
	pc := protonClient()
	s := homeStatus{out: tilde(o.out), format: o.format}
	switch {
	case o.noVPN || cfg.NoVPN:
		s.vpn = "off (Settings to turn it on)"
	case !pc.SignedIn():
		s.vpn = "not signed in: downloads use your own address (Settings to sign in)"
	default:
		n := connectionsLabel(cfg, pc)
		if o.conns > 0 {
			n = fmt.Sprint(min(o.conns, maxConns))
		}
		s.vpn, s.vpnOK = "Proton VPN, "+count(atoiOr(n, 0), "connection"), true
		if strings.HasPrefix(n, "auto") {
			s.vpn = "Proton VPN, " + n + " connections"
		}
	}
	if appleSignedIn() {
		s.apple, s.appleOK = "signed in (store "+strings.ToUpper(orDefault(cfg.AppleStorefront, "us"))+"): songs come from there first", true
	} else {
		s.apple = "not signed in: Apple Music and Spotify songs come from YouTube"
	}
	s.library = librarySummary(o.out)
	return s
}

func atoiOr(s string, def int) int {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		return def
	}
	return n
}

// librarySummary counts what the music folder's database knows is downloaded ("" if it
// has none yet). It never creates the database.
func librarySummary(out string) string {
	dir := filepath.Join(out, ".ytm")
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		return ""
	}
	st, err := store.Open(dir)
	if err != nil {
		return ""
	}
	defer st.Close()
	hs, err := st.Held()
	if err != nil || len(hs) == 0 {
		return ""
	}
	by := map[string]int{}
	for _, h := range hs {
		by[h.Source]++
	}
	var parts []string
	if n := by["youtube"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s from YouTube", thousands(n)))
	}
	if n := by["apple"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s from Apple Music", thousands(n)))
	}
	s := thousands(len(hs)) + " songs"
	if len(hs) == 1 {
		s = "1 song"
	}
	if len(parts) > 1 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

// thousands writes 1234567 as "1,234,567".
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// parseInputs reads what was typed or pasted in the home screen's box: links from YouTube,
// Apple Music or Spotify, and paths to a Takeout export or a file of links.
func parseInputs(s string) ([]string, error) {
	var out []string
	for _, f := range takeout.SplitPaths(s) {
		f = strings.Trim(f, `"'`)
		switch {
		case apple.IsURL(f) || spotify.IsURL(f):
			out = append(out, f)
		default:
			if l, ok := asURL(f); ok {
				out = append(out, l)
				continue
			}
			if _, err := os.Stat(f); err == nil {
				out = append(out, f)
				continue
			}
			return nil, fmt.Errorf("%q isn't a YouTube, Apple Music or Spotify link, or a file here", ansi.Truncate(f, 60, "…"))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("paste a link (or the path to your Takeout export) first")
	}
	return out, nil
}

// homeModel is the home screen: a box to paste links into, what ytm knows about your
// setup and library, and every action in one list.
type homeModel struct {
	status   homeStatus
	sections []homeSection
	items    []homeItem // flattened, in order
	cur      int
	input    textinput.Model
	note     string // a message under the box (an error, or a hint)
	noteBad  bool
	w, h     int

	action string   // what was chosen; "" = quit
	inputs []string // for action "input"
}

func newHomeModel(o opts, cur int, note string) *homeModel {
	cfg := config.Load()
	m := &homeModel{status: readHomeStatus(o), sections: homeSections(appleSignedIn(), cfg.Premium), cur: cur, note: note, w: 80, h: 24}
	for _, s := range m.sections {
		m.items = append(m.items, s.items...)
	}
	m.cur = max(0, min(m.cur, len(m.items)-1))
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Placeholder = "paste a YouTube, Apple Music or Spotify link and press enter"
	m.input.Focus()
	return m
}

func (m *homeModel) Init() tea.Cmd { return textinput.Blink }

func (m *homeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.SetWidth(max(10, homeBoxWidth(m.w)-8))
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.action = ""
			return m, tea.Quit
		case "esc":
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.note = ""
				return m, nil
			}
			m.action = ""
			return m, tea.Quit
		case "up", "shift+tab":
			m.cur = (m.cur - 1 + len(m.items)) % len(m.items)
			return m, nil
		case "down", "tab":
			m.cur = (m.cur + 1) % len(m.items)
			return m, nil
		case "home":
			m.cur = 0
			return m, nil
		case "end":
			m.cur = len(m.items) - 1
			return m, nil
		case "enter":
			if strings.TrimSpace(m.input.Value()) != "" {
				ins, err := parseInputs(m.input.Value())
				if err != nil {
					m.note, m.noteBad = err.Error(), true
					return m, nil
				}
				m.action, m.inputs = "input", ins
				return m, tea.Quit
			}
			switch k := m.items[m.cur].key; k {
			case "link":
				m.note, m.noteBad = "Paste or type the link in the box, then press enter.", false
				return m, nil
			case "quit":
				m.action = ""
				return m, tea.Quit
			default:
				m.action = k
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.note = ""
	}
	return m, cmd
}

const (
	cBold  = "\x1b[1m"
	cRed   = "\x1b[31m"
	cFaint = "\x1b[2m"
)

func (m *homeModel) View() tea.View {
	if m.action != "" {
		return tea.View{AltScreen: true}
	}
	w := max(40, m.w)
	var b strings.Builder
	line := func(s string) {
		b.WriteString(ansi.Truncate(s, w-1, "…"))
		b.WriteString("\n")
	}
	ok := func(good bool) string {
		if good {
			return cGreen + "●" + cReset
		}
		return cYellow + "○" + cReset
	}

	// On a short terminal the spacing goes first, so the keys line always fits.
	compact := m.h > 0 && m.h < 30
	gap := func() {
		if !compact {
			line("")
		}
	}
	gap()
	title := "  " + cAccent + "ytm" + cReset + cFaint + "  your music, from YouTube Music, Apple Music and Spotify" + cReset
	v := version
	if v != "dev" {
		v = "v" + strings.TrimPrefix(v, "v")
	}
	if ansi.StringWidth(title)+len(v)+4 < w {
		title += strings.Repeat(" ", w-ansi.StringWidth(title)-len(v)-3) + cFaint + v + cReset
	}
	line(title)
	line("")
	lib := m.status.library
	if lib == "" {
		lib = "no songs yet"
	}
	line(fmt.Sprintf("  %s %-7s %s  %s·  %s  ·  %s%s", ok(true), "Music", m.status.out, cFaint, m.status.format, lib, cReset))
	line(fmt.Sprintf("  %s %-7s %s", ok(m.status.vpnOK), "VPN", dimUnless(m.status.vpn, m.status.vpnOK)))
	line(fmt.Sprintf("  %s %-7s %s", ok(m.status.appleOK), "Apple", dimUnless(m.status.apple, m.status.appleOK)))
	line("")

	// The box: links typed or pasted here download straight away.
	boxW := homeBoxWidth(w)
	line("  " + cFaint + "╭" + strings.Repeat("─", boxW-2) + "╮" + cReset)
	in := ansi.Truncate(m.input.View(), boxW-6, "…")
	pad := max(0, boxW-6-ansi.StringWidth(in))
	line("  " + cFaint + "│" + cReset + " " + cAccent + "›" + cReset + " " + in + strings.Repeat(" ", pad) + " " + cFaint + "│" + cReset)
	line("  " + cFaint + "╰" + strings.Repeat("─", boxW-2) + "╯" + cReset)
	switch {
	case m.note != "" && m.noteBad:
		line("  " + cRed + "✗ " + m.note + cReset)
	case m.note != "":
		line("  " + cYellow + m.note + cReset)
	default:
		line("")
	}

	// The actions (with section headings when there's room), then the highlighted one's
	// explanation.
	i := 0
	for si, s := range m.sections {
		if !compact {
			if si > 0 {
				line("")
			}
			line("  " + cFaint + strings.ToUpper(s.title) + cReset)
		}
		for _, it := range s.items {
			if i == m.cur {
				line("  " + cAccent + "› " + it.label + cReset)
			} else {
				line("    " + it.label)
			}
			i++
		}
	}
	gap()
	if hint := m.items[m.cur].hint; hint != "" {
		line("  " + cFaint + hint + cReset)
	} else {
		line("")
	}
	line("")
	line("  " + cFaint + "↑↓ choose · enter go · paste a link to download it · esc quit" + cReset)
	view := tea.NewView(b.String())
	view.AltScreen = true
	return view
}

// homeBoxWidth is the link box's width on a terminal w columns wide.
func homeBoxWidth(w int) int { return min(max(40, w)-4, 100) }

func dimUnless(s string, bright bool) string {
	if bright {
		return s
	}
	return cFaint + s + cReset
}
