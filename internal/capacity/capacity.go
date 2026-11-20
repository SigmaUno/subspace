// Package capacity models global inference capacity limits.
package capacity

// Limit caps concurrency for a provider/model ("*" wildcards allowed).
type Limit struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	MaxConcurrency int    `json:"max_concurrency"`
	RequestsPerMin int    `json:"requests_per_min"`
}

// Config is the capacity configuration.
type Config struct {
	GlobalMaxAgents int     `json:"global_max_agents"`
	Limits          []Limit `json:"limits"`
}

// Usage counts agents in use per [provider, model].
type Usage map[[2]string]int

// TODO(split B3): Load(path), Config.Check(provider, model, n, usage) error, Config.Report(usage) []Report in capacity_impl.go.
