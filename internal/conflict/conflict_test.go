package conflict

import (
	"slices"
	"strings"
	"testing"
)

func TestOverlap(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"internal/api"}, []string{"internal/api"}, true},
		{[]string{"internal/api"}, []string{"internal/api/handler.go"}, true},
		{[]string{"internal/api/handler.go"}, []string{"internal/api"}, true},
		{[]string{"internal/api"}, []string{"internal/apiv2"}, false},
		{[]string{"internal/api"}, []string{"internal"}, true},
		{[]string{"internal/api"}, []string{"web"}, false},
		{[]string{"./internal//api/"}, []string{"internal/api/x.go"}, true},
		{[]string{"internal/*/handler.go"}, []string{"internal/api/handler.go"}, true},
		{[]string{"internal/*/handler.go"}, []string{"internal/api/routes.go"}, false},
		{[]string{"internal/*/handler.go"}, []string{"internal/api"}, true},
		{[]string{"internal/*.go"}, []string{"internal/a/b.go"}, false},
		{[]string{"internal/*.go"}, []string{"internal/x.go"}, true},
		{[]string{"internal/*.go"}, []string{"internal/x.ts"}, false},
		{[]string{"**/*.go"}, []string{"internal/a/b/c.go"}, true},
		{[]string{"**/*.go"}, []string{"web/app/page.tsx"}, true},
		{[]string{"**/*_test.go"}, []string{"cmd/*.go"}, true},
		{[]string{"**/*.md"}, []string{"docs/*.go"}, true},
		{[]string{"docs/**/*.md"}, []string{"web/**"}, false},
		{[]string{"**"}, []string{"anything/at/all"}, true},
		{[]string{"a/**/z"}, []string{"a/b/c/z"}, true},
		{[]string{"a/**/z"}, []string{"b/z"}, false},
		{[]string{"a/**/z.go"}, []string{"a/*/y.go"}, true},
		{[]string{"src/*.ts"}, []string{"src/*.go"}, false},
		{[]string{"src/a*"}, []string{"src/*b"}, true},
		{[]string{"src/a*c"}, []string{"src/*b"}, false},
		{[]string{"src/a?c"}, []string{"src/abc"}, true},
		{[]string{"src/a?c"}, []string{"src/ac"}, false},
		{[]string{"src/??"}, []string{"src/*x*"}, true},
		{[]string{"src/?"}, []string{"src/ab*"}, false},
		{[]string{"web/app/[id]/page.tsx"}, []string{"web/app/[id]"}, true},
		{[]string{"web/app/[id]/page.tsx"}, []string{"web/app/i/page.tsx"}, false},
		{[]string{"a/b", "c/d"}, []string{"x", "c"}, true},
		{nil, []string{"**"}, false},
		{[]string{"a"}, nil, false},
		{[]string{"Internal/api"}, []string{"internal/api"}, false},
		{[]string{"ünï/cödé"}, []string{"ünï/c?dé"}, true},
	}
	for _, c := range cases {
		got, err := Overlap(c.a, c.b)
		if err != nil {
			t.Fatalf("Overlap(%q, %q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if rev, _ := Overlap(c.b, c.a); rev != got {
			t.Errorf("Overlap not symmetric for %q, %q", c.a, c.b)
		}
	}
}

func TestOverlapRejectsAdversarialPaths(t *testing.T) {
	bad := []string{
		"", "/", "/etc/passwd", "//server/share", "C:/Windows", "c:", "..", "../x", "a/../../b", "a/..", "a/../b",
		"a\\..\\b", "..\\x", "a\x00b", "a\nb", "a\tb", " a", "a ", ".", "./", "a/..//b", strings.Repeat("a", 513),
		"\xff\xfe", "x/\u0085",
	}
	for _, p := range bad {
		if _, err := Normalize(p); err == nil {
			t.Errorf("Normalize(%q) accepted", p)
		}
		if _, err := Overlap([]string{p}, []string{"**"}); err == nil {
			t.Errorf("Overlap(%q) accepted", p)
		}
		if _, err := Overlap([]string{"a"}, []string{p}); err == nil {
			t.Errorf("Overlap(_, %q) accepted", p)
		}
	}
}

func TestNormalizeAll(t *testing.T) {
	got, err := NormalizeAll([]string{"./a//b/", "a/b", "c/./d", "...", "a/.hidden"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b", "c/d", "...", "a/.hidden"}; !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := NormalizeAll(make([]string, MaxPaths+1)); err == nil {
		t.Fatal("too many paths accepted")
	}
	if _, err := NormalizeAll([]string{"ok", "../no"}); err == nil || !strings.Contains(err.Error(), "../no") {
		t.Fatalf("error should name the bad path: %v", err)
	}
}

func TestOverlapPathological(t *testing.T) {
	a := []string{strings.Repeat("*a", 120) + "/" + strings.Repeat("**/", 40) + "x"}
	b := []string{strings.Repeat("a*", 120) + "/" + strings.Repeat("*/", 40) + "y"}
	if _, err := Overlap(a, b); err != nil {
		t.Fatal(err)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": Warn, "warn": Warn, " BLOCK ": Block, "off": Off} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if m, err := ParseMode("strict"); err == nil || m != Warn {
		t.Errorf("ParseMode(strict) = %q, %v", m, err)
	}
}
