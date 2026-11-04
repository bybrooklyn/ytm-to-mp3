package engine

import (
	"context"
	"encoding/csv"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
)

// TestBenchResolve compares resolving N ids one process each against batches.
// Hits YouTube for real, so it only runs when YTM_BENCH_IDS (a Takeout-style csv, first
// column = id) and YTM_BENCH_YTDLP are set.
func TestBenchResolve(t *testing.T) {
	csvPath, bin := os.Getenv("YTM_BENCH_IDS"), os.Getenv("YTM_BENCH_YTDLP")
	if csvPath == "" || bin == "" {
		t.Skip("set YTM_BENCH_IDS and YTM_BENCH_YTDLP to run")
	}
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows[1:] {
		ids = append(ids, r[0])
	}
	y := ytdlp{tools: deps.Tools{YtDlp: bin, JSRuntimes: []string{"deno"}}}
	ctx := context.Background()
	const par = 3
	run := func(groups [][]string) time.Duration {
		start := time.Now()
		var wg sync.WaitGroup
		sem := make(chan struct{}, par)
		for _, g := range groups {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if len(g) == 1 {
					if _, err := y.Resolve(ctx, g[0]); err != nil {
						t.Logf("single %s: %v", g[0], err)
					}
					return
				}
				m, err := y.ResolveBatch(ctx, g, nil)
				if err != nil || len(m) != len(g) {
					t.Logf("batch: %v, %d/%d", err, len(m), len(g))
				}
			}()
		}
		wg.Wait()
		return time.Since(start)
	}
	var singles, batches [][]string
	for _, id := range ids {
		singles = append(singles, []string{id})
	}
	for i := 0; i < len(ids); i += 4 {
		batches = append(batches, ids[i:min(i+4, len(ids))])
	}
	ds := run(singles)
	db := run(batches)
	t.Logf("%d ids, parallelism %d: single %v (%.2f tracks/s), batched(4) %v (%.2f tracks/s), speedup x%.2f",
		len(ids), par, ds.Round(time.Millisecond), float64(len(ids))/ds.Seconds(),
		db.Round(time.Millisecond), float64(len(ids))/db.Seconds(), ds.Seconds()/db.Seconds())
}
