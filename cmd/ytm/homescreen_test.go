package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestParseInputs(t *testing.T) {
	zip := filepath.Join(t.TempDir(), "takeout.zip")
	if err := os.WriteFile(zip, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseInputs("https://open.spotify.com/track/2X485T9Z5Ly0xyaghN73ed  youtu.be/dQw4w9WgXcQ " +
		"https://music.apple.com/us/album/currents/1440838039 " + zip)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://open.spotify.com/track/2X485T9Z5Ly0xyaghN73ed", "https://youtu.be/dQw4w9WgXcQ",
		"https://music.apple.com/us/album/currents/1440838039", zip}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
	if _, err := parseInputs("not a link"); err == nil {
		t.Error("junk must be refused")
	}
	if _, err := parseInputs("   "); err == nil {
		t.Error("nothing must be refused")
	}
}

func homeKey(m *homeModel, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		default:
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		m.Update(msg)
	}
}

func TestHomeKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no saved settings or sign-ins
	o := opts{out: t.TempDir(), format: "opus"}

	m := newHomeModel(o, 0, "")
	homeKey(m, "up") // wraps to the last item
	if m.items[m.cur].key != "quit" {
		t.Fatalf("up from the top should wrap to Quit, got %q", m.items[m.cur].key)
	}
	homeKey(m, "down", "down", "down", "enter") // library, link, apple
	if m.action != "apple" {
		t.Fatalf("action = %q", m.action)
	}

	// Typing goes to the box; Esc clears it first, and only then quits.
	m = newHomeModel(o, 0, "")
	homeKey(m, "x", "y")
	if m.input.Value() != "xy" {
		t.Fatalf("box = %q", m.input.Value())
	}
	homeKey(m, "enter")
	if m.action != "" || !m.noteBad {
		t.Fatalf("junk in the box must show an error, not run (action %q, note %q)", m.action, m.note)
	}
	homeKey(m, "esc")
	if m.input.Value() != "" || m.action != "" {
		t.Fatal("esc should clear the box")
	}

	// A pasted link runs straight away.
	m = newHomeModel(o, 0, "")
	m.Update(tea.PasteMsg{Content: "https://open.spotify.com/album/79dL7FLiJFOO0EoehUHQBv"})
	homeKey(m, "enter")
	if m.action != "input" || len(m.inputs) != 1 {
		t.Fatalf("paste+enter: action %q inputs %q", m.action, m.inputs)
	}

	// "Download links" points at the box instead of opening anything.
	m = newHomeModel(o, 1, "")
	homeKey(m, "enter")
	if m.action != "" || m.note == "" {
		t.Fatalf("Download links: action %q note %q", m.action, m.note)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q", n, got)
		}
	}
}
