package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

// fakeYtDlp is a shell script standing in for yt-dlp. Its behaviour follows the exit it
// runs through (http_proxy) and the environment:
//
//	FAKE_BLOCK=<substr>  exits whose proxy contains it answer "not a bot"
//	FAKE_GONE=<id>       that id is unavailable everywhere
//	FAKE_SLEEP=<secs>    per-invocation latency
//	FAKE_LOG=<file>      one line per invocation: "<proxy> <n ids>"
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
n=0; for i in $ids; do n=$((n+1)); done
[ -n "$FAKE_LOG" ] && echo "$http_proxy $n" >> "$FAKE_LOG"
rc=0
for id in $ids; do
  if [ -n "$FAKE_BLOCK" ] && case "$http_proxy" in *"$FAKE_BLOCK"*) true;; *) false;; esac; then
    echo "ERROR: [youtube] $id: Sign in to confirm you’re not a bot" >&2; rc=1; continue
  fi
  if [ "$id" = "$FAKE_GONE" ]; then
    echo "ERROR: [youtube] $id: Video unavailable" >&2; rc=1; continue
  fi
  if [ $mode = download ]; then
    f=$(echo "$tmpl" | sed 's/%(ext)s/m4a/; s/%%/%/g')
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

func tid(i int) string { return fmt.Sprintf("t%010d", i) }

