package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SigmaUno/subspace/internal/guardhint"
	"github.com/SigmaUno/subspace/internal/mcp"
)

func main() {
	doc := flag.String("doc", "docs/api.md", "API document to update")
	manual := flag.Bool("manual", false, "print the steps that cannot be generated")
	flag.Parse()
	if err := run(*doc, *manual, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gen-sync:", err)
		os.Exit(1)
	}
}

func run(doc string, manual bool, in io.Reader, out io.Writer) error {
	if manual {
		_, err := io.WriteString(out, guardhint.Manual())
		return err
	}
	section, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(doc) //nolint:gosec // path is a command line flag
	if err != nil {
		return err
	}
	updated, err := splice(string(raw), string(section))
	if err != nil {
		return fmt.Errorf("%s: %w", doc, err)
	}
	if updated == string(raw) {
		return nil
	}
	return os.WriteFile(doc, []byte(updated), 0o644) //nolint:gosec // source file checked into the repo
}

func splice(doc, section string) (string, error) {
	if !strings.HasPrefix(section, mcp.ToolMatrixHeading+"\n") {
		return "", errors.New("generated section does not start with the tool permissions heading")
	}
	lines := strings.SplitAfter(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, "\n") == mcp.ToolMatrixHeading {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("heading %q not found", mcp.ToolMatrixHeading)
	}
	end := start + 1
	for end < len(lines) && strings.TrimSpace(lines[end]) == "" {
		end++
	}
	for end < len(lines) && strings.HasPrefix(lines[end], "|") {
		end++
	}
	return strings.Join(lines[:start], "") + section + strings.Join(lines[end:], ""), nil
}
