package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/playbooks"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestExampleBootstrapConfigsDryRun(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "1h")
	dir := filepath.Join("..", "..", "deploy", "examples")
	configs, err := filepath.Glob(filepath.Join(dir, "bootstrap", "*.json"))
	if err != nil || len(configs) < 3 {
		t.Fatalf("found %d bootstrap examples: %v", len(configs), err)
	}
	pbFiles, err := filepath.Glob(filepath.Join(dir, "playbooks", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := servertest.New(t)
	for _, f := range pbFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var ps []playbooks.Playbook
		if err := json.Unmarshal(raw, &ps); err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if code, out := env.REST("POST", "/api/v1/playbooks", p); code != 201 {
				t.Fatalf("import playbook %s: %d %v", p.Name, code, out)
			}
		}
	}
	getenv := func(k string) string {
		return map[string]string{"SUBSPACE_URL": env.TS.URL, "SUBSPACE_OWNER_TOKEN": servertest.OwnerToken}[k]
	}
	for _, cfg := range configs {
		t.Run(filepath.Base(cfg), func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(t.Context(), []string{"-config", cfg, "-dry-run"}, getenv, &out, &errb); code != exitOK {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
			}
			if bytes.Contains(out.Bytes(), []byte(markProblem+" ")) {
				t.Fatalf("dry run reported problems:\n%s", out.String())
			}
		})
	}
}
