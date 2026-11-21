package board

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SigmaUno/subspace/internal/spawner"
	"regexp"
	"strings"
)

func isErr(err, target error) bool { return errors.Is(err, target) }

var agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var providerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func nz[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

type noopSpawner struct{}

func (noopSpawner) Name() string                                 { return "noop" }
func (noopSpawner) Spawn(context.Context, spawner.Request) error { return nil }

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
