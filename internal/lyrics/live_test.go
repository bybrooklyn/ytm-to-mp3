package lyrics

import (
	"context"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
)

// TestLiveLRCLIB asks the real lrclib.net about a few songs. Off unless YTM_LIVE=1.
func TestLiveLRCLIB(t *testing.T) {
	if appname.Getenv("LIVE") != "1" {
		t.Skip("set YTM_LIVE=1 to ask lrclib.net")
	}
	f := &Fetcher{Providers: []Provider{&LRCLIB{}}}
	for _, q := range []Query{
		{Title: "Bloom", Artist: "Radiohead", Album: "The King of Limbs", Duration: 315 * time.Second},
		{Title: "Get Lucky (feat. Pharrell Williams & Nile Rodgers) (Radio Edit)", Artist: "Daft Punk", Duration: 248 * time.Second},
		{Title: "Zzyzx Qwerty Nonexistent Tune", Artist: "Nobody Ever", Duration: 100 * time.Second},
	} {
		r, err := f.Lookup(context.Background(), q)
		n := 0
		if r.Kind == KindSynced {
			n = len(r.Synced)
		}
		t.Logf("%q: kind=%q source=%q lines=%d err=%v", q.Title, r.Kind, r.Source, n, err)
		time.Sleep(time.Second)
	}
}
