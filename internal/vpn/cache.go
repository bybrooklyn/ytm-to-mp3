package vpn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
)

const cacheVersion = 1

const (
	// FreshTTL is how long a cached server list is used without asking Proton again.
	FreshTTL = 10 * time.Minute
	// MaxStale is the oldest cached list that is still used straight away (and refreshed
	// in the background). Older than this, startup waits for a fresh list, and falls back
	// to the old one only if the network fails.
	MaxStale = 24 * time.Hour
)

// backoffSteps is how long a server stays out of rotation after its 1st, 2nd, ... block.
var backoffSteps = []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Backoff is the exclusion time after the given number of consecutive blocks
// (10m, 30m, 2h, then capped at 6h). Zero strikes means no exclusion.
func Backoff(strikes int) time.Duration {
	if strikes <= 0 {
		return 0
	}
	return backoffSteps[min(strikes, len(backoffSteps))-1]
}

// strikeDecay is how long it takes for one strike to be forgotten without any news.
const strikeDecay = 12 * time.Hour

// Health is what we have learnt about one server (or one country) from using it.
type Health struct {
	Successes    int       `json:"successes"`
	Failures     int       `json:"failures"`
	Strikes      int       `json:"strikes"` // consecutive-ish blocks; drives BlockedUntil, falls on success
	LastBlock    time.Time `json:"last_block,omitzero"`
	LastReason   string    `json:"last_reason,omitempty"`
	BlockedUntil time.Time `json:"blocked_until,omitzero"`
	LastGood     time.Time `json:"last_good,omitzero"`
	HandshakeMs  float64   `json:"handshake_ms,omitempty"`  // EWMA of connect time
	BytesPerSec  float64   `json:"bytes_per_sec,omitempty"` // EWMA of observed throughput
}

func ewma(old, v float64) float64 {
	if old <= 0 {
		return v
	}
	return old*0.7 + v*0.3
}

func (h *Health) block(reason string, now time.Time) {
	h.Strikes++
	h.Failures++
	h.LastBlock, h.LastReason = now, reason
	h.BlockedUntil = now.Add(Backoff(h.Strikes))
}

func (h *Health) good(bps float64, now time.Time) {
	h.Successes++
	h.LastGood = now
	if h.Strikes > 0 {
		h.Strikes--
	}
	h.BlockedUntil = time.Time{} // it works right now, so it is not blocked
	if bps > 0 {
		h.BytesPerSec = ewma(h.BytesPerSec, bps)
	}
}

// strikes is the strike count after time-based forgetting.
func (h *Health) strikes(now time.Time) int {
	if h == nil || h.Strikes == 0 {
		return 0
	}
	if h.LastBlock.IsZero() {
		return h.Strikes
	}
	return max(0, h.Strikes-int(now.Sub(h.LastBlock)/strikeDecay))
}

func (h *Health) blocked(now time.Time) bool { return h != nil && now.Before(h.BlockedUntil) }

// countryBlockStrikes is how many strikes a whole country needs before it is excluded.
const countryBlockStrikes = 3

type cacheFile struct {
	Version    int                `json:"version"`
	Account    string             `json:"account,omitempty"`
	FetchedAt  time.Time          `json:"fetched_at,omitzero"`
	MaxConnect int                `json:"max_connect,omitempty"`
	Servers    []proton.Server    `json:"servers,omitempty"`
	Health     map[string]*Health `json:"health,omitempty"`
	Countries  map[string]*Health `json:"countries,omitempty"`
}

// Cache is the on-disk memory of Proton's server list and how each server has behaved.
// It is safe for concurrent use. A nil path keeps it in memory only.
type Cache struct {
	mu       sync.Mutex
	path     string
	f        cacheFile
	lastSave time.Time
}

// DefaultCachePath is ytm/servers.json inside the user cache directory.
func DefaultCachePath() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "ytm", "servers.json")
}

// AccountID is a short non-reversible tag for a username, so one account's server list
// (which depends on its plan) is never used for another.
func AccountID(username string) string {
	if username == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(username)))
	return hex.EncodeToString(sum[:6])
}

// LoadCache reads path. A missing, corrupt, wrong-version or other-account file just
// yields an empty cache (health is kept across account mismatch only if account matches).
func LoadCache(path, account string) *Cache {
	c := &Cache{path: path}
	c.f = cacheFile{Version: cacheVersion, Account: account}
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<20 {
		return c
	}
	var f cacheFile
	if json.Unmarshal(b, &f) != nil || f.Version != cacheVersion || f.Account != account {
		return c
	}
	if f.Health == nil {
		f.Health = map[string]*Health{}
	}
	if f.Countries == nil {
		f.Countries = map[string]*Health{}
	}
	// drop servers that can't be dialled (hand-edited or damaged entries)
	good := f.Servers[:0]
	for _, s := range f.Servers {
		if s.Name != "" && s.IP != "" && s.PublicKey != "" {
			good = append(good, s)
		}
	}
	f.Servers = good
	c.f = f
	return c
}

