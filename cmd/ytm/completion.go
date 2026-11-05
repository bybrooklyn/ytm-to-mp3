package main

import (
	"fmt"
	"strings"
)

// flagNames are ytm's options; everything else on the command line is a link or a file.
var flagNames = []string{"-o", "-f", "-j", "-y", "--no-vpn", "--connections", "--dry-run", "--retry", "--all",
	"--albums", "--albums-min", "--source", "--maybe", "--premium-upgrade", "--completion", "--version", "--help"}

// completionScript returns a tab-completion script for the given shell: options, and file
// names (a Takeout export, a file of links).
func completionScript(shell string) (string, error) {
	flags := strings.Join(flagNames, " ")
	switch shell {
	case "bash":
		return fmt.Sprintf(`_ytm() {
  local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
  case $prev in
    -f) COMPREPLY=($(compgen -W "mp3 m4a opus flac" -- "$cur")); return ;;
    -o) COMPREPLY=($(compgen -d -- "$cur")); return ;;
    --source) COMPREPLY=($(compgen -W "youtube" -- "$cur")); return ;;
    --completion) COMPREPLY=($(compgen -W "bash zsh fish powershell" -- "$cur")); return ;;
  esac
  COMPREPLY=($(compgen -W "%s" -- "$cur") $(compgen -f -- "$cur"))
}
complete -F _ytm ytm
`, flags), nil
	case "zsh":
		return fmt.Sprintf(`#compdef ytm
_ytm() {
  local -a flags
  flags=(%s)
  case $words[CURRENT-1] in
    -f) compadd mp3 m4a opus flac; return ;;
    -o) _files -/; return ;;
    --source) compadd youtube; return ;;
    --completion) compadd bash zsh fish powershell; return ;;
  esac
  compadd -a flags
  _files
}
compdef _ytm ytm
`, flags), nil
	case "fish":
		var b strings.Builder
		b.WriteString("complete -c ytm -s o -r -F -d 'music folder'\n")
		b.WriteString("complete -c ytm -s f -x -a 'mp3 m4a opus flac' -d 'format'\n")
		b.WriteString("complete -c ytm -s j -x -d 'max parallel downloads'\n")
		b.WriteString("complete -c ytm -s y -d 'assume yes'\n")
		b.WriteString("complete -c ytm -l source -x -a 'youtube' -d 'audio source for Apple Music/Spotify links'\n")
		b.WriteString("complete -c ytm -l completion -x -a 'bash zsh fish powershell' -d 'print tab completion'\n")
		for _, f := range flagNames {
			if n := strings.TrimPrefix(f, "--"); n != f && n != "source" && n != "completion" {
				fmt.Fprintf(&b, "complete -c ytm -l %s\n", n)
			}
		}
		return b.String(), nil
	case "powershell":
		quoted := make([]string, 0, len(flagNames))
		for _, w := range flagNames {
			quoted = append(quoted, "'"+w+"'")
		}
		return fmt.Sprintf(`Register-ArgumentCompleter -Native -CommandName ytm -ScriptBlock {
  param($wordToComplete)
  @(%s) | Where-Object { $_ -like "$wordToComplete*" } |
    ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }
}
`, strings.Join(quoted, ", ")), nil
	}
	return "", fmt.Errorf("unknown shell %q (use bash, zsh, fish or powershell)", shell)
}
