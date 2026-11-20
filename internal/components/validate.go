package components

import (
	"fmt"
	"path"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

func nameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be relative", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q escapes the repository", p)
	}
	return c, nil
}

func Validate(specs []Spec) error {
	_, err := resolve(specs)
	return err
}

func resolve(specs []Spec) ([][]int, error) {
	if len(specs) == 0 {
		return nil, model.BadRequest("component plan is empty")
	}
	index := make(map[string]int, len(specs))
	for i, s := range specs {
		key := nameKey(s.Name)
		if key == "" {
			return nil, model.BadRequest("component %d has an empty name", i+1)
		}
		if _, dup := index[key]; dup {
			return nil, model.BadRequest("duplicate component name %q", strings.TrimSpace(s.Name))
		}
		index[key] = i
		for _, p := range s.Paths {
			if _, err := cleanPath(p); err != nil {
				return nil, model.BadRequest("component %q: %v", strings.TrimSpace(s.Name), err)
			}
		}
	}

	deps := make([][]int, len(specs))
	for i, s := range specs {
		seen := make(map[int]bool, len(s.DependsOn))
		for _, d := range s.DependsOn {
			j, ok := index[nameKey(d)]
			switch {
			case !ok:
				return nil, model.BadRequest("component %q depends on unknown component %q", strings.TrimSpace(s.Name), d)
			case j == i:
				return nil, model.BadRequest("component %q depends on itself", strings.TrimSpace(s.Name))
			case seen[j]:
				return nil, model.BadRequest("component %q lists dependency %q twice", strings.TrimSpace(s.Name), d)
			}
			seen[j] = true
			deps[i] = append(deps[i], j)
		}
	}

	if cycle := findCycle(deps); cycle != nil {
		names := make([]string, len(cycle))
		for i, n := range cycle {
			names[i] = strings.TrimSpace(specs[n].Name)
		}
		return nil, model.BadRequest("dependency cycle: %s", strings.Join(names, " -> "))
	}
	return deps, nil
}

func findCycle(deps [][]int) []int {
	const (
		unvisited = iota
		active
		finished
	)
	state := make([]int, len(deps))
	var stack []int
	var visit func(n int) []int
	visit = func(n int) []int {
		state[n] = active
		stack = append(stack, n)
		for _, d := range deps[n] {
			switch state[d] {
			case active:
				start := 0
				for stack[start] != d {
					start++
				}
				return append(append([]int{}, stack[start:]...), d)
			case unvisited:
				if c := visit(d); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = finished
		return nil
	}
	for n := range deps {
		if state[n] == unvisited {
			if c := visit(n); c != nil {
				return c
			}
		}
	}
	return nil
}
