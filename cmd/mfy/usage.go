package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// flagRe finds an option in the help text ("--albums-min N", "-o DIR", "--source youtube").
var flagRe = regexp.MustCompile(`(^|[\s(])(--?[a-z][a-z-]*(?:=false)?(?: (?:[A-Z]+|youtube|SHELL))?)`)

// printUsage prints the help: on a terminal with the title bold, the section heading bold
// and every option cyan; piped, as plain text.
func printUsage() {
	if st, err := os.Stdout.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Print(usage)
		return
	}
	lines := strings.Split(strings.TrimRight(usage, "\n"), "\n")
	for i, l := range lines {
		switch {
		case i == 0:
			name, rest, _ := strings.Cut(l, " - ")
			l = ui.Accent("♪ ") + ui.Bold(name) + ui.Dim(" - "+rest)
		case i == 1:
			l = ui.Dim(l)
		case strings.HasSuffix(l, ":") && !strings.HasPrefix(l, " "):
			l = ui.Bold(l)
		case strings.HasPrefix(strings.TrimSpace(l), "mfy"):
			cmd, rest, _ := strings.Cut(strings.TrimSpace(l), "   ")
			l = "  " + ui.Cyan(cmd) + "   " + rest
		default:
			l = flagRe.ReplaceAllStringFunc(l, func(m string) string {
				flag := strings.TrimLeft(m, " \t(")
				return m[:len(m)-len(flag)] + ui.Cyan(flag)
			})
		}
		fmt.Println(l)
	}
}
