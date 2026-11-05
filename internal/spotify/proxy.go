package spotify

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// proxyClient builds an HTTP client that dials through proxyURL. An empty or
// "system" value yields the default client (which honours HTTP(S)_PROXY).
func proxyClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" || proxyURL == "system" {
		return http.DefaultClient, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
	}
	var transport *http.Transport
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if parsed.User != nil {
			pass, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: pass}
		}
		dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 dialer: %w", err)
		}
		transport = &http.Transport{Dial: dialer.Dial, TLSHandshakeTimeout: 30 * time.Second, ForceAttemptHTTP2: true}
	case "http", "https":
		transport = &http.Transport{Proxy: http.ProxyURL(parsed), TLSHandshakeTimeout: 30 * time.Second, ForceAttemptHTTP2: true}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}, nil
}