// Save writes the cache atomically with mode 0600.
func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Cache) saveLocked() error {
	if c.path == "" {
		return nil
	}
	c.f.Version = cacheVersion
	// forget health that no longer matters, so the file doesn't grow forever
	now := time.Now()
	for k, h := range c.f.Health {
		last := maxTime(h.LastGood, h.LastBlock)
		if h.Strikes == 0 && h.Successes == 0 && h.Failures == 0 && h.HandshakeMs == 0 || !last.IsZero() && now.Sub(last) > 14*24*time.Hour {
			delete(c.f.Health, k)
		}
	}
	b, err := json.MarshalIndent(&c.f, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	c.lastSave = now
	return os.Rename(tmp, c.path)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// saveSoon saves unless a save happened in the last 30s.
func (c *Cache) saveSoon() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastSave) > 30*time.Second {
		_ = c.saveLocked()
	}
}

// Age is how old the stored server list is (a huge value if there is none).
func (c *Cache) Age(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.f.Servers) == 0 || c.f.FetchedAt.IsZero() {
		return 1<<62 - 1
	}
	return now.Sub(c.f.FetchedAt)
}

// Servers returns the stored list and the plan's device limit.
func (c *Cache) Servers() ([]proton.Server, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]proton.Server(nil), c.f.Servers...), c.f.MaxConnect
}

// SetServers replaces the stored list with a freshly fetched one.
func (c *Cache) SetServers(list []proton.Server, maxConnect int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.f.Servers = append([]proton.Server(nil), list...)
	c.f.FetchedAt = now
	if maxConnect > 0 {
		c.f.MaxConnect = maxConnect
	}
}

func (c *Cache) srv(name string) *Health {
	if c.f.Health == nil {
		c.f.Health = map[string]*Health{}
	}
	h := c.f.Health[name]
	if h == nil {
		h = &Health{}
		c.f.Health[name] = h
	}
	return h
}

func (c *Cache) cty(code string) *Health {
	if c.f.Countries == nil {
		c.f.Countries = map[string]*Health{}
	}
	h := c.f.Countries[code]
	if h == nil {
		h = &Health{}
		c.f.Countries[code] = h
	}
	return h
}

// RecordBlock notes that s was blocked, throttled or unusable. The server is excluded
// for Backoff(strikes); the country gets a strike too, and is excluded once it has
// several (a block is often about the whole country's address range).
func (c *Cache) RecordBlock(s proton.Server, reason string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.srv(s.Name).block(reason, now)
	if reason != "unreachable" { // a dead server says nothing about its country
		ch := c.cty(s.Country)
		ch.block(reason, now)
		if ch.Strikes >= countryBlockStrikes {
			ch.BlockedUntil = now.Add(Backoff(ch.Strikes - countryBlockStrikes + 1))
		} else {
			ch.BlockedUntil = time.Time{}
		}
	}
}

// RecordGood notes that s carried traffic (bytesPerSec <= 0 means "not measured").
func (c *Cache) RecordGood(s proton.Server, bytesPerSec float64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.srv(s.Name).good(bytesPerSec, now)
	c.cty(s.Country).good(bytesPerSec, now)
}

