//go:build !cgo

// Command ytm-desktop is ytm's native desktop window. This build has no C compiler (CGO is
// off), and the window toolkit needs one: build with CGO_ENABLED=1 (just build-desktop).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ytm-desktop was built without cgo, which its window toolkit needs: rebuild with CGO_ENABLED=1")
	os.Exit(1)
}
