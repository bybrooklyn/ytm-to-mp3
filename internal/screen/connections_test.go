package screen

import "testing"

func testConns() *Connections {
	return NewConnections([]Connection{
		{ID: "yt", Name: "YouTube Music library", Status: "not set up", Action: "youtube"},
		{ID: "apple", Name: "Apple Music", Status: "signed in", Good: true, Action: "appleout"},
		{ID: "bc", Name: "Bandcamp", Status: "ready", Dim: true},
	}, 0)
}

func TestConnectionsNavigateAndAct(t *testing.T) {
	c := testConns()
	c.Key("down")
	c.Key("enter")
	if c.Action() != "appleout" || !c.Finished() {
		t.Fatalf("action=%q", c.Action())
	}
	c.ClearAction()
	if c.Finished() {
		t.Fatal("clearing the action reopens the screen")
	}
	c.SelectIndex(2)
	c.Key("enter")
	if c.Action() != "" || c.Finished() {
		t.Fatal("a row with nothing to do does nothing")
	}
	c.Key("up")
	c.Key("up")
	if c.Cursor() != 0 { // wraps: 2 -> 1 -> 0
		t.Fatalf("cursor=%d", c.Cursor())
	}
	c.Key("up")
	if c.Cursor() != 2 {
		t.Fatal("up wraps to the end")
	}
	c.Key("esc")
	if !c.Finished() {
		t.Fatal("esc leaves")
	}
}

func TestConnectionsScreenMarksState(t *testing.T) {
	sc := testConns().Screen()
	if len(sc.Rows) != 3 || !sc.Rows[0].Selected || !sc.Rows[1].Good || !sc.Rows[2].Dim {
		t.Fatalf("%+v", sc.Rows)
	}
	if sc.Title != "connections" {
		t.Fatal(sc.Title)
	}
}

func TestDetectConnectionsListsEverySource(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	got := map[string]bool{}
	for _, c := range DetectConnections() {
		got[c.ID] = true
	}
	for _, id := range []string{"youtube", "apple", "vpn", "premium", "spotify", "soundcloud", "bandcamp"} {
		if !got[id] {
			t.Errorf("missing %s", id)
		}
	}
}
