//go:build !unix

package store

// noFollow is unavailable on this platform; O_EXCL alone still refuses to reuse a path.
const noFollow = 0
