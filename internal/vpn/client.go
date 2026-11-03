package vpn

import (
	"net/http"
	"net/url"
	"time"
)

// NewClient returns an HTTP client that goes out through proxy ("" = direct).
func NewClient(proxy string) (*http.Client, error) {
	tr := &http.Transport{
		MaxIdleConnsPerHost: 32,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if proxy == "" {
		tr.Proxy = http.ProxyFromEnvironment
	} else {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr}, nil
}
