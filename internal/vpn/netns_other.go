//go:build !linux

package vpn

import (
	"context"
	"errors"
)

// Pool is a set of live WireGuard exits (Linux only).
type Pool struct{ Exits []Exit }

func Up(ctx context.Context, files []string, logf func(string, ...any)) (*Pool, error) {
	return nil, errors.New("--vpn needs Linux (network namespaces). On this OS pass SOCKS proxies with --proxy instead")
}

func (p *Pool) Close()                {}
func Clean(logf func(string, ...any)) {}
func ProxyMain(socket string) error   { return errors.New("linux only") }
