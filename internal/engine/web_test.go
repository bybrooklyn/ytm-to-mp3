package engine

import (
	"encoding/json"
	"testing"
)

func TestWebSite(t *testing.T) {
	for link, want := range map[string]string{
		"https://soundcloud.com/forss/flickermood":      "soundcloud",
		"https://m.soundcloud.com/forss":                "soundcloud",
		"https://artist.bandcamp.com/album/x":           "bandcamp",
		"https://bandcamp.com/":                         "bandcamp",
		"https://evilbandcamp.com/album/x":              "",
		"https://soundcloud.com.evil.example/x":         "",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":   "",
		"ftp://soundcloud.com/x":                        "",
		"not a url":                                     "",
		"https://notsoundcloud.com/x":                   "",
		"https://artist.bandcamp.com.attacker.io/album": "",
	} {
		if got := WebSite(link); got != want {
			t.Errorf("WebSite(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestFlattenAlbumNumbersAndCover(t *testing.T) {
	const j = `{"title":"Album","entries":[
	 {"id":"1","title":"A - One","track":"One","artist":"A","album":"Rec","track_number":1,"webpage_url":"https://a.bandcamp.com/track/one","release_date":"20190531",
	  "thumbnails":[{"url":"https://f4.bcbits.com/img/a1_5.jpg","width":700,"height":700}]},
	 {"id":"2","title":"A - Two","track":"Two","artist":"A","album":"Rec","track_number":2,"webpage_url":"https://a.bandcamp.com/track/two"},
	 {"id":"3","title":"Single","uploader":"U","webpage_url":"https://soundcloud.com/u/single"}]}`
	var root webInfo
	if err := json.Unmarshal([]byte(j), &root); err != nil {
		t.Fatal(err)
	}
	var out []WebTrack
	flattenWeb(root, "bandcamp", "", &out)
	if len(out) != 3 {
		t.Fatalf("got %d", len(out))
	}
	if out[0].Title != "One" || out[0].Artist != "A" || out[0].AlbumArtist != "A" || out[0].Date != "2019-05-31" {
		t.Errorf("first: %+v", out[0])
	}
	if out[0].Cover != "https://f4.bcbits.com/img/a1_10.jpg" {
		t.Errorf("cover not bumped to the 1200 px original: %q", out[0].Cover)
	}
	if out[2].Artist != "U" || out[2].Album != "" {
		t.Errorf("a loose song takes the uploader as artist and has no album: %+v", out[2])
	}
}
