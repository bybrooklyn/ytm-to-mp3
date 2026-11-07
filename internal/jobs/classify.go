package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"syscall"
)

// Class is what kind of failure an error is, shared by every source.
type Class string

const (
	ClassNone        Class = ""
	ClassCancelled   Class = "cancelled"   // the run was stopped
	ClassAuth        Class = "auth"        // login missing, expired or refused
	ClassThrottle    Class = "throttle"    // rate limited, bot check, too many requests
	ClassNetwork     Class = "network"     // connection trouble, timeouts, server errors
	ClassUnavailable Class = "unavailable" // the source doesn't have it (removed, region, not offered)
	ClassCorrupt     Class = "corrupt"     // downloaded but failed the integrity check
	ClassDisk        Class = "disk"        // can't write (full, read-only, permissions)
	ClassOther       Class = "other"
)

// Retryable reports whether trying the same thing again later can work.
func (c Class) Retryable() bool {
	switch c {
	case ClassThrottle, ClassNetwork, ClassCorrupt, ClassCancelled, ClassOther:
		return true
	}
	return false
}

// Trips reports whether repeated failures of this class mean the whole source is the
// problem (so the Breaker should count it), rather than one song.
func (c Class) Trips() bool { return c == ClassAuth || c == ClassThrottle }

// Statuser is implemented by errors that carry an HTTP status (tidal.APIError does).
type Statuser interface{ HTTPStatus() int }

// Classer is implemented by errors that already know their class by name (engine.Error):
// "throttle", "permanent" or "transient".
type Classer interface{ JobClass() string }

// StatusError is an HTTP failure from a source that has no error type of its own.
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}
func (e *StatusError) HTTPStatus() int { return e.Status }

// CorruptError says a finished download failed its integrity check.
type CorruptError struct{ Msg string }

func (e *CorruptError) Error() string { return e.Msg }

// Classify sorts err into a Class.
func Classify(err error) Class {
	if err == nil {
		return ClassNone
	}
	if errors.Is(err, context.Canceled) {
		return ClassCancelled
	}
	var ce *CorruptError
	if errors.As(err, &ce) {
		return ClassCorrupt
	}
	var c Classer
	if errors.As(err, &c) {
		switch c.JobClass() {
		case "throttle":
			return ClassThrottle
		case "permanent":
			return ClassUnavailable
		case "transient":
			return ClassNetwork
		}
	}
	var s Statuser
	if errors.As(err, &s) {
		switch st := s.HTTPStatus(); {
		case st == 401:
			return ClassAuth
		case st == 403 && authText(err):
			return ClassAuth
		case st == 429 || st == 403:
			return ClassThrottle // refused: a rate limit, bot check or revoked link
		case st == 404 || st == 410 || st == 451:
			return ClassUnavailable
		case st >= 500:
			return ClassNetwork
		}
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EDQUOT) ||
		errors.Is(err, fs.ErrPermission) {
		return ClassDisk
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return ClassNetwork
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ClassNetwork
	}
	m := strings.ToLower(err.Error())
	switch {
	case authText(err):
		return ClassAuth
	case has(m, "429", "too many requests", "rate limit", "rate-limit", "not a bot", "try again later"):
		return ClassThrottle
	case has(m, "no space left", "read-only file system", "permission denied", "disk quota"):
		return ClassDisk
	case has(m, "isn't a flac", "truncated", "shorter than expected", "integrity", "corrupt"):
		return ClassCorrupt
	case has(m, "not available", "unavailable", "not found", "private video", "has been removed", "does not exist", "not offered"):
		return ClassUnavailable
	case has(m, "connection reset", "connection refused", "timeout", "timed out", "tls", "eof", "no such host", "network is unreachable"):
		return ClassNetwork
	}
	return ClassOther
}

func authText(err error) bool {
	return has(strings.ToLower(err.Error()), "unauthorized", "sign in again", "not signed in", "login", "token", "expired", "subscription", "invalid_grant")
}

func has(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// Human is a short sentence a person can act on, for the end-of-run summary.
func Human(err error) string {
	if err == nil {
		return ""
	}
	raw := firstLine(err.Error())
	switch Classify(err) {
	case ClassCancelled:
		return "stopped before it finished"
	case ClassAuth:
		return "the login was refused · sign in again in Settings"
	case ClassThrottle:
		return "the service was rate limiting · try again later"
	case ClassNetwork:
		return "the connection failed · " + raw
	case ClassUnavailable:
		return "not available from this source · " + raw
	case ClassCorrupt:
		return "the download was damaged and was discarded · " + raw
	case ClassDisk:
		return "couldn't write to disk · " + raw
	default:
		return raw
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 160 {
		s = s[:159] + "…"
	}
	return s
}
