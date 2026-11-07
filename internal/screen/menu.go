package screen

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
)

// MenuItem is one action of a Menu: a label, what it does, and the button's text.
type MenuItem struct {
	ID     string
	Label  string
	Help   string
	Button string // "Open" when empty
	Group  string // starts a section with this heading
}

// Menu is a page of actions (Library, Tools): a title, sections of rows, each row a button.
// Pressing one calls the function the page was built with; that function starts the work
// and returns at once. The Tools page also runs its work as a Task: the Menu keeps the log
// of the run and whether it is still going, for a window to draw under the buttons.
// The terminal never shows it.
type Menu struct {
	mu       sync.Mutex
	title    string
	subtitle string
	items    []MenuItem
	cur      int
	press    func(id string)

	task   *taskState
	cancel context.CancelFunc

	// OnChange, if set, is called after the task's state or log changes (from any goroutine).
	OnChange func()
}

type taskState struct {
	title  string
	state  string // "running", "done", "failed", "cancelled"
	log    strings.Builder
	err    string
	cancel context.CancelFunc
}

// TaskView is the run a Menu last started, as a window draws it.
type TaskView struct {
	Title string
	State string // "running", "done", "failed", "cancelled"
	Log   string // plain text, as it was printed
	Error string // why it failed
}

// NewMenu builds a page. press is called with the ID of the row the person pressed.
func NewMenu(title, subtitle string, items []MenuItem, press func(id string)) *Menu {
	return &Menu{title: title, subtitle: subtitle, items: items, press: press}
}

// ---- Model ---------------------------------------------------------------------------

func (m *Menu) Editing() bool  { return false }
func (m *Menu) Finished() bool { return false }
func (m *Menu) Action() string { return "" }
func (m *Menu) Type(string)    {}
func (m *Menu) Paste(string)   {}

func (m *Menu) SelectIndex(i int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= 0 && i < len(m.items) {
		m.cur = i
	}
}

func (m *Menu) Key(k string) {
	m.mu.Lock()
	n := len(m.items)
	switch k {
	case "up", "k":
		m.cur = (m.cur - 1 + n) % n
	case "down", "j", "tab":
		m.cur = (m.cur + 1) % n
	case "enter", "right", "l", "space":
		id := m.items[m.cur].ID
		busy := m.task != nil && m.task.state == "running"
		m.mu.Unlock()
		if !busy && m.press != nil {
			m.press(id)
		}
		return
	}
	m.mu.Unlock()
}

// Screen is the rows, and the task under them.
func (m *Menu) Screen() Screen {
	m.mu.Lock()
	defer m.mu.Unlock()
	busy := m.task != nil && m.task.state == "running"
	sc := Screen{Title: m.title, Subtitle: m.subtitle, Footer: "↑↓ select · enter open"}
	for i, it := range m.items {
		btn := it.Button
		if btn == "" {
			btn = "Open"
		}
		sc.Rows = append(sc.Rows, Row{
			ID: it.ID, Label: it.Label, Help: it.Help, Selected: i == m.cur, Dim: busy,
			Kind: KindAction, Button: btn, Group: it.Group,
		})
	}
	if m.task != nil {
		t := m.task
		sc.Task = &TaskView{Title: t.title, State: t.state, Log: t.log.String(), Error: t.err}
	}
	return sc
}

var _ Model = (*Menu)(nil)

// ---- tasks ---------------------------------------------------------------------------

// Running reports whether a task is under way.
func (m *Menu) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.task != nil && m.task.state == "running"
}

// StartTask runs f in the background with a writer for its log, and shows it under the
// buttons until Dismiss. It refuses while another task runs.
func (m *Menu) StartTask(title string, f func(ctx context.Context, log io.Writer) error) error {
	m.mu.Lock()
	if m.task != nil && m.task.state == "running" {
		m.mu.Unlock()
		return errors.New("something is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &taskState{title: title, state: "running", cancel: cancel}
	m.task = t
	m.mu.Unlock()
	m.changed()
	go func() {
		err := f(ctx, taskLog{m, t})
		m.mu.Lock()
		switch {
		case errors.Is(err, ErrBackedOut):
			m.task = nil // asked and declined: nothing happened, nothing to show
		case ctx.Err() != nil:
			t.state = "cancelled"
		case err != nil:
			t.state, t.err = "failed", err.Error()
		default:
			t.state = "done"
		}
		m.mu.Unlock()
		cancel()
		m.changed()
	}()
	return nil
}

// Cancel stops the running task: what it was doing is cleaned up.
func (m *Menu) Cancel() {
	m.mu.Lock()
	if m.task != nil && m.task.state == "running" {
		m.task.cancel()
	}
	m.mu.Unlock()
}

// Dismiss clears a finished task from the page.
func (m *Menu) Dismiss() {
	m.mu.Lock()
	if m.task != nil && m.task.state != "running" {
		m.task = nil
	}
	m.mu.Unlock()
	m.changed()
}

func (m *Menu) changed() {
	m.mu.Lock()
	f := m.OnChange
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

// taskLog is the writer a task prints into.
type taskLog struct {
	m *Menu
	t *taskState
}

func (w taskLog) Write(p []byte) (int, error) {
	w.m.mu.Lock()
	w.t.log.Write(p)
	w.m.mu.Unlock()
	w.m.changed()
	return len(p), nil
}
