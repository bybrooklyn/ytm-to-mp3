package screen

import (
	"errors"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
)

func press(m *Settings, keys ...string) {
	named := map[string]bool{"up": true, "down": true, "left": true, "right": true, "enter": true, "esc": true, "backspace": true}
	for _, k := range keys {
		if !named[k] && m.Editing() {
			m.Type(k)
		} else {
			m.Key(k)
		}
	}
}

func testModel(signedIn bool) (*Settings, *config.Config, *int) {
	var last config.Config
	n := 0
	m := NewSettings(config.Config{}, signedIn, "me", 0,
		func(c config.Config) error { last = c; n++; return nil },
		func(s string) error {
			if s == "bad" {
				return errors.New("nope")
			}
			return nil
		})
	return m, &last, &n
}

func TestFormatCyclesAndSaves(t *testing.T) {
	m, last, n := testModel(false)
	press(m, "down", "right")
	if last.Format != "flac" || *n != 1 || !m.saved { // default opus, one step right
		t.Fatalf("format=%q saves=%d saved=%v", last.Format, *n, m.saved)
	}
	press(m, "right")
	if last.Format != "mp3" {
		t.Fatalf("wrap: %q", last.Format)
	}
}

func TestVPNDisabledWhenSignedOut(t *testing.T) {
	m, _, n := testModel(false)
	m.cur = int(rowVPN)
	press(m, "right")
	if *n != 0 || m.err == "" {
		t.Fatal("toggle should be refused with a hint")
	}
	m2, last, _ := testModel(true)
	m2.cur = int(rowVPN)
	press(m2, "right")
	if !last.NoVPN {
		t.Fatal("expected VPN off")
	}
	press(m2, "right")
	if last.NoVPN {
		t.Fatal("expected VPN on")
	}
}

func TestClamping(t *testing.T) {
	m, last, _ := testModel(true)
	m.cur = int(rowConns)
	for i := 0; i < 20; i++ {
		press(m, "right")
	}
	if last.Connections != MaxConns {
		t.Fatalf("conns=%d", last.Connections)
	}
	for i := 0; i < 20; i++ {
		press(m, "left")
	}
	if last.Connections != -1 { // -1 = auto (plan limit minus a spare), below 1
		t.Fatalf("conns=%d", last.Connections)
	}
	press(m, "right")
	if last.Connections != 1 { // 0 ("unset") is skipped
		t.Fatalf("conns=%d", last.Connections)
	}
	m.cur = int(rowJobs)
	press(m, "left")
	if last.Jobs != 0 {
		t.Fatalf("jobs=%d", last.Jobs)
	}
	press(m, "right", "right")
	if last.Jobs != 2 {
		t.Fatalf("jobs=%d", last.Jobs)
	}
	m.cfg.Jobs = MaxJobs
	press(m, "right")
	if last.Jobs != MaxJobs {
		t.Fatalf("jobs=%d", last.Jobs)
	}
}

// The extras are on until switched off, and each row toggles exactly its own setting.
func TestExtrasDefaultOnAndToggle(t *testing.T) {
	m, last, _ := testModel(true)
	for _, r := range []rowKind{rowAlbums, rowRetry, rowTagAfter, rowPremiumAfter} {
		if v, _, _ := m.value(r); v != "on" {
			t.Errorf("%v should start on, is %q", r, v)
		}
	}
	m.cur = int(rowTagAfter)
	press(m, "right")
	if !last.NoTagAfter || last.NoAlbums || last.NoRetry || last.NoPremiumAfter {
		t.Fatalf("wrong setting toggled: %+v", *last)
	}
	press(m, "left")
	if last.NoTagAfter {
		t.Fatal("toggle back on")
	}
}

func TestCountriesCycle(t *testing.T) {
	m, last, _ := testModel(true)
	m.cur = int(rowCountries)
	press(m, "right")
	if last.Countries != countryChoices[1] {
		t.Fatalf("got %q", last.Countries)
	}
	press(m, "left", "left")
	if last.Countries != countryChoices[len(countryChoices)-1] {
		t.Fatalf("got %q", last.Countries)
	}
}

func TestNavigationWraps(t *testing.T) {
	m, _, _ := testModel(true)
	press(m, "up")
	if m.cur != len(settingsRows)-1 {
		t.Fatalf("cur=%d", m.cur)
	}
	press(m, "down")
	if m.cur != 0 {
		t.Fatalf("cur=%d", m.cur)
	}
}

