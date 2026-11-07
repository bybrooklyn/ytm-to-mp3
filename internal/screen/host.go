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
