package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestGetInstructionsKindGolden(t *testing.T) {
	e := servertest.New(t)
	cases := []struct{ id, kind, golden string }{
		{"claude-1", "claude-code", "worker-claude-code.txt"},
		{"codex-1", "codex", "worker-codex.txt"},
		{"opencode-1", "opencode", "worker-opencode.txt"},
		{"other-1", "other", "worker-other.txt"},
	}
	for _, c := range cases {
		tok := e.Register(c.id, "worker", c.kind)
		got := e.Must(tok, "get_instructions", nil)["instructions"].(string)
		want, err := os.ReadFile(filepath.Join("testdata", "instructions", c.golden))
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s instructions differ from %s:\n%s", c.kind, c.golden, got)
		}
	}

	lead := e.Register("rex", "leader", "claude-code")
	if got := e.Must(lead, "get_instructions", nil)["instructions"].(string); strings.Contains(got, "tips:") {
		t.Fatalf("leader got worker kind tips: %s", got)
	}
}
