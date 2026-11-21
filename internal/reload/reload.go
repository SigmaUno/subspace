package reload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
)

const maxFileBytes = 1 << 20

const ReasonRestart = "requires restart"

var RestartKeys = []string{"SUBSPACE_OWNER_TOKEN", "SUBSPACE_OWNER_TOKEN_FILE", "SUBSPACE_DB", "SUBSPACE_ADDR"}

type Ignored struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type Plan struct {
	Commit  func()
	Ignored []Ignored
}

type Reloadable interface {
	PrepareReload(getenv func(string) string) (Plan, error)
}

type Result struct {
	Applied []string  `json:"applied"`
	Ignored []Ignored `json:"ignored"`
}

type Error struct{ Errors []string }

func (e *Error) Error() string {
	return fmt.Sprintf("reload rejected: %s", strings.Join(e.Errors, "; "))
}

type part struct {
	name string
	keys []string
	r    Reloadable
}

type Manager struct {
	file string
	base func(string) string

	mu    sync.Mutex
	parts []part
}

func New(file string, base func(string) string) *Manager {
	if base == nil {
		base = os.Getenv
	}
	return &Manager{file: file, base: base}
}

func (m *Manager) Register(name string, keys []string, r Reloadable) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parts = append(m.parts, part{name: name, keys: keys, r: r})
}

func (m *Manager) Load() (func(string) string, error) {
	over, err := m.read()
	if err != nil {
		return nil, err
	}
	return m.overlay(over), nil
}

func (m *Manager) Check() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	over, err := m.read()
	if err != nil {
		return err
	}
	if _, errs := m.classify(over); len(errs) > 0 {
		return &Error{errs}
	}
	return nil
}

func (m *Manager) Reload() (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	over, err := m.read()
	if err != nil {
		return Result{}, &Error{[]string{err.Error()}}
	}
	ignored, errs := m.classify(over)
	getenv := m.overlay(over)
	var commits []func()
	for _, p := range m.parts {
		plan, err := p.r.PrepareReload(getenv)
		if err != nil {
			errs = append(errs, p.name+": "+err.Error())
			continue
		}
		if plan.Commit != nil {
			commits = append(commits, plan.Commit)
		}
		ignored = append(ignored, plan.Ignored...)
	}
	if len(errs) > 0 {
		return Result{}, &Error{errs}
	}
	for _, c := range commits {
		c()
	}
	skip := make(map[string]bool, len(ignored))
	for _, ig := range ignored {
		skip[ig.Key] = true
	}
	applied := []string{}
	for _, k := range m.keys() {
		if skip[k] {
			continue
		}
		if _, ok := over[k]; ok || getenv(k) != "" {
			applied = append(applied, k)
		}
	}
	ignored = dedupe(ignored)
	return Result{Applied: applied, Ignored: ignored}, nil
}

func (m *Manager) keys() []string {
	var out []string
	for _, p := range m.parts {
		out = append(out, p.keys...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (m *Manager) classify(over map[string]string) ([]Ignored, []string) {
	known := m.keys()
	var ignored []Ignored
	var errs []string
	for k := range over {
		switch {
		case slices.Contains(RestartKeys, k):
			ignored = append(ignored, Ignored{Key: k, Reason: ReasonRestart})
		case !slices.Contains(known, k):
			errs = append(errs, "unknown key "+k)
		}
	}
	slices.SortFunc(ignored, func(a, b Ignored) int { return strings.Compare(a.Key, b.Key) })
	slices.Sort(errs)
	return ignored, errs
}

func dedupe(in []Ignored) []Ignored {
	out := []Ignored{}
	seen := map[string]bool{}
	for _, ig := range in {
		if !seen[ig.Key] {
			seen[ig.Key] = true
			out = append(out, ig)
		}
	}
	return out
}

func (m *Manager) overlay(over map[string]string) func(string) string {
	return func(k string) string {
		if v, ok := over[k]; ok {
			return v
		}
		return m.base(k)
	}
}

func (m *Manager) read() (map[string]string, error) {
	if m.file == "" {
		return nil, nil
	}
	f, err := os.Open(m.file) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if len(b) > maxFileBytes {
		return nil, errors.New("config file: larger than 1 MiB")
	}
	return parse(b)
}

func parse(b []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if dec.More() {
		return nil, errors.New("config file: trailing data")
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, err := scalar(v)
		if err != nil {
			return nil, fmt.Errorf("config file: %s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

func scalar(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		return fmt.Sprint(x), nil
	case json.Number:
		return x.String(), nil
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			if _, nested := e.([]any); nested {
				return "", errors.New("nested lists are not supported")
			}
			s, err := scalar(e)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), nil
	}
	return "", errors.New("value must be a string, number, boolean or list of those")
}
