package screen

import (
	"strings"
	"testing"
)

// Every settings row says what control suits it, and sections start where expected.
func TestSettingsRowsCarryKinds(t *testing.T) {
	h, _ := testHost()
	rows := h.View().Screen.Rows
	want := map[string]string{
		"Music folder": KindText, "Format": KindChoice, "Complete albums": KindToggle,
		"VPN": KindToggle, "Proton account": KindAction, "VPN connections": KindNumber,
		"Parallel downloads": KindNumber, "Exit countries": KindChoice, "Test VPN": KindAction,
	}
	groups := 0
	for _, r := range rows {
		if k, ok := want[r.Label]; ok && r.Kind != k {
			t.Errorf("%s: kind %q, want %q", r.Label, r.Kind, k)
		}
		if r.Kind == KindChoice && len(r.Options) < 2 {
			t.Errorf("%s: a choice needs options", r.Label)
		}
		if r.Kind == KindAction && r.Button == "" {
			t.Errorf("%s: an action needs a button label", r.Label)
		}
		if r.Group != "" {
			groups++
		}
	}
	if groups != 3 || rows[0].Group == "" {
		t.Fatalf("want 3 sections starting at the first row, got %d", groups)
	}
}

// A graphical host's clicks become the keys the terminal uses, and nothing more.
func TestSetEventsAreKeys(t *testing.T) {
	h, last := testHost()
	h.Do(Event{Set: &Set{Row: 2, Value: "off"}})
	if !last.NoAlbums {
		t.Fatal("switch off")
	}
	h.Do(Event{Set: &Set{Row: 2, Value: "off"}}) // already off: no change
	if !last.NoAlbums {
		t.Fatal("setting the same value twice must not flip it back")
	}
	h.Do(Event{Set: &Set{Row: 1, Value: "flac"}})
	if last.Format != "flac" {
		t.Fatalf("format %q", last.Format)
	}
	h.Do(Event{Set: &Set{Row: 13, Value: "DE"}})
	if last.Countries != "DE" {
		t.Fatalf("countries %q", last.Countries)
	}
	h.Do(Event{Set: &Set{Row: 12, Step: 1}})
	h.Do(Event{Set: &Set{Row: 12, Step: 1}})
	if last.Jobs != 2 {
		t.Fatalf("jobs %d", last.Jobs)
	}
	h.Do(Event{Set: &Set{Row: 0, Value: "/tmp/mfetchy-set-test"}})
	if !strings.HasSuffix(last.Out, "mfetchy-set-test") {
		t.Fatalf("folder %q", last.Out)
	}
	if v := h.Do(Event{Set: &Set{Row: 99, Press: true}}); v.Editing {
		t.Fatal("an out-of-range row must do nothing")
	}
	var got string
	h.OnAction = func(a string) { got = a }
	h.Do(Event{Set: &Set{Row: 10, Press: true}})
	if got != "signin" {
		t.Fatalf("pressing the VPN button should hand \"signin\" to the window, got %q", got)
	}
}

// The terminal draws only Label/Value/Selected etc. The graphical hints change nothing there.
func TestANSIIgnoresPresentationHints(t *testing.T) {
	h, _ := testHost()
	plain := h.View().Screen
	hinted := plain
	hinted.Rows = append([]Row(nil), plain.Rows...)
	for i := range hinted.Rows {
		hinted.Rows[i].Kind, hinted.Rows[i].Help, hinted.Rows[i].Group = "", "", ""
		hinted.Rows[i].Options, hinted.Rows[i].Button, hinted.Rows[i].Status = nil, "", ""
		hinted.Rows[i].On, hinted.Rows[i].Error = false, ""
	}
	if plain.ANSI() != hinted.ANSI() {
		t.Fatal("ANSI output must not depend on the presentation hints")
	}
}

// A row that sets no Kind (another feature's row) is still drawn from Label and Value.
func TestKindlessRowStillRenders(t *testing.T) {
	s := Screen{Title: "x", Rows: []Row{{Label: "Lyrics", Value: "on"}}}
	if !strings.Contains(s.ANSI(), "Lyrics") {
		t.Fatal("kindless row lost")
	}
}

func TestConnectionsKinds(t *testing.T) {
	c := NewConnections([]Connection{
		{ID: "a", Name: "A", Status: "x", Action: "applein", Button: "Sign in"},
		{ID: "b", Name: "B", Status: "ready", Dim: true},
		{ID: "c", Name: "C", Status: "y", Action: "premium"},
	}, 0)
	r := c.Screen().Rows
	if r[0].Kind != KindAction || r[0].Button != "Sign in" || r[1].Kind != KindInfo || r[2].Button == "" {
		t.Fatalf("%+v", r)
	}
}
