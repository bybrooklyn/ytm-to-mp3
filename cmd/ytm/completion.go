package main

import (
	"fmt"
	"strings"
)

var (
	subcommands = []string{"setup", "login", "logout", "status", "doctor", "export", "tag", "update", "version", "help", "completion", "apple", "spotify"}
	flagNames   = []string{"-o", "-f", "-j", "--no-vpn", "--dry-run", "--retry", "--all", "-y", "--version",
		"--albums", "--connections", "--source", "--maybe", "--replace", "--premium-upgrade"}
)

// completionScript returns a tab-completion script for the given shell.
func completionScript(shell string) (string, error) {
	cmds := strings.Join(subcommands, " ")
	flags := strings.Join(flagNames, " ")
	switch shell {
	case "bash":
		return fmt.Sprintf(`_ytm() {
  local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
  case $prev in
    -f) COMPREPLY=($(compgen -W "mp3 m4a opus flac" -- "$cur")); return ;;
    -o) COMPREPLY=($(compgen -d -- "$cur")); return ;;
  esac
  if [ $COMP_CWORD -eq 1 ]; then
    COMPREPLY=($(compgen -W "%s %s" -- "$cur") $(compgen -f -- "$cur"))
  else
    COMPREPLY=($(compgen -W "%s" -- "$cur") $(compgen -f -- "$cur"))
  fi
}
complete -F _ytm ytm
`, cmds, flags, flags), nil
	case "zsh":
		return fmt.Sprintf(`#compdef ytm
_ytm() {
  local -a cmds flags
  cmds=(%s)
  flags=(%s)
  if [[ $words[CURRENT-1] == -f ]]; then compadd mp3 m4a opus flac; return; fi
  if [[ $words[CURRENT-1] == -o ]]; then _files -/; return; fi
  if (( CURRENT == 2 )); then compadd -a cmds; fi
  compadd -a flags
  _files
}
compdef _ytm ytm
`, cmds, flags), nil
	case "fish":
		var b strings.Builder
		b.WriteString("complete -c ytm -f\n")
		for _, c := range subcommands {
			fmt.Fprintf(&b, "complete -c ytm -n '__fish_use_subcommand' -a %s\n", c)
		}
		b.WriteString("complete -c ytm -s o -r -F -d 'music folder'\n")
		b.WriteString("complete -c ytm -s f -x -a 'mp3 m4a opus flac' -d 'format'\n")
		b.WriteString("complete -c ytm -s j -x -d 'max parallel downloads'\n")
		b.WriteString("complete -c ytm -l source -x -a 'youtube' -d 'audio source for Apple Music/Spotify links'\n")
		for _, f := range []string{"no-vpn", "dry-run", "retry", "all", "version", "albums", "connections", "maybe", "replace", "premium-upgrade"} {
			fmt.Fprintf(&b, "complete -c ytm -l %s\n", f)
		}
		return b.String(), nil
	case "powershell":
		quoted := make([]string, 0, len(subcommands)+len(flagNames))
		for _, w := range append(append([]string{}, subcommands...), flagNames...) {
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
