package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

type rowKind int

const (
	rowFolder rowKind = iota
	rowFormat
	rowAlbums // the extras of a library download, all on by default
	rowRetry
	rowTagAfter
	rowPremiumAfter
	rowVPN
	rowAccount
	rowConns
	rowJobs
	rowCountries
	rowPremium
	rowApple
	rowTest
)

const (
	maxConns = 12
	maxJobs  = 64
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
	{rowVPN, "VPN"},
	{rowAccount, "Proton account"},
	{rowConns, "VPN connections"},
	{rowJobs, "Parallel downloads"},
	{rowCountries, "Exit countries"},
	{rowPremium, "YouTube Premium"},
	{rowApple, "Apple Music"},
	{rowTest, "Test VPN"},
}

// settingsModel is a small inline bubbletea model: every setting on one screen, changed
// with the arrow keys and saved the moment it changes.
type settingsModel struct {
	cfg      config.Config
	signedIn bool
	appleIn  bool // an Apple Music login is saved
	user     string
	cur      int

	editing bool   // typing a folder path
	input   string // the folder being typed
	err     string // validation / save problem for the highlighted row
	saved   bool   // show the "saved" indicator

	action string // "signin", "signout", "test": work to do outside the program
	done   bool

	save  func(config.Config) error
	check func(string) error
}

func newSettingsModel(cfg config.Config, signedIn bool, user string, cur int) *settingsModel {
	return &settingsModel{
		cfg: cfg, signedIn: signedIn, user: user, cur: cur,
		save: func(c config.Config) error { return c.Save() }, check: checkFolder,
	}
}

func (m *settingsModel) Init() tea.Cmd { return nil }

func (m *settingsModel) kind() rowKind { return settingsRows[m.cur].kind }

func (m *settingsModel) vpnUsable() bool { return m.signedIn }

func (m *settingsModel) persist() {
	if err := m.save(m.cfg); err != nil {
		m.err, m.saved = "couldn't save: "+err.Error(), false
		return
	}
	m.err, m.saved = "", true
}

func (m *settingsModel) move(d int) {
	m.cur = (m.cur + d + len(settingsRows)) % len(settingsRows)
	m.saved, m.err = false, ""
}

func (m *settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.editing {
			m.input += strings.TrimSpace(string(msg.Content))
		}
	case tea.KeyPressMsg:
		if m.editing {
			return m, m.editKey(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.done = true
			return m, tea.Quit
		case "up", "k":
			m.move(-1)
		case "down", "j", "tab":
			m.move(1)
		case "left", "h":
			m.change(-1)
		case "right", "l", "space":
			m.change(1)
		case "enter":
			return m, m.activate()
		}
	}
	return m, nil
}

func (m *settingsModel) editKey(msg tea.KeyPressMsg) tea.Cmd {
	switch s := msg.String(); s {
	case "ctrl+c":
		m.done = true
		return tea.Quit
	case "esc":
		m.editing, m.err = false, ""
	case "enter":
		if err := m.check(m.input); err != nil {
			m.err = err.Error()
			return nil
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
		if msg.Text != "" {
			m.input += msg.Text
			m.err = ""
		}
	}
	return nil
}

func (m *settingsModel) activate() tea.Cmd {
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
		return tea.Quit
	case rowTest:
		if !m.signedIn {
			m.err = "sign in to Proton first"
			return nil
		}
		m.action = "test"
		return tea.Quit
	case rowPremium:
		m.action = "premium"
		return tea.Quit
	case rowApple:
		m.action = "applein"
		if m.appleIn {
			m.action = "appleout"
		}
		return tea.Quit
	}
	return nil
}

// change steps the highlighted value by d (-1 or +1) and saves it.
func (m *settingsModel) change(d int) {
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
	case rowVPN:
		if !m.vpnUsable() {
			m.err = "sign in to Proton first"
			return
		}
		m.cfg.NoVPN = !m.cfg.NoVPN
	case rowConns:
		n := m.cfg.Connections
		if n == 0 {
			n = 3 // unset
		}
		if n = clamp(n+d, -1, maxConns); n == 0 {
			n += d // 0 means "unset": step over it, to 1 or to Auto (-1)
		}
		m.cfg.Connections = clamp(n, -1, maxConns)
	case rowJobs:
		m.cfg.Jobs = clamp(m.cfg.Jobs+d, 0, maxJobs)
	case rowCountries:
		m.cfg.Countries = cycle(countryChoices, m.cfg.Countries, d)
	default:
		return
	}
	m.persist()
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

