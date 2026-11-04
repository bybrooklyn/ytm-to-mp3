package download

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Client is the shared HTTP client used by all Apple API requests. Its transport picks
// the proxy per request: the one attached to the request's context by WithProxy, else
// the default set by Init. So several downloads can run at once over different exits.
var Client = &http.Client{Transport: router{}}

func Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return Client.Do(req)
}

func GetWithHeaders(url string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return Client.Do(req)
}

// responseHeaderTimeout bounds how long a request waits for the server to
// start responding.
const responseHeaderTimeout = 60 * time.Second

var (
	mu           sync.Mutex
	defaultProxy string
	transports   = map[string]*http.Transport{}
)

// Init sets the default proxy for requests whose context names none.
// proxyURL may be empty (direct), or any of:
//
//	socks5://user:pass@host:port
//	socks5://host:port
//	http://host:port
//	https://host:port
func Init(proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if _, err := transportFor(proxyURL); err != nil {
		return err
	}
	mu.Lock()
	defaultProxy = proxyURL
	mu.Unlock()
	return nil
}

type proxyKey struct{}

// WithProxy returns a context whose requests go through proxyURL ("" = the default).
func WithProxy(ctx context.Context, proxyURL string) context.Context {
	return context.WithValue(ctx, proxyKey{}, strings.TrimSpace(proxyURL))
}

// router sends each request through the transport of its proxy.
type router struct{}

func (router) RoundTrip(req *http.Request) (*http.Response, error) {
	p, _ := req.Context().Value(proxyKey{}).(string)
	if p == "" {
		mu.Lock()
		p = defaultProxy
		mu.Unlock()
	}
	t, err := transportFor(p)
	if err != nil {
		return nil, err
	}
	if req.URL.Host != catalogHost {
		return t.RoundTrip(req)
	}
	// The catalog API rate-limits per address (429 after ~100 quick searches): pace it
	// per exit, and on a 429 rest that exit and try again (GETs only: they are safe to
	// repeat).
	l := limiterFor(p)
	for attempt := 0; ; attempt++ {
		if err := l.wait(req.Context()); err != nil {
			return nil, err
		}
		resp, err := t.RoundTrip(req)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests || req.Method != http.MethodGet || attempt >= 1 {
			return resp, err
		}
		resp.Body.Close()
		l.rest(retryAfter(resp.Header.Get("Retry-After")))
	}
}

const catalogHost = "amp-api.music.apple.com"

// Catalog pacing per exit: this many requests a second, with a small burst.
const (
	catalogRate  = 2.0
	catalogBurst = 4.0
	catalogRest  = 30 * time.Second // after a 429 without Retry-After
)

// limiter is a token bucket with a "rest until" time.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	until  time.Time
}

var limiters = map[string]*limiter{}

func limiterFor(proxyURL string) *limiter {
	mu.Lock()
	defer mu.Unlock()
	l, ok := limiters[proxyURL]
	if !ok {
		l = &limiter{tokens: catalogBurst, last: time.Now()}
		limiters[proxyURL] = l
	}
	return l
}

// wait blocks until a request may go out.
func (l *limiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		var d time.Duration
		if now.Before(l.until) {
			d = l.until.Sub(now)
		} else {
			l.tokens = min(catalogBurst, l.tokens+now.Sub(l.last).Seconds()*catalogRate)
			l.last = now
			if l.tokens >= 1 {
				l.tokens--
				l.mu.Unlock()
				return nil
			}
			d = time.Duration((1 - l.tokens) / catalogRate * float64(time.Second))
		}
		l.mu.Unlock()
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// rest pauses the exit for d.
func (l *limiter) rest(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u := time.Now().Add(d); u.After(l.until) {
		l.until = u
	}
	l.tokens = 0
}

// retryAfter reads a Retry-After header in seconds (capped), else catalogRest.
func retryAfter(h string) time.Duration {
	var s int
	if _, err := fmt.Sscan(h, &s); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, 2*time.Minute)
	}
	return catalogRest
}

// transportFor returns the (cached) transport for proxyURL; "" is a direct connection.
func transportFor(proxyURL string) (*http.Transport, error) {
	mu.Lock()
	defer mu.Unlock()
	if t, ok := transports[proxyURL]; ok {
		return t, nil
	}
	// No overall timeout: it would also cap reading the response body and abort large
	// downloads. Streaming downloads detect stalls themselves.
	t := &http.Transport{
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
	}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if parsed.User != nil {
				pass, _ := parsed.User.Password()
				auth = &proxy.Auth{User: parsed.User.Username(), Password: pass}
			}
			dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
			}
			cd, ok := dialer.(proxy.ContextDialer)
			if !ok {
				return nil, fmt.Errorf("SOCKS5 dialer cannot dial with a context")
			}
			t.DialContext = cd.DialContext
		case "http", "https":
			t.Proxy = http.ProxyURL(parsed)
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q (supported: socks5, http, https)", parsed.Scheme)
		}
	} else {
		t.Proxy = nil // never pick up HTTP_PROXY behind ytm's back: direct means direct
	}
	transports[proxyURL] = t
	return t, nil
}
