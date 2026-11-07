package lyrics

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
)

// workingMedia finds an ffmpeg/ffprobe pair that really works (a broken one can shadow the
// good one on PATH), or skips the test.
func workingMedia(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	for _, dir := range []string{"", "/usr/bin", "/usr/local/bin", "/opt/homebrew/bin"} {
		ff, fp := "ffmpeg", "ffprobe"
		if dir != "" {
			ff, fp = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "ffprobe")
		}
		lf, err1 := exec.LookPath(ff)
		lp, err2 := exec.LookPath(fp)
		if err1 != nil || err2 != nil {
			continue
		}
		probe := filepath.Join(t.TempDir(), "probe.m4a")
		if exec.Command(lf, "-v", "error", "-f", "lavfi", "-i", "sine=d=1", "-y", probe).Run() == nil &&
			exec.Command(lp, "-v", "error", probe).Run() == nil {
			return lf, lp
		}
	}
	t.Skip("no working ffmpeg/ffprobe")
	return "", ""
}

func TestProcessWritesSidecarAndTagForEveryContainer(t *testing.T) {
	ffmpeg, ffprobe := workingMedia(t)
	lines, _ := ParseLRC(bloomLRC)
	_ = lines
	for _, ext := range []string{"mp3", "m4a", "opus", "flac"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "01 - Bloom."+ext)
			meta := []string{"-metadata", "title=Bloom", "-metadata", "artist=Radiohead", "-metadata", "album=The King of Limbs"}
			args := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=d=215"}, meta...)
			if ext == "opus" {
				args = append(args, "-c:a", "libopus")
			}
			if out, err := exec.Command(ffmpeg, append(args, "-y", path)...).CombinedOutput(); err != nil {
				t.Skipf("cannot make a %s: %v %s", ext, err, out)
			}
			c := syncedCand("LRCLIB")
			p := &fakeProvider{name: "LRCLIB", cands: []Candidate{c}}
			svc := &Service{Fetcher: &Fetcher{Providers: []Provider{p}, Cache: &Cache{Dir: CacheDir(dir)}}, FFmpeg: ffmpeg, FFprobe: ffprobe}

			out, err := svc.Process(context.Background(), path)
			if err != nil || out.Kind != KindSynced || out.Cached {
				t.Fatalf("%+v %v", out, err)
			}
			side, err := os.ReadFile(filepath.Join(dir, "01 - Bloom.lrc"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"[ti:Bloom]", "[ar:Radiohead]", "[al:The King of Limbs]", "[length:03:35]", "[by:ytm · LRCLIB]", "[00:12.34]Open your mouth wide"} {
				if !strings.Contains(string(side), want) {
					t.Errorf("sidecar lacks %q:\n%s", want, side)
				}
			}
			info, err := tag.Probe(context.Background(), ffprobe, path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Tags["lyrics"]; got != strings.TrimRight(string(side), "\n") {
				t.Errorf("embedded lyrics differ from the sidecar:\n%q\n%q", got, side)
			}
			if info.Tags["title"] != "Bloom" {
				t.Errorf("other tags survive: %v", info.Tags)
			}

			// a second run changes nothing and asks nobody
			if out, err = svc.Process(context.Background(), path); err != nil || !out.Had || p.calls != 1 {
				t.Fatalf("%+v %v calls=%d", out, err, p.calls)
			}

			// an upgrade to a better source: a new file at a new path, same song. The cache has it.
			newPath := filepath.Join(dir, "Radiohead", "01 - Bloom."+ext)
			if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, newPath); err != nil {
				t.Fatal(err)
			}
			MoveSidecars(path, newPath)
			if _, err := os.Stat(filepath.Join(dir, "01 - Bloom.lrc")); err == nil {
				t.Error("old sidecar should have moved")
			}
			if out, err = svc.Process(context.Background(), newPath); err != nil || !out.Had {
				t.Fatalf("%+v %v", out, err)
			}
			// a fresh file (tags without lyrics) at another path is filled from the cache, no request
			fresh := filepath.Join(dir, "fresh."+ext)
			a2 := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=d=216"}, meta...)
			if ext == "opus" {
				a2 = append(a2, "-c:a", "libopus")
			}
			if out, err := exec.Command(ffmpeg, append(a2, "-y", fresh)...).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if out, err = svc.Process(context.Background(), fresh); err != nil || !out.Cached || out.Kind != KindSynced || p.calls != 1 {
				t.Fatalf("%+v %v calls=%d", out, err, p.calls)
			}
		})
	}
}

func TestProcessPlainAndNothingFound(t *testing.T) {
	ffmpeg, ffprobe := workingMedia(t)
	dir := t.TempDir()
	mk := func(name, title string) string {
		p := filepath.Join(dir, name)
		if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=d=100", "-metadata", "title="+title,
			"-metadata", "artist=Band", "-y", p).CombinedOutput(); err != nil {
			t.Skipf("%v %s", err, out)
		}
		return p
	}
	pl := cand("YT", 0, "Plain Song", "Band", 100*time.Second)
	pl.Plain = plain(8)
	svc := &Service{Fetcher: &Fetcher{Providers: []Provider{&fakeProvider{name: "YT", cands: []Candidate{pl}}}}, FFmpeg: ffmpeg, FFprobe: ffprobe}

	p1 := mk("plain.m4a", "Plain Song")
	out, err := svc.Process(context.Background(), p1)
	if err != nil || out.Kind != KindPlain {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plain.txt")); err != nil {
		t.Error("plain lyrics go to a .txt sidecar")
	}
	if _, err := os.Stat(filepath.Join(dir, "plain.lrc")); err == nil {
		t.Error("no .lrc for plain lyrics")
	}

	p2 := mk("none.m4a", "Unknown Tune")
	out, err = svc.Process(context.Background(), p2)
	if err != nil || out.Kind != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "none.*")); len(m) != 1 {
		t.Errorf("nothing found writes nothing: %v", m)
	}
}

func TestProcessKeepsLyricsAlreadyInTheFile(t *testing.T) {
	ffmpeg, ffprobe := workingMedia(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "own.m4a")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=d=100", "-metadata", "title=Bloom",
		"-metadata", "artist=Radiohead", "-metadata", "lyrics=my own words\nsecond line", "-y", p).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	prov := &fakeProvider{name: "LRCLIB", cands: []Candidate{syncedCand("LRCLIB")}}
	svc := &Service{Fetcher: &Fetcher{Providers: []Provider{prov}}, FFmpeg: ffmpeg, FFprobe: ffprobe}
	out, err := svc.Process(context.Background(), p)
	if err != nil || !out.Had || prov.calls != 0 {
		t.Fatalf("%+v %v calls=%d", out, err, prov.calls)
	}
	b, err := os.ReadFile(filepath.Join(dir, "own.txt"))
	if err != nil || !strings.HasPrefix(string(b), "my own words") {
		t.Fatalf("%q %v", b, err)
	}
}

func TestMoveSidecarsKeepsExistingDestination(t *testing.T) {
	dir := t.TempDir()
	must := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "a.mp3"), "")
	must(filepath.Join(dir, "a.lrc"), "old")
	must(filepath.Join(dir, "b.lrc"), "keep")
	MoveSidecars(filepath.Join(dir, "a.mp3"), filepath.Join(dir, "b.opus"))
	if b, _ := os.ReadFile(filepath.Join(dir, "b.lrc")); string(b) != "keep" {
		t.Errorf("%q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.lrc")); err == nil {
		t.Error("source sidecar should be gone")
	}
}