func TestFolderEdit(t *testing.T) {
	m, last, n := testModel(false)
	press(m, "enter")
	if !m.editing {
		t.Fatal("should be editing")
	}
	m.input = ""
	press(m, "b", "a", "d", "enter")
	if !m.editing || m.err != "nope" || *n != 0 {
		t.Fatalf("invalid folder must stay in edit: editing=%v err=%q", m.editing, m.err)
	}
	press(m, "backspace", "backspace", "backspace", "/", "t", "m", "p", "enter")
	if m.editing || last.Out != config.ExpandPath("/tmp") || *n != 1 {
		t.Fatalf("editing=%v out=%q", m.editing, last.Out)
	}
	press(m, "enter", "x", "esc")
	if m.editing || last.Out != config.ExpandPath("/tmp") {
		t.Fatal("esc must cancel")
	}
}

func TestActionsAndQuit(t *testing.T) {
	m, _, _ := testModel(false)
	m.cur = int(rowAccount)
	press(m, "enter")
	if m.action != "signin" {
		t.Fatalf("action=%q", m.action)
	}
	m2, _, _ := testModel(true)
	m2.cur = int(rowAccount)
	press(m2, "enter")
	if m2.action != "signout" {
		t.Fatalf("action=%q", m2.action)
	}
	m3, _, _ := testModel(false)
	m3.cur = int(rowTest)
	press(m3, "enter")
	if m3.action != "" || m3.err == "" {
		t.Fatal("test row must refuse when signed out")
	}
	m5, _, _ := testModel(false)
	m5.cur = int(rowPremium)
	press(m5, "enter")
	if m5.action != "premium" {
		t.Fatalf("premium row: action=%q", m5.action)
	}
	m6, _, _ := testModel(false)
	m6.cur = int(rowApple)
	press(m6, "enter")
	if m6.action != "applein" {
		t.Fatalf("apple row signed out: action=%q", m6.action)
	}
	m7, _, _ := testModel(false)
	m7.appleIn = true
	m7.cur = int(rowApple)
	press(m7, "enter")
	if m7.action != "appleout" {
		t.Fatalf("apple row signed in: action=%q", m7.action)
	}
	m4, _, _ := testModel(true)
	press(m4, "esc")
	if !m4.done {
		t.Fatal("esc leaves")
	}
}

func TestRenderMentionsEverything(t *testing.T) {
	m, _, _ := testModel(true)
	s := m.Render()
	for _, r := range settingsRows {
		if !contains(s, r.label) {
			t.Errorf("missing %q", r.label)
		}
	}
	if !contains(s, "esc done") {
		t.Error("missing key hints")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestLyricsSettingsRows(t *testing.T) {
	m, last, _ := testModel(true)
	for r, want := range map[rowKind]string{rowLyricsAfter: "on", rowLyricsLRCLIB: "on", rowLyricsNetEase: "off"} {
		if v, _, _ := m.value(r); v != want {
			t.Errorf("%v starts %q, want %q", r, v, want)
		}
	}
	m.cur = int(rowLyricsLRCLIB)
	press(m, "right")
	if !last.NoLyricsLRCLIB || last.NoLyricsAfter || last.LyricsNetEase {
		t.Fatalf("wrong setting toggled: %+v", *last)
	}
	m.cur = int(rowLyricsNetEase)
	press(m, "right")
	if !last.LyricsNetEase {
		t.Fatal("NetEase should switch on")
	}
}

func TestSidecarSettingsRows(t *testing.T) {
	m, last, _ := testModel(true)
	for r, want := range map[rowKind]string{rowFolderJpg: "on", rowNFO: "on", rowCoverPNG: "off"} {
		if v, _, _ := m.value(r); v != want {
			t.Errorf("%v starts %q, want %q", r, v, want)
		}
	}
	if rowFolderJpg != rowTidalQuality+1 || rowCoverPNG+1 != rowPrivate {
		t.Fatal("the new rows sit after the Tidal rows and before Private mode")
	}
	m.cur = int(rowNFO)
	press(m, "right")
	if !last.NoNFO || last.NoFolderJpg || last.CoverPNG {
		t.Fatalf("wrong setting toggled: %+v", *last)
	}
	m.cur = int(rowCoverPNG)
	press(m, "right")
	if !last.CoverPNG {
		t.Fatal("cover.png should switch on")
	}
	m.cur = int(rowFolderJpg)
	press(m, "right")
	if !last.NoFolderJpg {
		t.Fatal("folder.jpg should switch off")
	}
}

func TestPrivateModeRowToggles(t *testing.T) {
	m, last, n := testModel(false)
	m.cur = int(rowPrivate)
	press(m, "right")
	if !last.Private || *n != 1 {
		t.Fatalf("private not saved: %+v", last)
	}
	press(m, "right")
	if last.Private {
		t.Fatal("private did not toggle back")
	}
}
