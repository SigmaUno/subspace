package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/dst"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testmain"
)

func TestMain(m *testing.M) { testmain.Run(m) }

func TestFailingSeedIsFiledOnceAsTaskAndRecorded(t *testing.T) {
	e := servertest.New(t)
	code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "DST findings", "body": "b", "repos": []string{"o/r"}})
	if code != 201 {
		t.Fatalf("create brief: %d %v", code, br)
	}
	brief := br["id"].(string)
	lead := e.Register("lead", "leader", "claude-code")

	ctx := context.Background()
	regs := filepath.Join(t.TempDir(), "regressions.txt")
	o := &dst.Outcome{Seed: 42, Steps: []dst.Step{{Op: dst.Op{Kind: dst.OpSweep}, At: dst.Epoch}},
		Violation: &dst.Violation{Invariant: dst.InvMerge, Detail: "T-3 merged without approval"}}
	for range 2 {
		f, err := newFiler(ctx, e.TS.URL+"/mcp", lead, brief, "")
		if err != nil {
			t.Fatal(err)
		}
		s := &sweep{mode: dst.ModeServer, steps: 400, regressions: regs, filer: f}
		if err := s.report(o.Seed, o); err != nil {
			t.Fatal(err)
		}
		if err := s.report(o.Seed, o); err != nil {
			t.Fatal(err)
		}
	}

	code, out := e.REST("GET", "/api/v1/tasks?brief_id="+brief, nil)
	if code != 200 {
		t.Fatalf("list tasks: %d %v", code, out)
	}
	tasks := out["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("want 1 filed task, got %d: %v", len(tasks), tasks)
	}
	task := tasks[0].(map[string]any)
	if task["title"] != findingTitle(dst.ModeServer, 42, dst.InvMerge) || task["repo"] != "o/r" {
		t.Fatalf("task = %v", task)
	}
	spec := task["spec"].(string)
	for _, want := range []string{"go run ./cmd/subspace-dst -mode server -seeds 1 -start 42 -steps 400", "T-3 merged without approval", "VIOLATION"} {
		if !strings.Contains(spec, want) {
			t.Fatalf("spec misses %q:\n%s", want, spec)
		}
	}

	got, err := dst.LoadRegressions(regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (dst.Regression{Seed: 42, Steps: 400, Mode: dst.ModeServer}) {
		t.Fatalf("regressions = %v", got)
	}
}
