package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed fixturegen.go.tmpl
var programSource string

type generation struct{ name, commit string }

var generations = []generation{
	{"v1-store", "00636ce895755aa162541d3b4a3bb175ece90358"},
	{"v2-cross-review", "c32bce3f7c0a5058fb0814d42802c9adec22c245"},
	{"v3-audit-chain", "d998e5157fdc43fbe2b73df062ef9627f0db70dc"},
	{"v4-task-fields", "c7b10dd44e7d9838354eefef67ad53b7db6f965b"},
	{"v5-lease-poison", "16cd43b2241949eb93efd816b6af5a44e04ef3c0"},
	{"v6-spawn-lifecycle", "9efcf64cf2b54c40203321e58ce4caee9841e1ed"},
}

type features struct {
	CrossReview   bool
	AutoDone      bool
	TaskFields    bool
	Warnings      bool
	LeaseExpiries bool
	TokenExpiry   bool
}

const usage = `gen-fixtures builds the SQLite upgrade fixtures under internal/store/sqlite/testdata/fixtures.

For every generation (an earlier commit on the feat/server first-parent line) it:
  1. checks the commit out into a temporary git worktree,
  2. writes a small program that fills a database through that commit's own store code
     (briefs, plans, tasks in every status, agents with token hashes, messages, questions,
     audit rows, spawns; fields the commit does not know are left out),
  3. runs it with a fixed clock and fixed ids, then switches the file to journal_mode=DELETE and vacuums it,
  4. copies the single .db file into the output directory and removes the worktree.

Run it from the repository root (needs git and the Go toolchain with the module cache populated):

  go run ./cmd/gen-fixtures [-repo .] [-out internal/store/sqlite/testdata/fixtures] [-only v1-store,v3-audit-chain]

Fixtures are ignored by .gitignore (*.db); add regenerated files with git add -f.
The data set is identical for every generation, so TestUpgradeFixtures expects the same row counts from all of them.

Generations:
`

func main() {
	repo := flag.String("repo", ".", "git repository")
	out := flag.String("out", "internal/store/sqlite/testdata/fixtures", "output directory")
	only := flag.String("only", "", "comma separated generation names (default all)")
	flag.Usage = func() {
		_, _ = fmt.Fprint(flag.CommandLine.Output(), usage)
		for _, g := range generations {
			_, _ = fmt.Fprintf(flag.CommandLine.Output(), "  %-20s %s\n", g.name, g.commit)
		}
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "\nFlags:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*repo, *out, *only); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "gen-fixtures:", err)
		os.Exit(1)
	}
}

func run(repo, out, only string) error {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n != "" {
			selected[n] = true
		}
	}
	for n := range selected {
		if !hasGeneration(n) {
			return fmt.Errorf("unknown generation %q", n)
		}
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	for _, g := range generations {
		if len(selected) > 0 && !selected[g.name] {
			continue
		}
		if err := generate(repo, out, g); err != nil {
			return fmt.Errorf("%s: %w", g.name, err)
		}
		fmt.Println("wrote", filepath.Join(out, g.name+".db"))
	}
	return nil
}

func hasGeneration(name string) bool {
	for _, g := range generations {
		if g.name == name {
			return true
		}
	}
	return false
}

func generate(repo, out string, g generation) (err error) {
	tmp, err := os.MkdirTemp("", "gen-fixtures-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	wt := filepath.Join(tmp, "wt")
	if err := command(repo, "git", "worktree", "add", "--detach", wt, g.commit); err != nil {
		return err
	}
	defer func() {
		if rerr := command(repo, "git", "worktree", "remove", "--force", wt); rerr != nil && err == nil {
			err = rerr
		}
	}()
	src, err := program(wt)
	if err != nil {
		return err
	}
	dir := filepath.Join(wt, "cmd", "fixturegen")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o600); err != nil {
		return err
	}
	db := filepath.Join(tmp, g.name+".db")
	if err := command(wt, "go", "run", "./cmd/fixturegen", db); err != nil {
		return err
	}
	data, err := os.ReadFile(db) //nolint:gosec // path built from a temp dir
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, g.name+".db"), data, 0o600) //nolint:gosec // output directory is a command line flag
}

func program(wt string) ([]byte, error) {
	m, err := os.ReadFile(filepath.Join(wt, "internal", "model", "model.go")) //nolint:gosec // path built from a temp dir
	if err != nil {
		return nil, err
	}
	has := func(s string) bool { return bytes.Contains(m, []byte(s)) }
	f := features{
		CrossReview:   has("CrossReview"),
		AutoDone:      has("AutoDoneOnMerge"),
		TaskFields:    has("ComponentID"),
		Warnings:      has("PlanFinding"),
		LeaseExpiries: has("LeaseExpiries"),
		TokenExpiry:   has("TokenExpiresAt"),
	}
	t, err := template.New("fixturegen").Parse(programSource)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, f); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

func command(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // fixed tool names, arguments built by this program
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, b)
	}
	return nil
}