// RecordConnect notes a connection attempt: how long the handshake took, or that it failed.
func (c *Cache) RecordConnect(s proton.Server, ok bool, took time.Duration, now time.Time) {
	if !ok {
		c.RecordBlock(s, "unreachable", now)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.srv(s.Name)
	h.HandshakeMs = ewma(h.HandshakeMs, float64(took.Milliseconds()))
}

// Blocked reports whether s (or its whole country) is currently excluded.
func (c *Cache) Blocked(s proton.Server, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blockedLocked(s, now)
}

func (c *Cache) blockedLocked(s proton.Server, now time.Time) bool {
	return c.f.Health[s.Name].blocked(now) || c.f.Countries[s.Country].blocked(now)
}

// cost is the server's badness: lower is better. Proton's own score and load come
// first; our own record of the server and its country adjusts them.
func (c *Cache) costLocked(s proton.Server, now time.Time) float64 {
	cost := s.Score + float64(s.Load)/25
	if h := c.f.Health[s.Name]; h != nil {
		cost += 0.8 * float64(h.strikes(now))
		if h.Successes > 0 && h.strikes(now) == 0 {
			cost -= 0.5
		}
		cost += h.HandshakeMs / 4000 // 4s handshake costs one point
		if h.BytesPerSec > 0 {
			cost -= min(h.BytesPerSec/(2<<20), 1) * 0.5 // up to half a point for >=2 MB/s
		}
	}
	if ch := c.f.Countries[s.Country]; ch != nil {
		cost += 0.5 * float64(ch.strikes(now))
		if ch.Successes > 0 && ch.strikes(now) == 0 {
			cost -= 0.3
		}
	}
	return cost
}

// RankOpts steers Rank.
type RankOpts struct {
	Avoid         map[string]bool // server names never returned
	UsedCountries map[string]int  // countries already carrying an exit (how many)
	AvoidCountry  string          // country to move away from (e.g. where a block happened)
	Countries     []string        // if set, only these country codes (case-insensitive)
	NoShuffle     bool            // deterministic order (tests); otherwise the best few vary
}

// Rank orders candidates best first: low load and score, good record, and spread over
// distinct countries (countries already in use, and AvoidCountry, come after fresh ones).
// Servers that are currently blocked come last, soonest-to-recover first, so something is
// still returned when everything is blocked.
func (c *Cache) Rank(all []proton.Server, o RankOpts, now time.Time) []proton.Server {
	var want map[string]bool
	if len(o.Countries) > 0 {
		want = map[string]bool{}
		for _, cc := range o.Countries {
			want[strings.ToUpper(strings.TrimSpace(cc))] = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	type item struct {
		s     proton.Server
		cost  float64
		prio  float64 // rank within country + penalties
		group float64 // cost of the country's best server
	}
	byCountry := map[string][]item{}
	var blocked []proton.Server
	for _, s := range all {
		if o.Avoid[s.Name] || (want != nil && !want[strings.ToUpper(s.Country)]) {
			continue
		}
		if c.blockedLocked(s, now) {
			blocked = append(blocked, s)
			continue
		}
		byCountry[s.Country] = append(byCountry[s.Country], item{s: s, cost: c.costLocked(s, now)})
	}
	var items []item
	for cc, list := range byCountry {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].cost != list[j].cost {
				return list[i].cost < list[j].cost
			}
			return list[i].s.Name < list[j].s.Name
		})
		if !o.NoShuffle && len(list) > 1 { // vary which of the best few leads, run to run
			k := min(len(list), 3)
			j := rand.Intn(k)
			list[0], list[j] = list[j], list[0]
		}
		pen := float64(o.UsedCountries[cc])
		if o.AvoidCountry != "" && strings.EqualFold(cc, o.AvoidCountry) {
			pen += 2
		}
		for i := range list {
			list[i].prio = float64(i) + pen
			list[i].group = list[0].cost + countryPrior(cc)
		}
		items = append(items, list...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch {
		case a.prio != b.prio:
			return a.prio < b.prio
		case a.group != b.group:
			return a.group < b.group
		case a.cost != b.cost:
			return a.cost < b.cost
		}
		return a.s.Name < b.s.Name
	})
	out := make([]proton.Server, 0, len(items)+len(blocked))
	for _, it := range items {
		out = append(out, it.s)
	}
	until := func(s proton.Server) time.Time {
		return maxTime(c.f.Health[s.Name].until(), c.f.Countries[s.Country].until())
	}
	sort.SliceStable(blocked, func(i, j int) bool { return until(blocked[i]).Before(until(blocked[j])) })
	return append(out, blocked...)
}

func (h *Health) until() time.Time {
	if h == nil {
		return time.Time{}
	}
	return h.BlockedUntil
}

// ServerStat is one row of Pool.Snapshot: a server's listing data plus our record of it.
type ServerStat struct {
	Name, Country, City string
	Load                int
	Score               float64
	Active              bool // carrying an exit right now
	Warm                bool // connected spare
	Blocked             bool // excluded now (server or whole country)
	BlockedUntil        time.Time
	LastReason          string
	Successes, Failures int
	Strikes             int
	HandshakeMs         float64
	BytesPerSec         float64
	CountryStrikes      int
}

// stat builds one ServerStat. The caller holds c.mu.
func (c *Cache) stat(s proton.Server, now time.Time) ServerStat {
	st := ServerStat{Name: s.Name, Country: s.Country, City: s.City, Load: s.Load, Score: s.Score}
	if h := c.f.Health[s.Name]; h != nil {
		st.Successes, st.Failures, st.Strikes = h.Successes, h.Failures, h.strikes(now)
		st.LastReason, st.HandshakeMs, st.BytesPerSec = h.LastReason, h.HandshakeMs, h.BytesPerSec
		st.BlockedUntil = h.BlockedUntil
	}
	if ch := c.f.Countries[s.Country]; ch != nil {
		st.CountryStrikes = ch.strikes(now)
		st.BlockedUntil = maxTime(st.BlockedUntil, ch.BlockedUntil)
	}
	st.Blocked = c.blockedLocked(s, now)
	if !st.Blocked {
		st.BlockedUntil = time.Time{}
	}
	return st
}

// wellConnected are countries with large, well-peered Proton server clusters. With no
// measurements yet they make far better download exits than tiny countries whose servers
// merely have a low Proton score (real runs: exits in ZW/LY/CD moved almost nothing while
// one US exit did 10 of 12 tracks). Measured throughput in the cache outweighs this prior.
var wellConnected = map[string]bool{
	"US": true, "CA": true, "GB": true, "DE": true, "NL": true, "CH": true, "SE": true, "FR": true,
	"JP": true, "ES": true, "IT": true, "AU": true, "SG": true, "NO": true, "DK": true, "FI": true,
	"PL": true, "AT": true, "BE": true, "IE": true, "CZ": true, "RO": true, "PT": true, "HK": true,
}

// countryPrior is added to a country's rank cost: 0 for well-connected countries, a clear
// penalty for the rest.
func countryPrior(cc string) float64 {
	if wellConnected[strings.ToUpper(cc)] {
		return 0
	}
	return 6
}
