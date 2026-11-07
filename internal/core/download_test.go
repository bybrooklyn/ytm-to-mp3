package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// recSink records every event, in order.
type recSink struct {
	mu sync.Mutex
	ev []jobs.Event
}

func (r *recSink) Emit(e jobs.Event) { r.mu.Lock(); r.ev = append(r.ev, e); r.mu.Unlock() }

func (r *recSink) events() []jobs.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]jobs.Event(nil), r.ev...)
}

func (r *recSink) kinds(id string) []jobs.Kind {
	var out []jobs.Kind
	for _, e := range r.events() {
		if e.ID == id {
			out = append(out, e.Kind)
		}
	}
	return out
}

// fakePrompter answers from a script and remembers what it was asked.
type fakePrompter struct {
	mu      sync.Mutex
	confirm []bool // answers, in order; then the default
	asked   []Question
	notices []string
}

func (p *fakePrompter) Confirm(_ context.Context, q Question) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, q)
	if len(p.confirm) == 0 {
		return q.Default, nil
	}
	a := p.confirm[0]
	p.confirm = p.confirm[1:]
	return a, nil
}
func (p *fakePrompter) Select(_ context.Context, _, _ string, _ []Option, def string) (string, error) {
	return def, nil
}
func (p *fakePrompter) Input(_ context.Context, _, _, def string) (string, error) { return def, nil }
func (p *fakePrompter) Notice(title, _ string) {
	p.mu.Lock()
	p.notices = append(p.notices, title)
	p.mu.Unlock()
}
func (p *fakePrompter) Working(ctx context.Context, _ string, f func(context.Context) error) error {
	return f(ctx)
}

// fakeYtDlp and fakeFFmpeg stand in for the real tools: every id resolves and downloads, but
// FAKE_GONE=<id> is unavailable, and FAKE_SLEEP delays each call.
const fakeYtDlp = `#!/bin/sh
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
mode=resolve; tmpl=""; ids=""
prev=""
for a in "$@"; do
  case "$a" in
    https://www.youtube.com/watch?v=*) ids="$ids ${a#*v=}" ;;
    --print) mode=download ;;
  esac
  [ "$prev" = "-o" ] && tmpl="$a"
  prev="$a"
done
rc=0
for id in $ids; do
  if [ "$id" = "$FAKE_GONE" ]; then
    echo "ERROR: [youtube] $id: Video unavailable" >&2; rc=1; continue
  fi
  if [ $mode = download ]; then
    f=$(echo "$tmpl" | sed 's/%(ext)s/m4a/; s/%%/%/g')
    echo "ytmprog 1000 4000"; sleep 0.3; echo "ytmprog 4000 4000"
    echo data > "$f"; echo "$f"
  else
    echo "{\"id\":\"$id\",\"url\":\"https://example.invalid/$id\",\"protocol\":\"m3u8\",\"ext\":\"m4a\",\"acodec\":\"mp4a\",\"title\":\"Song $id\"}"
  fi
done
exit $rc
`

const fakeFFmpeg = `#!/bin/sh
for a; do last="$a"; done
echo audio > "${last#file:}"
`

// sandboxRun isolates settings and swaps the tools for fakes; it returns the music folder.
func sandboxRun(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake yt-dlp and ffmpeg are shell scripts (covered on Linux and macOS)")
	}
	sandboxConfig(t)
	t.Setenv("MFY_RESOLVER", "yt-dlp")
	cfg := config.Load()
	cfg.NoFolderJpg, cfg.NoNFO, cfg.CoverPNG = true, true, false
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	yt, ff := filepath.Join(dir, "yt-dlp"), filepath.Join(dir, "ffmpeg")
	for p, body := range map[string]string{yt: fakeYtDlp, ff: fakeFFmpeg} {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := ensureTools
	ensureTools = func(context.Context, deps.Logger) (deps.Tools, error) {
		return deps.Tools{YtDlp: yt, FFmpeg: ff}, nil
	}
	t.Cleanup(func() { ensureTools = old })
	return filepath.Join(dir, "music")
}

