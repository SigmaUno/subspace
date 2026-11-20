package capacity

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/SigmaUno/subspace/internal/model"
)

type Report struct {
	Limit
	InUse     int `json:"in_use"`
	Available int `json:"available"`
}

func (c Config) Validate() error {
	if c.GlobalMaxAgents < 0 {
		return fmt.Errorf("global_max_agents must be >= 0")
	}
	for i, l := range c.Limits {
		if l.Provider == "" || l.Model == "" {
			return fmt.Errorf("limit %d: provider and model are required (use \"*\")", i)
		}
		if l.MaxConcurrency < 0 || l.RequestsPerMin < 0 {
			return fmt.Errorf("limit %d: values must be >= 0", i)
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("capacity file: %w", err)
	}
	return c, c.Validate()
}

func match(pat, v string) bool { return pat == "*" || pat == v }

func (u Usage) used(l Limit) int {
	n := 0
	for k, c := range u {
		if match(l.Provider, k[0]) && match(l.Model, k[1]) {
			n += c
		}
	}
	return n
}

func (u Usage) total() int {
	n := 0
	for _, c := range u {
		n += c
	}
	return n
}

func (c Config) Check(provider, mdl string, n int, u Usage) error {
	if c.GlobalMaxAgents > 0 && u.total()+n > c.GlobalMaxAgents {
		return model.Errf("capacity", "global limit of %d agents reached (%d in use)", c.GlobalMaxAgents, u.total())
	}
	for _, l := range c.Limits {
		if !match(l.Provider, provider) || !match(l.Model, mdl) || l.MaxConcurrency == 0 {
			continue
		}
		if used := u.used(l); used+n > l.MaxConcurrency {
			return model.Errf("capacity", "limit %s/%s: %d in use + %d requested > max %d", l.Provider, l.Model, used, n, l.MaxConcurrency)
		}
	}
	return nil
}

func (c Config) Report(u Usage) []Report {
	out := make([]Report, 0, len(c.Limits))
	for _, l := range c.Limits {
		used := u.used(l)
		avail := l.MaxConcurrency - used
		if l.MaxConcurrency == 0 {
			avail = -1
		} else if avail < 0 {
			avail = 0
		}
		out = append(out, Report{Limit: l, InUse: used, Available: avail})
	}
	return out
}