const (
	cDim    = "\x1b[2m"
	cAccent = "\x1b[1;36m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cReset  = "\x1b[0m"
)

func (m *settingsModel) value(k rowKind) (val, hint string, dim bool) {
	switch k {
	case rowFolder:
		return tilde(config.ExpandPath(orDefault(m.cfg.Out, config.DefaultOut()))), "enter to type a new folder", false
	case rowFormat:
		f := orDefault(m.cfg.Format, config.DefaultFormat)
		if f == "opus" {
			return f, "YouTube's own audio, copied untouched (others are re-encoded)", false
		}
		return f, "re-encoded from YouTube's audio; opus keeps it untouched", false
	case rowAlbums:
		return onOff(!m.cfg.NoAlbums), "a library download also gets the rest of every album you have a song from", false
	case rowRetry:
		return onOff(!m.cfg.NoRetry), "songs that were unavailable last time are tried again (incl. on YouTube Music)", false
	case rowTagAfter:
		return onOff(!m.cfg.NoTagAfter), "the songs just downloaded get MusicBrainz tags (only when it is certain)", false
	case rowPremiumAfter:
		hint := "after downloading, upgrade songs to Premium quality (slow; uses your account)"
		if !m.cfg.Premium {
			hint = "needs a YouTube Premium sign-in: set it up in the YouTube Premium row below"
		}
		return onOff(!m.cfg.NoPremiumAfter), hint, !m.cfg.Premium
	case rowVPN:
		if !m.signedIn {
			return "off", "sign in to Proton below to use it", true
		}
		if m.cfg.NoVPN {
			return "off", "", false
		}
		return "on", "", false
	case rowAccount:
		if m.signedIn {
			return "signed in as " + m.user, "enter to sign out", false
		}
		return "not signed in", "enter to sign in", false
	case rowConns:
		if m.cfg.Connections < 0 {
			return "Auto", "your Proton plan's device limit, minus one kept as a spare", !m.signedIn || m.cfg.NoVPN
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

func (m *settingsModel) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  \x1b[1mytm settings\x1b[0m  %s%s%s\n\n", cDim, tilde(config.Path()), cReset)
	for i, r := range settingsRows {
		sel := i == m.cur
		val, hint, dim := m.value(r.kind)
		marker, label := "  ", cDim+fmt.Sprintf("%-*s", labelWidth(), r.label)+cReset
		if sel {
			marker, label = cAccent+"› "+cReset, cAccent+fmt.Sprintf("%-*s", labelWidth(), r.label)+cReset
		}
		switch {
		case sel && m.editing && r.kind == rowFolder:
			val = m.input + cAccent + "▏" + cReset
		case r.kind == rowTest:
			// action row: value text only
			if sel {
				val = cAccent + val + cReset
			} else {
				val = cDim + val + cReset
			}
		case dim:
			val = cDim + val + cReset
		case sel && isStepRow(r.kind):
			val = cAccent + "‹ " + val + " ›" + cReset
		case sel:
			val = cAccent + val + cReset
		default:
			if val == "on" || strings.HasPrefix(val, "signed in") {
				val = cGreen + val + cReset
			}
		}
		fmt.Fprintf(&b, "  %s%s  %s\n", marker, label, val)
		if sel {
			switch {
			case m.err != "":
				fmt.Fprintf(&b, "      %s%s%s\n", cYellow, m.err, cReset)
			case m.editing:
				fmt.Fprintf(&b, "      %senter to save · esc to cancel%s\n", cYellow, cReset)
			case hint != "":
				fmt.Fprintf(&b, "      %s%s%s\n", cYellow, hint, cReset)
			}
		}
	}
	b.WriteString("\n  ")
	if m.editing {
		b.WriteString(cDim + "type a folder · enter save · esc cancel" + cReset)
	} else {
		b.WriteString(cDim + "↑↓ select · ←→ change · enter edit · esc done" + cReset)
	}
	if m.saved {
		b.WriteString("   " + cGreen + "✓ saved" + cReset)
	}
	b.WriteString("\n")
	return b.String()
}

// printErr reports a failed settings action; backing out of it (Esc, Ctrl-C) isn't one.
func printErr(err error) {
	if err != nil && !errors.Is(err, errBack) && !errors.Is(err, context.Canceled) {
		fmt.Println("  ✗", err)
	}
}

func isStepRow(k rowKind) bool {
	switch k {
	case rowFormat, rowAlbums, rowRetry, rowTagAfter, rowPremiumAfter, rowVPN, rowConns, rowJobs, rowCountries:
		return true
	}
	return false
}

func (m *settingsModel) View() tea.View {
	if m.done || m.action != "" {
		return tea.NewView("") // leave a clean line behind
	}
	return tea.NewView(m.render())
}

// runSettings shows the settings screen. Sign-in/out and the VPN test need the plain
// terminal, so the program quits, the action runs, and the screen comes back where it was.
func runSettings(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("settings need a terminal")
	}
	pc := protonClient()
	cur := 0
	if !pc.SignedIn() {
		cur = int(rowAccount) // first run: land on the sign-in row
	}
	for {
		cfg := config.Load()
		user := ""
		if pc.SignedIn() {
			user = pc.Session.Username
		}
		m := newSettingsModel(cfg, pc.SignedIn(), user, cur)
		m.appleIn = appleSignedIn()
		final, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
		if err != nil {
			return err
		}
		fm := final.(*settingsModel)
		cur = fm.cur
		switch fm.action {
		case "":
			if !config.Exists() {
				if err := fm.cfg.Save(); err != nil {
					return fmt.Errorf("couldn't save your settings to %s: %w", config.Path(), err)
				}
			}
			fmt.Printf("  %s✓%s settings kept in %s\n", cGreen, cReset, config.Path())
			return nil
		case "signout":
			pc.Logout()
		case "signin":
			c := config.Load()
			if err := loginFlow(ctx, pc, &c); err != nil {
				printErr(err)
			}
			if err := c.Save(); err != nil {
				return err
			}
		case "premium":
			if err := premiumSetup(ctx, u); err != nil {
				printErr(err)
			}
		case "applein":
			if err := appleLogin(ctx, u); err != nil {
				printErr(err)
			}
		case "appleout":
			if err := appleLogout(u); err != nil {
				printErr(err)
			}
		case "test":
			if err := runStatus(ctx, u); err != nil {
				printErr(err)
			}
			fmt.Print("\n  press enter to go back ")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
	}
}
