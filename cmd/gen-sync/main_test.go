package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/mcp"
)

func TestSpliceReplacesOnlyTheToolTable(t *testing.T) {
	section := mcp.ToolMatrixHeading + "\n\n| Tool | leader |\n|---|:-:|\n| a, b | ✓ |\n"
	doc := "intro\n\n" + mcp.ToolMatrixHeading + "\n\n| Tool | leader |\n|---|:-:|\n| old | ✓ |\n\nafter the table\n"
	got, err := splice(doc, section)
	if err != nil {
		t.Fatal(err)
	}
	want := "intro\n\n" + section + "\nafter the table\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := splice(got, section)
	if err != nil || again != got {
		t.Fatalf("splice is not idempotent: %v\n%s", err, again)
	}
}

func TestSpliceRejectsForeignInput(t *testing.T) {
	if _, err := splice("no heading here\n", mcp.ToolMatrixHeading+"\n\n"); err == nil {
		t.Fatal("missing heading accepted")
	}
	if _, err := splice(mcp.ToolMatrixHeading+"\n", "garbage"); err == nil {
		t.Fatal("foreign section accepted")
	}
}

func TestRunRewritesTheDoc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.md")
	section := mcp.ToolMatrixHeading + "\n\n| Tool | leader |\n|---|:-:|\n| a | ✓ |\n"
	if err := os.WriteFile(path, []byte("x\n\n"+mcp.ToolMatrixHeading+"\n\n| Tool |\n|---|\n| z |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(path, false, strings.NewReader(section), os.Stderr); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "x\n\n"+section {
		t.Fatalf("doc = %q", b)
	}
}
