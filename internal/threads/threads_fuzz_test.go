package threads

import (
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func FuzzThreadParse(f *testing.F) {
	for _, s := range []string{"agent:me", "agent:claude-1", "task:T-3", "brief:B-1", "task:", "agent:a:b", "nope:x", "agent"} {
		f.Add(s, "claude-1")
		f.Add(s, "")
	}
	f.Fuzz(func(t *testing.T, s, self string) {
		r, err := Parse(s, self)
		if err != nil {
			return
		}
		kind, id, _ := strings.Cut(s, ":")
		want := id
		if kind == model.ThreadAgent && id == "me" && self != "" {
			want = self
		}
		if r.Kind != kind || r.ID != want || r.ID == "" {
			t.Fatalf("Parse(%q, %q) = %+v, want {%s %s}", s, self, r, kind, want)
		}
	})
}