func fakeOptions(t *testing.T, exits ...vpn.Exit) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	yt := filepath.Join(dir, "yt-dlp")
	ff := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(yt, []byte(fakeYtDlp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ff, []byte(fakeFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	old := laneGap
	laneGap = 5 * time.Millisecond
	t.Cleanup(func() { laneGap = old })
	return Options{Out: out, Format: "m4a", Tools: deps.Tools{YtDlp: yt, FFmpeg: ff}, Store: st,
		UI: ui.New(), Exits: exits}, dir
}

func tracks(n int) []model.Track {
	var ts []model.Track
	for i := 0; i < n; i++ {
		ts = append(ts, model.Track{ID: tid(i), Title: fmt.Sprintf("T%d", i)})
	}
	return ts
}

func exitN(name string) vpn.Exit { return vpn.Exit{Name: name, Proxy: "http://" + name + ".test:1"} }

func TestParseBatchMixed(t *testing.T) {
	ids := []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "ddddddddddd"}
	stdout := []byte(`{"id":"aaaaaaaaaaa","url":"https://x/a","protocol":"https","title":"A","filesize":10}
not json at all
{"id":"ccccccccccc","url":"https://x/c","title":"C"}
{"id":"zzzzzzzzzzz","url":"https://x/z"}
`)
	stderr := "ERROR: [youtube] bbbbbbbbbbb: Video unavailable. This video has been removed\n" +
		"ERROR: [youtube] ddddddddddd: Sign in to confirm you’re not a bot\n"
	m := parseBatch(ids, stdout, stderr)
	if len(m) != 4 {
		t.Fatalf("got %d results: %+v", len(m), m)
	}
	if m[ids[0]].res == nil || m[ids[0]].res.Title != "A" || m[ids[0]].res.Size != 10 {
		t.Errorf("a: %+v", m[ids[0]])
	}
	if kindOf(m[ids[1]].err) != kindPermanent {
		t.Errorf("b should be permanent: %+v", m[ids[1]])
	}
	if m[ids[2]].res == nil || m[ids[2]].res.Title != "C" {
		t.Errorf("c: %+v", m[ids[2]])
	}
	if kindOf(m[ids[3]].err) != kindThrottled {
		t.Errorf("d should be throttled: %+v", m[ids[3]])
	}
}

func TestParseBatchOddOutput(t *testing.T) {
	ids := []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"}
	// nothing attributable: caller must fall back to single resolves for all
	if m := parseBatch(ids, []byte("garbage"), "Traceback...\n"); len(m) != 0 {
		t.Fatalf("expected no results, got %+v", m)
	}
	// exactly one video unaccounted for + one unattributed error: they belong together
	m := parseBatch(ids,
		[]byte(`{"id":"aaaaaaaaaaa","url":"u"}`+"\n"+`{"id":"bbbbbbbbbbb","url":"u"}`+"\n"),
		"ERROR: Private video. Sign in\n")
	if len(m) != 3 || kindOf(m["ccccccccccc"].err) != kindPermanent {
		t.Fatalf("got %+v", m)
	}
}

func TestPickWeighsThroughput(t *testing.T) {
	e := testEngine(3)
	e.lanes[0].setSpeed(100)
	e.lanes[1].setSpeed(10)
	e.lanes[2].setSpeed(50)
	if got := e.pick(); got != e.lanes[0] {
		t.Fatalf("idle: picked %s, want the fastest (a)", got.exitName())
	}
	// load the fast lane to its limit: the next-fastest idle lane should win
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := e.lanes[0].gov.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.pick(); got != e.lanes[2] {
		t.Fatalf("loaded: picked %s, want c", got.exitName())
	}
	// unmeasured lanes are not penalised (they get explored)
	e.lanes[1].setSpeed(0)
	if got := e.pick(); got != e.lanes[1] && got != e.lanes[2] {
		t.Fatalf("picked %s", got.exitName())
	}
}

func TestLaneSampleIgnoresIdle(t *testing.T) {
	l := &lane{}
	now := time.Now()
	l.sample(now)
	l.bytes.Add(1000)
	l.fetching.Store(1)
	l.sample(now.Add(time.Second))
	r := l.speed()
	if r < 900 || r > 1100 {
		t.Fatalf("rate %v", r)
	}
	l.fetching.Store(0)
	l.sample(now.Add(2 * time.Second)) // idle window: estimate unchanged
	if l.speed() != r {
		t.Fatalf("idle window changed rate: %v", l.speed())
	}
}

func TestRunBatchesAndBlockRequeue(t *testing.T) {
	a, b := exitN("blockedA"), exitN("goodB")
	o, dir := fakeOptions(t, a, b)
	log := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_BLOCK", "blockedA")
	t.Setenv("FAKE_LOG", log)
	var mu sync.Mutex
	var blocked []string
	var good []string
	var swapped int
	o.OnBlocked = func(ex vpn.Exit, reason string) {
		mu.Lock()
		blocked = append(blocked, ex.Name+":"+reason)
		mu.Unlock()
	}
	o.OnGood = func(ex vpn.Exit, bps float64) { mu.Lock(); good = append(good, ex.Name); mu.Unlock() }
	o.Replace = func(ctx context.Context, old vpn.Exit) (vpn.Exit, error) {
		mu.Lock()
		swapped++
		mu.Unlock()
		return exitN("freshC"), nil
	}
	const n = 24
	sum := Run(context.Background(), o, tracks(n))
	if sum.Done != n || sum.Failed != 0 {
		t.Fatalf("done=%d failed=%d", sum.Done, sum.Failed)
	}
	if sum.Blocked < 1 || sum.Swaps < 1 {
		t.Fatalf("blocked=%d swaps=%d", sum.Blocked, sum.Swaps)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(blocked) == 0 || !strings.HasPrefix(blocked[0], "blockedA:bot check") {
		t.Fatalf("OnBlocked calls: %v", blocked)
	}
	if len(good) == 0 {
		t.Fatal("OnGood never called")
	}
	raw, _ := os.ReadFile(log)
	batched := false
	for _, l := range strings.Split(string(raw), "\n") {
		var p string
		var k int
		if _, err := fmt.Sscanf(l, "%s %d", &p, &k); err == nil && k > 1 {
			batched = true
		}
	}
	if !batched {
		t.Fatalf("no batched yt-dlp invocation seen:\n%s", raw)
	}
}

func TestBlockedTrackMovesWithoutSpendingBudget(t *testing.T) {
	// two of three exits are blocked; the track must still resolve, instantly (no retry
	// sleeps) and without any exhaustion of its budget.
	o, _ := fakeOptions(t, exitN("blockedA"), exitN("blockedB"), exitN("goodC"))
	t.Setenv("FAKE_BLOCK", "blocked")
	e := &engine{Options: o, pctx: context.Background(), startLimit: 2, laneMax: 4}
	for _, x := range o.Exits {
		if _, err := e.newLane(x); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	for i := 0; i < 10; i++ {
		_, _, ln, _, err := e.resolveWithRetry(context.Background(), model.Track{ID: tid(i)}, nil)
		if err != nil {
			t.Fatalf("track %d: %v", i, err)
		}
		if ln.exitName() != "goodC" {
			t.Fatalf("resolved on %s", ln.exitName())
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v: tracks are sleeping between exits", d)
	}
}

func TestUnavailableOnAllExitsStillFails(t *testing.T) {
	o, _ := fakeOptions(t, exitN("a"), exitN("b"), exitN("c"))
	t.Setenv("FAKE_GONE", tid(1))
	sum := Run(context.Background(), o, tracks(3))
	if sum.Done != 2 || sum.Failed != 1 {
		t.Fatalf("done=%d failed=%d", sum.Done, sum.Failed)
	}
}

func TestGrowingExitsRace(t *testing.T) {
	before := runtime.NumGoroutine()
	o, _ := fakeOptions(t, exitN("a"))
	t.Setenv("FAKE_SLEEP", "0.15")
	ch := make(chan vpn.Exit)
	o.NewExits = ch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for _, n := range []string{"b", "c", "d"} {
			select {
			case ch <- exitN(n):
			case <-ctx.Done():
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	const n = 40
	sum := Run(ctx, o, tracks(n))
	if sum.Done != n || sum.Failed != 0 {
		t.Fatalf("done=%d failed=%d", sum.Done, sum.Failed)
	}
	if len(sum.PerExit) < 2 {
		t.Fatalf("lanes never grew: %+v", sum.PerExit)
	}
	used := 0
	for _, x := range sum.PerExit {
		if x.Done > 0 {
			used++
		}
	}
	if used < 2 {
		t.Fatalf("new exits did no work: %+v", sum.PerExit)
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("goroutines leaked: %d -> %d", before, after)
	}
}

func TestRunWaitsForFirstExit(t *testing.T) {
	o, _ := fakeOptions(t)
	ch := make(chan vpn.Exit, 1)
	o.NewExits = ch
	go func() { time.Sleep(100 * time.Millisecond); ch <- exitN("late") }()
	sum := Run(context.Background(), o, tracks(5))
	if sum.Done != 5 {
		t.Fatalf("done=%d failed=%d", sum.Done, sum.Failed)
	}
}

func TestRunCancelIsClean(t *testing.T) {
	before := runtime.NumGoroutine()
	o, _ := fakeOptions(t, exitN("a"), exitN("b"))
	t.Setenv("FAKE_SLEEP", "0.2")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan Summary, 1)
	go func() { done <- Run(ctx, o, tracks(200)) }()
	select {
	case sum := <-done:
		if !sum.Interrupted {
			t.Fatal("not marked interrupted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	time.Sleep(300 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("goroutines leaked: %d -> %d", before, after)
	}
}
