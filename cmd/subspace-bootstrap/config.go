package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/repos"
	"github.com/SigmaUno/subspace/internal/spendguard"
)

type Config struct {
	Charter      *CharterSpec        `json:"charter"`
	Profiles     []profiles.Profile  `json:"profiles"`
	Leader       AgentSpec           `json:"leader"`
	Workers      []WorkerSpec        `json:"workers"`
	Reviewer     AgentSpec           `json:"reviewer"`
	Tester       AgentSpec           `json:"tester"`
	Repos        []repos.Input       `json:"repos"`
	Capacity     *capacity.Config    `json:"capacity"`
	Limits       *spendguard.Limits  `json:"limits"`
	QualityGates *model.QualityGates `json:"quality_gates"`
	Autopilot    *AutopilotSpec      `json:"autopilot"`
	Brief        string              `json:"brief"`
	ApprovePlan  bool                `json:"approve_plan"`
	SecretsFile  string              `json:"secrets_file"`

	dir string
}

type CharterSpec struct {
	Template string            `json:"template"`
	Vars     map[string]string `json:"vars"`
}

type AgentSpec struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Profile   string `json:"profile"`
	ExpiresIn string `json:"expires_in"`
}

type WorkerSpec struct {
	Prefix    string `json:"prefix"`
	Count     int    `json:"count"`
	Kind      string `json:"kind"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Profile   string `json:"profile"`
	ExpiresIn string `json:"expires_in"`
}

type AutopilotSpec struct {
	Enabled bool                   `json:"enabled"`
	Spawn   *autopilot.SpawnConfig `json:"spawn"`
}

type agentPlan struct {
	AgentSpec
	Role string
}

var agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func loadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.dir = filepath.Dir(path)
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) path(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.dir, p)
}

func (c *Config) secretsPath() string {
	if c.SecretsFile == "" {
		return c.path("subspace-agents.secrets")
	}
	return c.path(c.SecretsFile)
}

func (c *Config) validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, a := range c.agents() {
		switch {
		case !agentIDRe.MatchString(a.ID):
			errs = append(errs, fmt.Errorf("%s id %q must match %s", a.Role, a.ID, agentIDRe))
		case seen[a.ID]:
			errs = append(errs, fmt.Errorf("agent id %q is used twice", a.ID))
		}
		seen[a.ID] = true
		if a.Kind == "" && a.Profile == "" {
			errs = append(errs, fmt.Errorf("%s %q needs a kind or a profile", a.Role, a.ID))
		}
	}
	if len(c.Workers) == 0 {
		errs = append(errs, errors.New("at least one worker group is required"))
	}
	for _, w := range c.Workers {
		if w.Count < 1 || w.Count > 999 {
			errs = append(errs, fmt.Errorf("worker group %q: count must be 1..999", w.Prefix))
		}
	}
	if c.Brief == "" {
		errs = append(errs, errors.New("brief is required"))
	}
	if c.Charter != nil && c.Charter.Template == "" {
		errs = append(errs, errors.New("charter.template is required"))
	}
	return errors.Join(errs...)
}

func (c *Config) agents() []agentPlan {
	out := []agentPlan{{c.Leader, model.RoleLeader}}
	for _, w := range c.Workers {
		for i := 1; i <= w.Count; i++ {
			out = append(out, agentPlan{AgentSpec{ID: fmt.Sprintf("%s-%03d", w.Prefix, i), Kind: w.Kind, Model: w.Model,
				Provider: w.Provider, Profile: w.Profile, ExpiresIn: w.ExpiresIn}, model.RoleWorker})
		}
	}
	return append(out, agentPlan{c.Tester, model.RoleTester}, agentPlan{c.Reviewer, model.RoleReviewer})
}

func (c *Config) loadBrief() (*briefs.Doc, error) {
	raw, err := os.ReadFile(c.path(c.Brief))
	if err != nil {
		return nil, err
	}
	d, err := briefs.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Brief, err)
	}
	return d, nil
}

func (c *Config) renderCharter() (string, error) {
	raw, err := os.ReadFile(c.path(c.Charter.Template))
	if err != nil {
		return "", err
	}
	t, err := template.New(filepath.Base(c.Charter.Template)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", err
	}
	data := struct {
		Vars                     map[string]string
		Leader, Reviewer, Tester string
		Workers, Repos           []string
	}{Vars: c.Charter.Vars, Leader: c.Leader.ID, Reviewer: c.Reviewer.ID, Tester: c.Tester.ID}
	for _, a := range c.agents() {
		if a.Role == model.RoleWorker {
			data.Workers = append(data.Workers, a.ID)
		}
	}
	for _, r := range c.Repos {
		data.Repos = append(data.Repos, r.Name)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}
