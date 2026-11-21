package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

var globalFlags = []string{"--json", "--url", "--token-file", "--token", "--help"}

func parseCompletion(args []string, _ io.Reader) (command, error) {
	set := flags("completion")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	script, err := completionScript(set.Arg(0))
	if err != nil {
		return nil, err
	}
	return func(_ context.Context, _ *conn, p *printer) error { return p.text(script) }, nil
}

func completionScript(shell string) (string, error) {
	reg := registry()
	top := slices.Sorted(maps.Keys(reg))
	top = append(top, "help")
	switch shell {
	case "bash":
		return bashScript(top, reg), nil
	case "zsh":
		return zshScript(top, reg), nil
	case "fish":
		return fishScript(top, reg), nil
	}
	return "", fmt.Errorf("completion shell must be bash, zsh or fish")
}

func withSubs(reg map[string]entry) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(reg)) {
		if len(reg[name].subs) > 0 {
			out = append(out, name)
		}
	}
	return out
}

func bashScript(top []string, reg map[string]entry) string {
	var b strings.Builder
	b.WriteString(`_subspacectl() {
    local cur="${COMP_WORDS[COMP_CWORD]}" cmd="" at=0 i
    for ((i = 1; i < COMP_CWORD; i++)); do
        case "${COMP_WORDS[i]}" in
            --url|--token|--token-file) ((i++)) ;;
            -*) ;;
            *) cmd="${COMP_WORDS[i]}"; at=$i; break ;;
        esac
    done
    if [[ -z "$cmd" ]]; then
`)
	fmt.Fprintf(&b, "        COMPREPLY=($(compgen -W %q -- \"$cur\"))\n        return\n    fi\n", strings.Join(append(slices.Clone(top), globalFlags...), " "))
	b.WriteString("    if ((COMP_CWORD != at + 1)); then\n        return\n    fi\n    case \"$cmd\" in\n")
	for _, name := range withSubs(reg) {
		fmt.Fprintf(&b, "        %s) COMPREPLY=($(compgen -W %q -- \"$cur\")) ;;\n", name, strings.Join(reg[name].subs, " "))
	}
	b.WriteString("    esac\n}\ncomplete -F _subspacectl subspacectl\n")
	return b.String()
}

func zshScript(top []string, reg map[string]entry) string {
	var b strings.Builder
	b.WriteString(`#compdef subspacectl
_subspacectl() {
  local i=2 cmd=""
  while (( i < CURRENT )); do
    case $words[i] in
      --url|--token|--token-file) (( i += 2 )); continue ;;
      -*) (( i++ )); continue ;;
    esac
    cmd=$words[i]
    break
  done
  if [[ -z $cmd ]]; then
`)
	fmt.Fprintf(&b, "    compadd -- %s\n    return\n  fi\n", strings.Join(append(slices.Clone(top), globalFlags...), " "))
	b.WriteString("  (( CURRENT == i + 1 )) || return\n  case $cmd in\n")
	for _, name := range withSubs(reg) {
		fmt.Fprintf(&b, "    %s) compadd -- %s ;;\n", name, strings.Join(reg[name].subs, " "))
	}
	b.WriteString("  esac\n}\nif [ \"$funcstack[1]\" = \"_subspacectl\" ]; then\n  _subspacectl \"$@\"\nelse\n  compdef _subspacectl subspacectl\nfi\n")
	return b.String()
}

func fishScript(top []string, reg map[string]entry) string {
	var b strings.Builder
	b.WriteString("complete -c subspacectl -f\n")
	b.WriteString("complete -c subspacectl -l json -d 'compact JSON output'\n")
	b.WriteString("complete -c subspacectl -l url -r -d 'Subspace base URL'\n")
	b.WriteString("complete -c subspacectl -l token-file -r -F -d 'owner token file'\n")
	b.WriteString("complete -c subspacectl -l token -r -d 'owner token'\n")
	fmt.Fprintf(&b, "complete -c subspacectl -n __fish_use_subcommand -a %q\n", strings.Join(top, " "))
	for _, name := range withSubs(reg) {
		fmt.Fprintf(&b, "complete -c subspacectl -n '__fish_seen_subcommand_from %s; and not __fish_seen_subcommand_from %s' -a %q\n",
			name, strings.Join(reg[name].subs, " "), strings.Join(reg[name].subs, " "))
	}
	return b.String()
}
