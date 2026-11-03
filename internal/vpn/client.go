package vpn

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// NewClient returns an HTTP client that goes out through proxy ("" = direct).
func NewClient(proxy string) (*http.Client, error) {
	// Timeouts matter: a dead tunnel otherwise leaves requests hanging with no error, and
	// the exit never gets swapped.
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
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
