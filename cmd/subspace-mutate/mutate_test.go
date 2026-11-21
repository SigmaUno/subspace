package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureOptions(t *testing.T) *options {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	return &options{root: root, pkgs: []string{"."}, parallel: 4, timeout: 3 * time.Second}
}

func TestFixtureMutants(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test per mutant")
	}
	o := fixtureOptions(t)
	src := filepath.Join(o.root, "fixture.go")
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := mutants(o)
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{root: o.root, work: t.TempDir(), parallel: o.parallel, timeout: o.timeout}
	outs, err := r.run(context.Background(), ms)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]status{
		"fixture.go:8: replace > with <=":       killed,
		"fixture.go:8: replace > with >=":       survived,
		"fixture.go:8: change constant 0 to 1":  survived,
		"fixture.go:12: replace == with !=":     killed,
		"fixture.go:12: change constant 0 to 1": survived,
		"fixture.go:13: remove error return":    survived,
		"fixture.go:13: change constant 0 to 1": survived,
		"fixture.go:23: change constant 0 to 1": killed,
		"fixture.go:24: replace != with ==":     killed,
		"fixture.go:24: change constant 0 to 1": killed,
		"fixture.go:25: change constant 1 to 2": timedOut,
		"fixture.go:25: change constant 1 to 0": timedOut,
		"fixture.go:32: replace && with ||":     killed,
		"fixture.go:36: swap arguments n and 2": killed,
		"fixture.go:36: change constant 2 to 3": killed,
		"fixture.go:36: change constant 2 to 1": killed,
		"fixture.go:37: skip error check":       survived,
		"fixture.go:37: replace != with ==":     killed,
		"fixture.go:38: remove error return":    survived,
		"fixture.go:38: change constant 0 to 1": survived,
		"fixture.go:43: change constant 2 to 1": invalid,
		"fixture.go:43: change constant 2 to 3": survived,
	}
	got := map[string]status{}
	for _, o := range outs {
		got[o.m.String()] = o.status
	}
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("missing mutant %s", k)
		} else if g != w {
			t.Errorf("%s: got %s, want %s", k, g, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected mutant %s", k)
		}
	}
	var buf bytes.Buffer
	if score := report(&buf, outs); score < 57.1 || score > 57.2 {
		t.Errorf("score %.2f, want 12/21", score)
	}
	if !strings.Contains(buf.String(), "SURVIVED fixture.go:13: remove error return") {
		t.Errorf("report lacks survivor:\n%s", buf.String())
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("working tree source was modified")
	}
}

func TestSampleIsDeterministicSubset(t *testing.T) {
	o := fixtureOptions(t)
	all, err := mutants(o)
	if err != nil {
		t.Fatal(err)
	}
	o.sample, o.seed = 5, 7
	a, err := mutants(o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mutants(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("sample sizes %d, %d", len(a), len(b))
	}
	known := map[string]bool{}
	for _, m := range all {
		known[m.String()] = true
	}
	for i := range a {
		if a[i].String() != b[i].String() {
			t.Fatalf("sample differs at %d: %s vs %s", i, a[i], b[i])
		}
		if !known[a[i].String()] {
			t.Fatalf("sampled unknown mutant %s", a[i])
		}
	}
}

func TestLogCallsAreNotMutated(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module m\n\ngo 1.27\n")
	write("m.go", "package m\n\nimport \"log/slog\"\n\nfunc F(n int) {\n\tslog.Info(\"n\", \"big\", n > 10)\n}\n")
	ms, err := mutants(&options{root: dir, pkgs: []string{"."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("got mutants inside a log call: %v", ms)
	}
}
