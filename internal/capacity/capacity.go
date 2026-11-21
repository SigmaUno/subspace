package capacity

type Limit struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	MaxConcurrency int    `json:"max_concurrency"`
	RequestsPerMin int    `json:"requests_per_min"`
}

type Gate func(provider, model string, n int) error

type Config struct {
	GlobalMaxAgents int     `json:"global_max_agents"`
	Limits          []Limit `json:"limits"`
	Gate            Gate    `json:"-"`
}

type Usage map[[2]string]int
