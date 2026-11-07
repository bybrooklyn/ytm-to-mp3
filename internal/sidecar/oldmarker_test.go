package sidecar

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// Files written before the rename carry "written by ytm"; they must still count as ours
// (so they are updated), while a file without either marker stays the user's.
func TestOldMarkerStillOwned(t *testing.T) {
	dir := t.TempDir()
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	newJPG, err := markJPEG(jb.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	oldJPG := bytes.Replace(newJPG, []byte(Marker), []byte(OldMarker), 1)
	oldJPG[5] = byte(len(OldMarker) + 2) // segment length follows the shorter marker
	pngNew, err := jpegToPNG(jb.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"new.jpg":     newJPG,
		"old.jpg":     oldJPG,
		"new.png":     pngNew,
		"old.nfo":     []byte("<?xml version=\"1.0\"?>\n<!-- " + OldMarker + " -->\n<album/>"),
		"new.nfo":     []byte("<?xml version=\"1.0\"?>\n<!-- " + Marker + " -->\n<album/>"),
		"user.nfo":    []byte("<?xml version=\"1.0\"?>\n<album/>"),
		"user.jpg":    jb.Bytes(),
		"oldlike.nfo": []byte("<!-- written by somebody else -->"),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		want := !bytes.HasPrefix([]byte(name), []byte("user")) && name != "oldlike.nfo"
		if got := owned(p); got != want {
			t.Errorf("owned(%s) = %v, want %v", name, got, want)
		}
	}
	// a PNG written by the old version
	oldPNG := bytes.Replace(pngNew, []byte(Marker), []byte(OldMarker), 1)
	p := filepath.Join(dir, "old.png")
	_ = os.WriteFile(p, oldPNG, 0o644)
	if !owned(p) {
		t.Error("old png marker not recognised")
	}
}
