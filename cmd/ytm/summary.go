package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// runSummary is the line at the end of a download, for ui.Styled:
// "  ✓ 12 downloaded  ✗ 1 failed  · 48 MB in 1m 12s". what names the source when a run
// had several ("YouTube"); "" for none. A count of zero failures is left out.
func runSummary(done, failed int, bytes int64, elapsed time.Duration, what string) string {
	var b strings.Builder
	b.WriteString("\n  ")
	if what != "" {
		b.WriteString(ui.Bold(what) + "  ")
	}
	b.WriteString(ui.OK("✓") + " " + ui.Bold(fmt.Sprint(done)) + " downloaded")
	if failed > 0 {
		b.WriteString("  " + ui.Fail("✗") + " " + ui.Red(fmt.Sprint(failed)) + " failed")
	}
	if bytes > 0 || elapsed > 0 { // both 0: not measured, left out
		tail := engine.HumanBytes(bytes)
		switch {
		case elapsed >= 10*time.Second:
			tail += " in " + elapsed.Round(time.Second).String()
		case elapsed > 0:
			tail += fmt.Sprintf(" in %.1fs", elapsed.Seconds())
		}
		b.WriteString("  " + ui.Dim("· "+tail))
	}
	return b.String()
}

// failureLines lists songs that didn't download grouped by why, the most common reason
// first, for ui.Styled: a red reason line, then its songs. At most limit songs are named.
func failureLines(fails []store.Failure, limit int) []string {
	byWhy := map[string][]string{}
	var whys []string
	for _, f := range fails {
		why := ui.Sanitize(f.Error)
		if why == "" {
			why = "unknown error"
		}
		if _, ok := byWhy[why]; !ok {
			whys = append(whys, why)
		}
		name := f.Title
		if f.Artist != "" && f.Title != "" {
			name = f.Artist + " - " + f.Title
		}
		if name == "" {
			name = f.ID
		}
		byWhy[why] = append(byWhy[why], ui.Sanitize(name))
	}
	sort.SliceStable(whys, func(i, j int) bool { return len(byWhy[whys[i]]) > len(byWhy[whys[j]]) })
	var out []string
	named := 0
	for _, why := range whys {
		songs := byWhy[why]
		out = append(out, fmt.Sprintf("  %s %s %s", ui.Fail("✗"), why, ui.Dim(fmt.Sprintf("(%d)", len(songs)))))
		for i, s := range songs {
			if named >= limit {
				if rest := len(songs) - i; rest > 0 {
					out = append(out, ui.Dim(fmt.Sprintf("      …and %d more", rest)))
				}
				break
			}
			out = append(out, "      "+s)
			named++
		}
	}
	return out
}
