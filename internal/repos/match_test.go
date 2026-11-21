package repos_test

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/repos"
)

func TestProtectedPatternSemantics(t *testing.T) {
	for _, tc := range []struct {
		pattern, branch string
		want            bool
	}{
		{"main", "main", true},
		{"main", "main2", false},
		{"release/*", "release/1.0", true},
		{"release/*", "release/1.0/hotfix", false},
		{"release/**", "release/1.0/hotfix", true},
		{"v?.0", "v1.0", true},
		{"v?.0", "v10.0", false},
		{"a.b", "axb", false},
		{"sub/**", "sub/t-1-x", true},
	} {
		if got := repos.Match(tc.pattern, tc.branch); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.branch, got, tc.want)
		}
	}
}
