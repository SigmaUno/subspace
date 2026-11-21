package scope_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/scope"
)

func rules(r scope.Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Rule+":"+f.Severity)
	}
	return out
}

func TestScore(t *testing.T) {
	tests := []struct {
		name string
		in   scope.Input
		want []string
	}{
		{"clean", scope.Input{Diff: scope.Diff{Files: []string{"internal/api/a.go", "internal/api/x/b.go"}}, Paths: []string{"internal/api/**"}}, nil},
		{"no declared paths", scope.Input{Diff: scope.Diff{Files: []string{"anything/at/all.go"}}}, nil},
		{"outside", scope.Input{Diff: scope.Diff{Files: []string{"internal/api/a.go", "cmd/main.go"}}, Paths: []string{"internal/api"}},
			[]string{"outside_paths:error"}},
		{"directory claims children", scope.Input{Diff: scope.Diff{Files: []string{"web/src/a.ts"}}, Paths: []string{"web"}}, nil},
		{"single segment wildcard", scope.Input{Diff: scope.Diff{Files: []string{"a/b.go", "a/c/d.go"}}, Paths: []string{"a/*.go"}},
			[]string{"outside_paths:error"}},
		{"globstar in the middle", scope.Input{Diff: scope.Diff{Files: []string{"a/x/y/z.go"}}, Paths: []string{"a/**/z.go"}}, nil},
		{"question mark", scope.Input{Diff: scope.Diff{Files: []string{"a/b1.go", "a/b12.go"}}, Paths: []string{"a/b?.go"}},
			[]string{"outside_paths:error"}},
		{"workflow", scope.Input{Diff: scope.Diff{Files: []string{".github/workflows/ci.yml"}}}, []string{"protected_path:error"}},
		{"protected and outside", scope.Input{Diff: scope.Diff{Files: []string{"Dockerfile"}}, Paths: []string{"internal/**"}},
			[]string{"outside_paths:error", "protected_path:error"}},
		{"broad path does not allow protected", scope.Input{Diff: scope.Diff{Files: []string{".github/ci.yml"}}, Paths: []string{"**"}},
			[]string{"protected_path:error"}},
		{"explicit directory allows protected", scope.Input{Diff: scope.Diff{Files: []string{".github/ci.yml", "x.go"}}, Paths: []string{".github/**", "x.go"}}, nil},
		{"explicit lockfile allows", scope.Input{Diff: scope.Diff{Files: []string{"web/pnpm-lock.yaml"}}, Paths: []string{"web/pnpm-lock.yaml"}}, nil},
		{"web does not allow nested lockfile", scope.Input{Diff: scope.Diff{Files: []string{"web/pnpm-lock.yaml"}}, Paths: []string{"web"}},
			[]string{"protected_path:error"}},
		{"lockfile", scope.Input{Diff: scope.Diff{Files: []string{"go.sum"}}}, []string{"protected_path:error"}},
		{"secret names", scope.Input{Diff: scope.Diff{Files: []string{"cfg/.env", "keys/server.pem", "a/client_secret.json"}}}, []string{"protected_path:error"}},
		{"env example is fine", scope.Input{Diff: scope.Diff{Files: []string{".env.example"}}}, nil},
		{"compose", scope.Input{Diff: scope.Diff{Files: []string{"docker-compose.prod.yml"}}}, []string{"protected_path:error"}},
		{"large diff", scope.Input{Diff: scope.Diff{Additions: 900, Deletions: 100}, MaxMinutes: 30}, []string{"diff_too_large:warning"}},
		{"diff within budget", scope.Input{Diff: scope.Diff{Additions: 500, Deletions: 100}, MaxMinutes: 30}, nil},
		{"no budget no size check", scope.Input{Diff: scope.Diff{Additions: 1e6}}, nil},
		{"branch mismatch", scope.Input{Diff: scope.Diff{HeadRef: "feature/x"}, Branch: "sub/t-3-x"}, []string{"branch_mismatch:error"}},
		{"branch match", scope.Input{Diff: scope.Diff{HeadRef: "sub/t-3-x"}, Branch: "sub/t-3-x"}, nil},
		{"unknown head ref", scope.Input{Branch: "sub/t-3-x"}, nil},
		{"unexpected author", scope.Input{Diff: scope.Diff{Authors: []string{"Bot", "mallory"}}, AllowedAuthors: []string{"bot"}},
			[]string{"unexpected_author:error"}},
		{"authors unchecked without allow list", scope.Input{Diff: scope.Diff{Authors: []string{"mallory"}}}, nil},
		{"truncated", scope.Input{Diff: scope.Diff{Truncated: true}}, []string{"diff_truncated:warning"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := scope.Score(tc.in)
			if got := rules(r); !slices.Equal(got, tc.want) {
				t.Fatalf("findings %v, want %v", got, tc.want)
			}
			wantBlocking := slices.ContainsFunc(tc.want, func(s string) bool { return s[len(s)-5:] == "error" })
			if r.Blocking() != wantBlocking {
				t.Fatalf("blocking %v, want %v", r.Blocking(), wantBlocking)
			}
		})
	}
}

func TestScoreListsAtMostTwentyPaths(t *testing.T) {
	var files []string
	for i := range 100 {
		files = append(files, fmt.Sprintf("other/f%d.go", i))
	}
	r := scope.Score(scope.Input{Diff: scope.Diff{Files: files}, Paths: []string{"a"}})
	if len(r.Findings) != 1 || len(r.Findings[0].Paths) != 20 || r.Files != 100 {
		t.Fatalf("report %+v", r)
	}
}

func TestGlobstarMatchingIsNotExponential(t *testing.T) {
	pattern := "**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/**/zzz"
	file := "a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/a/q"
	r := scope.Score(scope.Input{Diff: scope.Diff{Files: []string{file}}, Paths: []string{pattern}})
	if len(r.Findings) != 1 {
		t.Fatalf("report %+v", r)
	}
}
