package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/deps"
)

var (
	// ErrNoPremiumFormat: YouTube didn't offer a Premium stream for this video (it may have
	// none, or the account isn't getting them right now).
	ErrNoPremiumFormat = errors.New("no Premium stream offered")
	// ErrAccountBlocked: YouTube wants the account to prove it isn't a bot, or the
	// cookies stopped working. The pass must stop, not retry.
	ErrAccountBlocked = errors.New("YouTube is challenging the account (bot check or login)")
)

// PremiumFormats is the Premium-only stream to ask for, given the library's format: Opus
// 774 for opus, AAC 141 for m4a (each copied without re-encoding), either for formats that
// are re-encoded anyway.
func PremiumFormats(format string) string {
	switch format {
	case "opus":
		return "774"
	case "m4a":
		return "141"
	}
	return "774/141"
}

// PremiumDownload fetches id's Premium stream into dir with the account's cookies, through
// proxy ("" = direct), and nothing else: there is no fallback format, so a song is never
// "upgraded" to the same 128k it already has.
func PremiumDownload(ctx context.Context, tools deps.Tools, proxy, cookies, cacheDir, format, id, dir string) (string, error) {
	y := ytdlp{tools: tools, proxy: proxy, cache: cacheDir, cookies: cookies, only: PremiumFormats(format)}
	p, err := y.Download(ctx, id, dir, nil)
	if err == nil || ctx.Err() != nil {
		return p, err
	}
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "requested format is not available"):
		return "", ErrNoPremiumFormat
	case strings.Contains(m, "not a bot"), strings.Contains(m, "login_required"),
		strings.Contains(m, "cookies are no longer valid"), strings.Contains(m, "http error 429"),
		strings.Contains(m, "too many requests"):
		// (not "sign in" alone: "Sign in to confirm your age" is just one song)
		return "", ErrAccountBlocked
	}
	return "", err
}
