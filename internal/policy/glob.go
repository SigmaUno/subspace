package policy

import (
	"errors"
	"fmt"
	"strings"
)

func checkGlob(p string) error {
	if p == "" {
		return errors.New("empty glob pattern")
	}
	if len(p) > maxString {
		return fmt.Errorf("glob pattern longer than %d bytes", maxString)
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.Contains(seg, "**") && seg != "**" {
			return fmt.Errorf("** must be a whole path segment in %q", p)
		}
	}
	return nil
}

func Glob(pattern, s string) bool {
	ps, ss := strings.Split(pattern, "/"), strings.Split(s, "/")
	n, m := len(ps), len(ss)
	seen := make([]bool, (n+1)*(m+1))
	var visit func(i, j int) bool
	visit = func(i, j int) bool {
		if i == n {
			return j == m
		}
		k := i*(m+1) + j
		if seen[k] {
			return false
		}
		seen[k] = true
		if ps[i] == "**" {
			if visit(i+1, j) {
				return true
			}
			return j < m && visit(i, j+1)
		}
		return j < m && segment([]rune(ps[i]), []rune(ss[j])) && visit(i+1, j+1)
	}
	return visit(0, 0)
}

func segment(p, s []rune) bool {
	n, m := len(p), len(s)
	seen := make([]bool, (n+1)*(m+1))
	var visit func(i, j int) bool
	visit = func(i, j int) bool {
		if i == n {
			return j == m
		}
		k := i*(m+1) + j
		if seen[k] {
			return false
		}
		seen[k] = true
		switch p[i] {
		case '*':
			return visit(i+1, j) || j < m && visit(i, j+1)
		case '?':
			return j < m && visit(i+1, j+1)
		}
		return j < m && p[i] == s[j] && visit(i+1, j+1)
	}
	return visit(0, 0)
}
