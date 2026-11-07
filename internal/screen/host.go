package screen

import (
	"sync"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
)

// Tab is one screen of a desktop window. Make builds it when the tab is opened (the
// connections list re-reads its state, say); Keep reuses the same instance on every visit.
// Remake, if set, builds the tab again after the state behind it changed (a sign-in), from
// the old model so it can keep the cursor.
type Tab struct {
	Name   string
	Make   func() Model
	Keep   bool
	Remake func(old Model) Model
	model  Model
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
}

// Host holds the tabs of a desktop window and turns events into views. Every desktop front
// end (the web page, the native window) drives the same Host, so they behave the same.
type Host struct {
	mu      sync.Mutex
	tabs    []*Tab
	current int

	// OnAction, if set, is called with the work a screen asks for ("signin", "premium", ...)
	// after the event that asked. It must return at once: the window runs the work (dialogs
	// and all) in the background.
	OnAction func(action string)
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

// View is the current state.
func (h *Host) View() View {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.view()
}

func (h *Host) view() View {
	m := h.tabs[h.current].model
	v := View{Screen: m.Screen(), Editing: m.Editing(), Current: h.current}
	for _, t := range h.tabs {
		v.Tabs = append(v.Tabs, t.Name)
	}
	return v
}

// Refresh builds the open tab again from the state on disk (after a sign-in or sign-out).
func (h *Host) Refresh() View {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tabs[h.current]
	switch {
	case t.Remake != nil:
		t.model = t.Remake(t.model)
	case !t.Keep:
		t.model = t.Make()
	}
	return h.view()
}

// Do applies an event and returns the new state.
func (h *Host) Do(ev Event) View {
	act, v := h.do(ev)
	if act != "" && h.OnAction != nil {
		h.OnAction(act)
	}
	return v
}

func (h *Host) do(ev Event) (string, View) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	// Work for the window to do (a sign-in): hand it over and carry on.
	act := m.Action()
	if act != "" {
		if c, ok := m.(interface{ ClearAction() }); ok {
			c.ClearAction()
		}
	}
	return act, h.view()
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

// DefaultTabs are the screens of the desktop window: connections and settings.
func DefaultTabs() []*Tab { return defaultTabs() }

// DesktopTabs are the screens of the desktop window: Downloads, the Library imports, the
// Tools, then the connections and the settings.
func DesktopTabs(d *Downloads, library, tools *Menu) []*Tab {
	tabs := []*Tab{
		{Name: "downloads", Keep: true, Make: func() Model { return d }},
		{Name: "library", Keep: true, Make: func() Model { return library }},
		{Name: "tools", Keep: true, Make: func() Model { return tools }},
	}
	return append(tabs, defaultTabs()...)
}

func tidalSignedIn() bool {
	p, err := tidal.SessionPath()
	return err == nil && tidal.New(p).SignedIn()
}

func defaultTabs() []*Tab {
	return []*Tab{
		{Name: "connections",
			Make: func() Model { return NewConnections(DetectConnections(), 0) },
			Remake: func(old Model) Model {
				cur := 0
				if c, ok := old.(*Connections); ok {
					cur = c.Cursor()
				}
				return NewConnections(DetectConnections(), cur)
			}},
		{Name: "settings", Keep: true,
			Make: func() Model { return newSettingsModel(0) },
			Remake: func(old Model) Model {
				cur := 0
				if s, ok := old.(*Settings); ok {
					cur = s.Cursor()
				}
				return newSettingsModel(cur)
			}},
	}
}

func newSettingsModel(cur int) *Settings {
	cfg := config.Load()
	v := DetectVPN(cfg)
	m := NewSettings(cfg, v.SignedIn, v.Who, cur, func(c config.Config) error { return c.Save() }, CheckFolder)
	m.SetProvider(v.Provider)
	m.SetApple(config.AppleToken() != "")
	m.SetTidal(tidalSignedIn())
	return m
}
