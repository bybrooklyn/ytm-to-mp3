package vpn

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
)

func srv(name, cc string, score float64, load int) proton.Server {
	return proton.Server{Name: name, Country: cc, City: cc + "-city", IP: "192.0.2.1",
		PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Score: score, Load: load, Status: 1}
}

func names(l []proton.Server) []string {
	var o []string
	for _, s := range l {
		o = append(o, s.Name)
	}
	return o
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{0, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		if got := Backoff(i); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i, got, w)
		}
	}
}

func TestCacheRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "servers.json")
	now := time.Now()
	c := LoadCache(path, "acct")
	c.SetServers([]proton.Server{srv("US#1", "US", 1, 10)}, 5, now)
	c.RecordBlock(srv("US#1", "US", 1, 10), "unavailable", now)
	c.RecordGood(srv("NL#1", "NL", 1, 10), 1000, now)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
	c2 := LoadCache(path, "acct")
	l, mc := c2.Servers()
	if len(l) != 1 || l[0].Name != "US#1" || mc != 5 {
		t.Fatalf("servers lost: %v %d", l, mc)
	}
	if age := c2.Age(now.Add(time.Minute)); age < time.Minute || age > 2*time.Minute {
		t.Fatalf("age %v", age)
	}
	if !c2.Blocked(srv("US#1", "US", 1, 10), now.Add(time.Minute)) {
		t.Fatal("block not persisted")
	}
	if c2.Blocked(srv("US#1", "US", 1, 10), now.Add(11*time.Minute)) {
		t.Fatal("block should have expired after 10m")
	}
	// another account must not see this one's list
	if l, _ := LoadCache(path, "other").Servers(); len(l) != 0 {
		t.Fatal("cache leaked across accounts")
	}
}

