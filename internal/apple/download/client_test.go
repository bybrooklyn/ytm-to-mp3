package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterPaces(t *testing.T) {
	l := &limiter{tokens: catalogBurst, last: time.Now()}
	start := time.Now()
	for i := 0; i < int(catalogBurst)+2; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// the burst goes at once, the 2 after it at catalogRate
	if el := time.Since(start); el < 800*time.Millisecond || el > 2*time.Second {
		t.Errorf("burst+2 took %v", el)
	}
	l.rest(200 * time.Millisecond)
	start = time.Now()
	_ = l.wait(context.Background())
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Errorf("rest not honoured: %v", el)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.rest(time.Hour)
	if err := l.wait(ctx); err == nil {
		t.Error("wait ignores a cancelled context")
	}
}

func TestRetryAfter(t *testing.T) {
	if d := retryAfter("5"); d != 5*time.Second {
		t.Errorf("5 -> %v", d)
	}
	if d := retryAfter(""); d != catalogRest {
		t.Errorf("empty -> %v", d)
	}
	if d := retryAfter("99999"); d != 2*time.Minute {
		t.Errorf("huge -> %v", d)
	}
}

// A catalog 429 is retried after resting; other hosts are passed straight through.
func TestRouter429Retry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	// Route "amp-api.music.apple.com" to the test server through an HTTP proxy URL: the
	// test server answers proxied requests itself.
	p := "http://" + u.Host
	req, _ := http.NewRequestWithContext(WithProxy(context.Background(), p), http.MethodGet, "http://"+catalogHost+"/v1/x", nil)
	start := time.Now()
	resp, err := Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() != 2 {
		t.Fatalf("status %d after %d hits", resp.StatusCode, hits.Load())
	}
	if time.Since(start) < time.Second {
		t.Error("retried without resting")
	}
}
