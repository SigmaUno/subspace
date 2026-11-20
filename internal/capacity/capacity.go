package capacity

type Limit struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	MaxConcurrency int    `json:"max_concurrency"`
	RequestsPerMin int    `json:"requests_per_min"`
}

type Config struct {
	GlobalMaxAgents int     `json:"global_max_agents"`
	Limits          []Limit `json:"limits"`
}

type Usage map[[2]string]int