func TestCacheToleratesDamage(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"garbage":  "\x00\x01not json",
		"truncate": `{"version":1,"servers":[{"name":"a"`,
		"version":  `{"version":99,"servers":[{"name":"a","ip":"1.1.1.1","public_key":"k"}]}`,
		"empty":    ``,
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o600)
		c := LoadCache(p, "")
		if l, _ := c.Servers(); len(l) != 0 {
			t.Errorf("%s: loaded %v", name, l)
		}
		if c.Age(time.Now()) < MaxStale {
			t.Errorf("%s: should look ancient", name)
		}
		c.RecordBlock(srv("x", "US", 1, 1), "r", time.Now()) // still usable
		if err := c.Save(); err != nil {
			t.Errorf("%s: save: %v", name, err)
		}
	}
	// entries that can't be dialled are dropped
	p := filepath.Join(dir, "partial")
	os.WriteFile(p, []byte(`{"version":1,"servers":[{"name":"a"},{"name":"b","ip":"1.1.1.1","public_key":"k"}]}`), 0o600)
	if l, _ := LoadCache(p, "").Servers(); len(l) != 1 || l[0].Name != "b" {
		t.Fatalf("got %v", l)
	}
	// a missing directory is fine
	if c := LoadCache(filepath.Join(dir, "nope", "x.json"), ""); c == nil {
		t.Fatal("nil cache")
	}
	// in-memory cache saves nothing and never fails
	if err := LoadCache("", "").Save(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthBackoffAndDecay(t *testing.T) {
	c := LoadCache("", "")
	s := srv("US#1", "US", 1, 10)
	now := time.Now()
	c.RecordBlock(s, "429", now)
	if !c.Blocked(s, now.Add(9*time.Minute)) || c.Blocked(s, now.Add(11*time.Minute)) {
		t.Fatal("first block should last 10m")
	}
	c.RecordBlock(s, "429", now)
	if !c.Blocked(s, now.Add(29*time.Minute)) || c.Blocked(s, now.Add(31*time.Minute)) {
		t.Fatal("second block should last 30m")
	}
	c.RecordGood(s, 2<<20, now)
	if c.Blocked(s, now) {
		t.Fatal("success must clear the block")
	}
	h := c.f.Health["US#1"]
	if h.Strikes != 1 || h.Successes != 1 || h.BytesPerSec != 2<<20 {
		t.Fatalf("%+v", h)
	}
	c.RecordGood(s, 1<<20, now)
	if want := 2<<20*0.7 + 1<<20*0.3; h.BytesPerSec != want || h.Strikes != 0 {
		t.Fatalf("ewma/decay wrong: %+v want %v", h, want)
	}
	// strikes are forgotten with time
	c.RecordBlock(s, "x", now)
	c.RecordBlock(s, "x", now)
	if got := h.strikes(now.Add(25 * time.Hour)); got != 0 {
		t.Fatalf("strikes after a day: %d", got)
	}
}

func TestCountryBlockedAfterRepeatedBlocks(t *testing.T) {
	c := LoadCache("", "")
	now := time.Now()
	a, b, d, other := srv("US#1", "US", 1, 1), srv("US#2", "US", 1, 1), srv("US#3", "US", 1, 1), srv("NL#1", "NL", 1, 1)
	c.RecordBlock(a, "403", now)
	c.RecordBlock(b, "403", now)
	if c.Blocked(d, now) {
		t.Fatal("country blocked too early")
	}
	c.RecordBlock(srv("US#9", "US", 1, 1), "403", now)
	if !c.Blocked(d, now) {
		t.Fatal("whole country should be out after 3 blocks")
	}
	if c.Blocked(other, now) {
		t.Fatal("other country affected")
	}
	// a server that is merely unreachable says nothing about its country
	c2 := LoadCache("", "")
	for i := 0; i < 5; i++ {
		c2.RecordConnect(srv("DE#"+string(rune('a'+i)), "DE", 1, 1), false, 0, now)
	}
	if c2.Blocked(srv("DE#z", "DE", 1, 1), now) {
		t.Fatal("unreachable servers must not block the country")
	}
}

func TestRankRules(t *testing.T) {
	now := time.Now()
	c := LoadCache("", "")
	all := []proton.Server{
		srv("US#1", "US", 1.0, 10), srv("US#2", "US", 1.1, 80), srv("US#3", "US", 1.0, 5),
		srv("NL#1", "NL", 1.5, 20), srv("DE#1", "DE", 2.5, 30), srv("DE#2", "DE", 1.2, 90),
	}
	o := RankOpts{NoShuffle: true}
	got := names(c.Rank(all, o, now))
	// best of each country first (cheapest country first), then the second of each, ...
	want := []string{"US#3", "NL#1", "DE#1", "US#1", "DE#2", "US#2"}
	_ = want
	if got[0] != "US#3" {
		t.Fatalf("lowest load/score first, got %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got[:3] {
		seen[n[:2]] = true
	}
	if len(seen) != 3 {
		t.Fatalf("first three should be three countries: %v", got)
	}
	if len(got) != len(all) {
		t.Fatalf("lost servers: %v", got)
	}

	// used countries drop behind fresh ones
	got = names(c.Rank(all, RankOpts{NoShuffle: true, UsedCountries: map[string]int{"US": 1}}, now))
	if got[0] == "US#3" || got[0] == "US#1" {
		t.Fatalf("a used country should not lead: %v", got)
	}
	// moving away from a country
	got = names(c.Rank(all, RankOpts{NoShuffle: true, AvoidCountry: "us"}, now))
	if got[0][:2] == "US" {
		t.Fatalf("AvoidCountry ignored: %v", got)
	}
	// avoid + filter
	got = names(c.Rank(all, RankOpts{NoShuffle: true, Avoid: map[string]bool{"NL#1": true}, Countries: []string{"nl", "de"}}, now))
	if len(got) != 2 || got[0][:2] != "DE" {
		t.Fatalf("filter wrong: %v", got)
	}

	// blocked servers go last, soonest recovery first, but are still returned
	c.RecordBlock(all[2], "x", now) // US#3 10m
	c.RecordBlock(all[2], "x", now) // 30m
	c.RecordBlock(all[3], "x", now) // NL#1 10m
	got = names(c.Rank(all, RankOpts{NoShuffle: true}, now))
	if n := len(got); got[n-2] != "NL#1" || got[n-1] != "US#3" {
		t.Fatalf("blocked order wrong: %v", got)
	}
	for _, n := range got[:len(got)-2] {
		if n == "NL#1" || n == "US#3" {
			t.Fatalf("blocked server ranked ahead: %v", got)
		}
	}

	// a country that has worked is preferred over an equal one that has not
	c2 := LoadCache("", "")
	eq := []proton.Server{srv("AA#1", "AA", 1, 10), srv("BB#1", "BB", 1, 10)}
	c2.RecordGood(eq[1], 0, now)
	if got := names(c2.Rank(eq, RankOpts{NoShuffle: true}, now)); got[0] != "BB#1" {
		t.Fatalf("proven country should lead: %v", got)
	}
}

func TestSnapshotAndShuffleStayInTopThree(t *testing.T) {
	c := LoadCache("", "")
	var all []proton.Server
	for i := 0; i < 10; i++ {
		all = append(all, srv("US#"+string(rune('a'+i)), "US", float64(i), 10))
	}
	for i := 0; i < 50; i++ {
		first := c.Rank(all, RankOpts{}, time.Now())[0]
		if first.Score > 2 {
			t.Fatalf("shuffle left the top three: %v", first)
		}
	}
}

// With no measurements, a big well-connected country beats a tiny one even when the tiny
// country's server has a better Proton score and load.
func TestRankPrefersWellConnectedCountries(t *testing.T) {
	c := LoadCache("", "")
	all := []proton.Server{
		srv("ZW#1", "ZW", 0.5, 1), srv("LY#1", "LY", 0.6, 1), srv("US#9", "US", 1.4, 40), srv("DE#9", "DE", 1.6, 50),
	}
	got := names(c.Rank(all, RankOpts{NoShuffle: true}, time.Now()))
	if got[0][:2] != "US" && got[0][:2] != "DE" {
		t.Fatalf("expected a well-connected country first, got %v", got)
	}
	if got[1][:2] != "US" && got[1][:2] != "DE" {
		t.Fatalf("both well-connected countries should lead: %v", got)
	}
}
