package screen

import (
	"sync"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
)

// Tab is one screen of a desktop window. Make builds it when the tab is opened (the
// connections list re-reads its state, say); Keep reuses the same instance on every visit.
type Tab struct {
	Name  string
	Make  func() Model
	Keep  bool
	model Model
}

// Event is one thing the user did in a window: pick a tab, click a row, type, paste, or press
// a key (named as in Model.Key). Exactly one field is set.
type Event struct {
	Tab   *int   `json:"tab"`
	Row   *int   `json:"row"`
	Key   string `json:"key"`
	Text  string `json:"text"`
	Paste string `json:"paste"`
	Set   *Set   `json:"set"`
}

// Set is a click on a row's own control. The host turns it into the same keys the terminal
// uses, so a switch, a dropdown or a button can't do anything a key press couldn't.
type Set struct {
	Row   int    `json:"row"`
	Value string `json:"value"` // toggle: "on"/"off"; choice: an option; text: the new text
	Step  int    `json:"step"`  // number: +1 or -1
	Press bool   `json:"press"` // action: run it
}

// View is what a window draws after an event.
type View struct {
	Screen  Screen   `json:"screen"`
	Editing bool     `json:"editing"`
	Tabs    []string `json:"tabs"`
	Current int      `json:"current"`
	Notice  string   `json:"notice,omitempty"`
}

// Host holds the tabs of a desktop window and turns events into views. Every desktop front
// end (the web page, the native window) drives the same Host, so they behave the same.
type Host struct {
	mu      sync.Mutex
	tabs    []*Tab
	current int
	notice  string // a message for the window ("sign-in happens in the terminal")
}

// NewHost opens the first tab.
func NewHost(tabs ...*Tab) *Host {
	h := &Host{tabs: tabs}
	h.open(0)
	return h
}

func (h *Host) open(i int) {
	t := h.tabs[i]
	if t.model == nil || !t.Keep {
		t.model = t.Make()
	}
	h.current = i
}

// ActionNotice explains an action a window can't run itself (the sign-ins are interactive
// terminal flows for now).
var ActionNotice = map[string]string{
	"signin":   "Signing in to Proton happens in the terminal: run ytm, then Connections.",
	"signout":  "Signing out happens in the terminal: run ytm, then Connections.",
	"test":     "The VPN test runs in the terminal: run ytm, then Settings > Test VPN.",
	"premium":  "The YouTube Premium setup runs in the terminal: run ytm, then Connections.",
	"applein":  "Apple Music sign-in runs in the terminal: run ytm, then Connections.",
	"appleout": "Apple Music sign-out runs in the terminal: run ytm, then Connections.",
	"bandcamp": "Choosing the browser for your Bandcamp purchases happens in the terminal: run ytm, then Connections.",
	"youtube":  "Choosing the browser for your YouTube Music library happens in the terminal: run ytm, then Connections.",
}

// View is the current state.
func (h *Host) View() View {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.view()
}

func (h *Host) view() View {
	m := h.tabs[h.current].model
	v := View{Screen: m.Screen(), Editing: m.Editing(), Current: h.current, Notice: h.notice}
	for _, t := range h.tabs {
		v.Tabs = append(v.Tabs, t.Name)
	}
	return v
}

// Do applies an event and returns the new state.
func (h *Host) Do(ev Event) View {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notice = ""
	m := h.tabs[h.current].model
	switch {
	case ev.Set != nil:
		h.set(m, *ev.Set)
	case ev.Tab != nil:
		if i := *ev.Tab; i >= 0 && i < len(h.tabs) {
			h.open(i)
		}
	case ev.Row != nil:
		m.SelectIndex(*ev.Row)
	case ev.Paste != "":
		m.Paste(ev.Paste)
	case ev.Text != "":
		m.Type(ev.Text)
	case ev.Key != "":
		// Leaving a screen is closing the window here, which the window does itself, so
		// Esc, q and Ctrl-C only mean something while a field is being typed in.
		if (ev.Key == "esc" || ev.Key == "q" || ev.Key == "ctrl+c") && !m.Editing() {
			break
		}
		m.Key(ev.Key)
	}
	// Work that needs the terminal can't run here: say so and carry on.
	if act := m.Action(); act != "" {
		h.notice = ActionNotice[act]
		if c, ok := m.(interface{ ClearAction() }); ok {
			c.ClearAction()
		}
	}
	return h.view()
}

// set applies a click on a row's control as the keys the terminal would use.
func (h *Host) set(m Model, s Set) {
	if m.Editing() {
		return
	}
	m.SelectIndex(s.Row)
	rows := m.Screen().Rows
	if s.Row < 0 || s.Row >= len(rows) {
		return
	}
	switch r := rows[s.Row]; {
	case s.Press:
		m.Key("enter")
	case s.Step != 0:
		if s.Step > 0 {
			m.Key("right")
		} else {
			m.Key("left")
		}
	case r.Kind == KindText:
		m.Key("enter") // starts editing, with the current text
		if m.Editing() {
			m.Key("ctrl+u")
			m.Type(s.Value)
			m.Key("enter")
		}
	case r.Kind == KindToggle:
		if (s.Value == "on") != r.On {
			m.Key("right")
		}
	default: // choice: step to it (the list wraps, so a few steps at most)
		for i := 0; i <= len(r.Options) && m.Screen().Rows[s.Row].Value != s.Value; i++ {
			m.Key("right")
		}
	}
}

// DefaultTabs are the screens of the desktop window: connections and settings. signedIn and
// user say whether a Proton session exists (for the settings screen).
func DefaultTabs(signedIn bool, user string) []*Tab {
	return []*Tab{
		{Name: "connections", Make: func() Model { return NewConnections(DetectConnections(), 0) }},
		{Name: "settings", Keep: true, Make: func() Model {
			return NewSettings(config.Load(), signedIn, user, 0, func(c config.Config) error { return c.Save() }, CheckFolder)
		}},
	}
}