func testOptions(out string) Options {
	return Options{Out: out, Format: "m4a", NoVPN: true, Retry: false, Albums: false, AlbumsMin: 1}
}

const (
	vidA = "aaaaaaaaaaa"
	vidB = "bbbbbbbbbbb"
	vidC = "ccccccccccc"
)

// Mixed, repeated and junk inputs become one clean run; every song's events arrive in order
// and the result and the terminal text agree with them.
func TestDownloadReportsEveryEventInOrder(t *testing.T) {
	out := sandboxRun(t)
	t.Setenv("FAKE_GONE", vidB)
	rec := &recSink{}
	var text strings.Builder
	input := "https://youtu.be/" + vidA + "\nnot a link at all\nhttps://youtu.be/" + vidB +
		"\nhttps://music.youtube.com/watch?v=" + vidA + "\nhttps://youtu.be/" + vidC
	res, err := Download(context.Background(), []string{input}, testOptions(out),
		Deps{UI: ui.NewWriter(&text), Sink: rec, Prompter: AutoPrompter{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Done != 2 || res.Counts.Failed != 1 || res.Rounds != 1 {
		t.Fatalf("counts %+v rounds %d", res.Counts, res.Rounds)
	}
	for _, id := range []string{vidA, vidC} {
		got := rec.kinds(id)
		want := []jobs.Kind{jobs.Queued, jobs.Started, jobs.Stage, jobs.Stage, jobs.Progress, jobs.Stage, jobs.Done}
		if !subsequence(got, want, true) {
			t.Errorf("%s: events %v, want %v in order", id, got, want)
		}
	}
	bad := rec.kinds(vidB)
	if bad[0] != jobs.Queued || bad[len(bad)-1] != jobs.Failed {
		t.Errorf("unavailable song: %v", bad)
	}
	// the same link given twice was one song, and the junk line was named, not fatal
	n := 0
	for _, e := range rec.events() {
		if e.ID == vidA && e.Kind == jobs.Queued {
			n++
		}
	}
	if n != 1 {
		t.Errorf("song queued %d times, want once", n)
	}
	if !strings.Contains(text.String(), "duplicate") {
		t.Errorf("the dropped duplicate should be said in the text output:\n%s", text.String())
	}
	var failed jobs.Result
	for _, r := range res.Results {
		if r.Kind == jobs.Failed {
			failed = r
		}
	}
	if failed.ID != vidB || failed.Reason == "" {
		t.Errorf("failed result %+v", failed)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("music folder missing: %v", err)
	}
}

// subsequence reports whether want occurs in got in order (each at most once, gaps allowed
// for kinds that repeat when loose is false).
func subsequence(got, want []jobs.Kind, loose bool) bool {
	i := 0
	for _, k := range got {
		if i < len(want) && k == want[i] {
			i++
		} else if i > 0 && k == want[i-1] && loose {
			continue // a repeated stage or progress event
		}
	}
	return i == len(want)
}

// When songs fail for a reason that may pass, the person is asked through the Prompter,
// and says no (one round) or yes (the failed ones run again).
func TestDownloadRetryOfferGoesThroughThePrompter(t *testing.T) {
	sandboxConfig(t)
	rounds := 0
	old := downloadOnceFn
	downloadOnceFn = func(_ context.Context, _ *ui.UI, _ opts, led *jobs.Ledger, _ jobs.Sink, _ []string) error {
		rounds++
		led.Emit(jobs.Event{Kind: jobs.Done, ID: "ok", Label: "Fine"})
		if rounds < 3 {
			led.Emit(jobs.Event{Kind: jobs.Failed, ID: "bad", Label: "Flaky", Reason: "connection reset", Class: jobs.ClassNetwork, Retryable: true})
		} else {
			led.Emit(jobs.Event{Kind: jobs.Done, ID: "bad", Label: "Flaky"})
		}
		return nil
	}
	t.Cleanup(func() { downloadOnceFn = old })

	no := &fakePrompter{confirm: []bool{false}}
	res, err := Download(context.Background(), nil, Options{}, Deps{Prompter: no})
	if err != nil || rounds != 1 || res.Counts.Retryable != 1 {
		t.Fatalf("declined: err=%v rounds=%d counts=%+v", err, rounds, res.Counts)
	}
	if len(no.asked) != 1 || !strings.Contains(no.asked[0].Title, "1 song failed") || no.asked[0].Default {
		t.Errorf("question: %+v", no.asked)
	}

	rounds = 0
	yes := &fakePrompter{confirm: []bool{true, true}}
	res, err = Download(context.Background(), nil, Options{}, Deps{Prompter: yes})
	if err != nil || rounds != 3 || res.Counts.Failed != 0 || res.Rounds != 3 {
		t.Fatalf("accepted: err=%v rounds=%d rounds(res)=%d counts=%+v", err, rounds, res.Rounds, res.Counts)
	}

	rounds = 0 // nobody to ask: the default is no
	if _, err = Download(context.Background(), nil, Options{}, Deps{}); err != nil || rounds != 1 {
		t.Fatalf("unattended: err=%v rounds=%d", err, rounds)
	}
}

// A run stopped through its context returns promptly with the context's error, keeps what
// arrived and leaves no work files behind.
func TestDownloadCancel(t *testing.T) {
	out := sandboxRun(t)
	t.Setenv("FAKE_SLEEP", "0.4")
	rec := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 400; i++ {
			for _, e := range rec.events() {
				if e.Kind == jobs.Started {
					cancel()
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	var ids []string
	for i := 0; i < 30; i++ {
		ids = append(ids, "https://youtu.be/"+strings.Repeat(string(rune('a'+i%26)), 10)+string(rune('A'+i%26)))
	}
	done := make(chan struct{})
	var res Result
	var err error
	go func() {
		res, err = Download(ctx, ids, testOptions(out), Deps{Sink: rec})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Download did not return after cancel")
	}
	if !errors.Is(err, context.Canceled) || !res.Stopped {
		t.Fatalf("err=%v stopped=%v", err, res.Stopped)
	}
	if res.Counts.Done >= len(ids) {
		t.Errorf("everything finished; the cancel did nothing: %+v", res.Counts)
	}
	left, _ := filepath.Glob(filepath.Join(out, ".mfetchy", "partial", "*"))
	for _, p := range left {
		if st, e := os.Stat(p); e == nil && st.IsDir() {
			if inner, _ := os.ReadDir(p); len(inner) > 0 {
				t.Errorf("partial work left in %s", p)
			}
		}
	}
}

// The steps that follow a web download run under the same settings as for other sources.
func TestAfterFilesFollowsSettings(t *testing.T) {
	sandboxConfig(t)
	cfg := config.Load()
	cfg.NoFolderJpg, cfg.NoNFO, cfg.CoverPNG = true, true, false
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var got [][]string
	testPostHook = func(files []string) error { got = append(got, files); return nil }
	t.Cleanup(func() { testPostHook = nil })
	ctx := context.Background()
	u := ui.NewWriter(&strings.Builder{})

	if err := afterFiles(ctx, u, opts{out: t.TempDir()}, []string{"/m/a.opus"}); err != nil || len(got) != 0 {
		t.Fatalf("with every step off nothing should run: %v %v", err, got)
	}
	if err := afterFiles(ctx, u, opts{out: t.TempDir(), lyricsAfter: true}, []string{"/m/a.opus", "/m/b.opus"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("lyrics on: the steps should see both files once: %v", got)
	}
	if err := afterFiles(ctx, u, opts{lyricsAfter: true}, nil); err != nil || len(got) != 1 {
		t.Fatalf("no files, nothing to do: %v %v", err, got)
	}
}

func TestTerminalPrompterAnswersTheDefaultWhenNotInteractive(t *testing.T) {
	p := TerminalPrompter{U: ui.NewWriter(&strings.Builder{})}
	if ok, _ := p.Confirm(context.Background(), Question{Title: "x", Default: true}); !ok {
		t.Error("default true should hold without a terminal")
	}
	if v, _ := p.Input(context.Background(), "t", "", "dflt"); v != "dflt" {
		t.Errorf("input default: %q", v)
	}
}
