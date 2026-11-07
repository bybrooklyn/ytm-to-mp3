package screen

import (
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/config"
)

func testHost() (*Host, *config.Config) {
	var last config.Config
	return NewHost(
		&Tab{Name: "settings", Keep: true, Make: func() Model {
			return NewSettings(config.Config{}, false, "", 0,
				func(c config.Config) error { last = c; return nil }, func(string) error { return nil })
		}},
		&Tab{Name: "connections", Make: func() Model {
			return NewConnections([]Connection{{ID: "a", Name: "A", Status: "x", Action: "premium"}, {ID: "b", Name: "B", Status: "y"}}, 0)
		}},
	), &last
}

func ev(key string) Event { return Event{Key: key} }

func TestHostKeysDriveTheSameScreenAsTheTerminal(t *testing.T) {
	h, last := testHost()
	v := h.Do(ev("down"))
	if !v.Screen.Rows[1].Selected || v.Screen.Rows[0].Selected {
		t.Fatalf("down should select the second row: %+v", v.Screen.Rows[:2])
	}
	v = h.Do(ev("right")) // Format steps
	if last.Format == "" || !v.Screen.Saved {
		t.Fatalf("a change must be saved: format=%q saved=%v", last.Format, v.Screen.Saved)
	}
	row := 4
	if v = h.Do(Event{Row: &row}); !v.Screen.Rows[4].Selected {
		t.Fatal("a click should select the row")
	}
}

func TestHostEscDoesNotCloseAndActionsExplainThemselves(t *testing.T) {
	h, _ := testHost()
	if v := h.Do(ev("esc")); len(v.Screen.Rows) == 0 {
		t.Fatal("esc must leave the screen as it is")
	}
	tab := 1
	if v := h.Do(Event{Tab: &tab}); v.Current != 1 || v.Screen.Title != "connections" || len(v.Tabs) != 2 {
		t.Fatalf("%+v", v)
	}
	v := h.Do(ev("enter")) // the first connection has an action a window can't run
	if v.Notice == "" {
		t.Fatal("expected an explanation")
	}
	if v = h.Do(ev("down")); v.Notice != "" {
		t.Error("the notice goes away on the next event")
	}
}

func TestHostTabsKeepOrRebuild(t *testing.T) {
	h, _ := testHost()
	h.Do(ev("down"))
	one, zero := 1, 0
	h.Do(Event{Tab: &one})
	if v := h.Do(Event{Tab: &zero}); !v.Screen.Rows[1].Selected {
		t.Fatal("a kept tab keeps its cursor between visits")
	}
	bad := 99
	if v := h.Do(Event{Tab: &bad}); v.Current != 0 {
		t.Fatal("a bad tab is ignored")
	}
}

func TestHostTypingWhileEditingSavesFolder(t *testing.T) {
	h, last := testHost()
	h.Do(ev("enter")) // edit the music folder
	if v := h.Do(Event{Text: "/tmp/x"}); !v.Editing {
		t.Fatal("should be editing")
	}
	if v := h.Do(ev("esc")); v.Editing { // esc cancels an edit even in a window
		t.Fatal("esc should cancel the edit")
	}
	h.Do(ev("enter"))
	h.Do(ev("ctrl+u"))
	h.Do(Event{Paste: "/tmp/music"})
	h.Do(ev("enter"))
	if last.Out != config.ExpandPath("/tmp/music") {
		t.Fatalf("out=%q", last.Out)
	}
}
