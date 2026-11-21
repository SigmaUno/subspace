package conflict

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

type Mode string

const (
	Off   Mode = "off"
	Warn  Mode = "warn"
	Block Mode = "block"
)

const (
	MaxPaths   = 100
	maxPathLen = 512
	globstar   = "**"
)

var Held = []string{model.StatusAssigned, model.StatusInProgress, model.StatusSubmitted}

func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return Warn, nil
	case Off, Warn, Block:
		return m, nil
	}
	return Warn, fmt.Errorf("invalid conflict mode %q (want off, warn or block)", s)
}

func Normalize(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("empty path")
	case len(p) > maxPathLen:
		return "", fmt.Errorf("path longer than %d bytes", maxPathLen)
	case !utf8.ValidString(p):
		return "", errors.New("path is not valid UTF-8")
	case strings.TrimSpace(p) != p:
		return "", errors.New("path has surrounding whitespace")
	case strings.HasPrefix(p, "/"):
		return "", errors.New("path must be relative to the repository")
	case len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]):
		return "", errors.New("path must be relative to the repository")
	}
	for _, r := range p {
		if r == '\\' {
			return "", errors.New("path must use / as separator")
		}
		if unicode.IsControl(r) {
			return "", errors.New("path contains control characters")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("path must not contain a .. segment")
		}
	}
	c := path.Clean(p)
	if c == "." {
		return "", errors.New("path names the repository root; use ** instead")
	}
	return c, nil
}

func NormalizeAll(ps []string) ([]string, error) {
	if len(ps) > MaxPaths {
		return nil, fmt.Errorf("at most %d paths per task", MaxPaths)
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		n, err := Normalize(p)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", p, err)
		}
		if !contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func Overlap(a, b []string) (bool, error) {
	pa, err := compileAll(a)
	if err != nil {
		return false, err
	}
	pb, err := compileAll(b)
	if err != nil {
		return false, err
	}
	for _, x := range pa {
		for _, y := range pb {
			if intersect(x, y) {
				return true, nil
			}
		}
	}
	return false, nil
}

func Conflicts(t *model.Task, agentID string, others []*model.Task) []*model.Task {
	if len(t.Paths) == 0 {
		return nil
	}
	var out []*model.Task
	for _, o := range others {
		if o.ID == t.ID || o.Repo != t.Repo || o.Assignee == nil || *o.Assignee == agentID || !contains(Held, o.Status) {
			continue
		}
		if ok, err := Overlap(t.Paths, o.Paths); err == nil && ok {
			out = append(out, o)
		}
	}
	return out
}

type pattern [][]rune

func compileAll(ps []string) ([]pattern, error) {
	out := make([]pattern, 0, len(ps))
	for _, p := range ps {
		n, err := Normalize(p)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", p, err)
		}
		segs := strings.Split(n, "/")
		if segs[len(segs)-1] != globstar {
			segs = append(segs, globstar)
		}
		pat := make(pattern, len(segs))
		for i, s := range segs {
			pat[i] = []rune(s)
		}
		out = append(out, pat)
	}
	return out, nil
}

func isGlobstar(s []rune) bool { return len(s) == 2 && s[0] == '*' && s[1] == '*' }

func intersect(a, b pattern) bool {
	return reachable(len(a), len(b), func(i, j int, next func(int, int) bool) bool {
		as, bs := i < len(a) && isGlobstar(a[i]), j < len(b) && isGlobstar(b[j])
		if as && next(i+1, j) || bs && next(i, j+1) {
			return true
		}
		if i == len(a) || j == len(b) {
			return false
		}
		switch {
		case as && bs:
			return false
		case as:
			return next(i, j+1)
		case bs:
			return next(i+1, j)
		}
		return segmentsIntersect(a[i], b[j]) && next(i+1, j+1)
	})
}

func segmentsIntersect(x, y []rune) bool {
	return reachable(len(x), len(y), func(i, j int, next func(int, int) bool) bool {
		xs, ys := i < len(x) && x[i] == '*', j < len(y) && y[j] == '*'
		if xs && next(i+1, j) || ys && next(i, j+1) {
			return true
		}
		if i == len(x) || j == len(y) {
			return false
		}
		switch {
		case xs && ys:
			return false
		case xs:
			return next(i, j+1)
		case ys:
			return next(i+1, j)
		}
		return (x[i] == y[j] || x[i] == '?' || y[j] == '?') && next(i+1, j+1)
	})
}

func reachable(n, m int, step func(i, j int, next func(int, int) bool) bool) bool {
	seen := make([]bool, (n+1)*(m+1))
	var visit func(i, j int) bool
	visit = func(i, j int) bool {
		if i == n && j == m {
			return true
		}
		k := i*(m+1) + j
		if seen[k] {
			return false
		}
		seen[k] = true
		return step(i, j, visit)
	}
	return visit(0, 0)
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
