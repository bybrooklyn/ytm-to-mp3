//go:build unix

package store

import "syscall"

// noFollow makes open(2) refuse a final-component symlink.
const noFollow = syscall.O_NOFOLLOW
