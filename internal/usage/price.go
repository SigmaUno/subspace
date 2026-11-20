package usage

import "strings"

type Price struct {
	Match      string  `json:"match"`
	In         float64 `json:"in"`
	Out        float64 `json:"out"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

var DefaultPrices = []Price{
	{Match: "claude-opus-4-1", In: 15, Out: 75, CacheRead: 1.5, CacheWrite: 18.75},
	{Match: "claude-opus-4-0", In: 15, Out: 75, CacheRead: 1.5, CacheWrite: 18.75},
	{Match: "opus", In: 5, Out: 25, CacheRead: 0.5, CacheWrite: 6.25},
	{Match: "sonnet", In: 3, Out: 15, CacheRead: 0.3, CacheWrite: 3.75},
	{Match: "haiku", In: 1, Out: 5, CacheRead: 0.1, CacheWrite: 1.25},
	{Match: "gpt-5-nano", In: 0.05, Out: 0.4, CacheRead: 0.005},
	{Match: "gpt-5-mini", In: 0.25, Out: 2, CacheRead: 0.025},
	{Match: "gpt-5", In: 1.25, Out: 10, CacheRead: 0.125},
	{Match: "deepseek-reasoner", In: 0.56, Out: 1.68, CacheRead: 0.07},
	{Match: "deepseek", In: 0.56, Out: 1.68, CacheRead: 0.07},
}

func priceFor(prices []Price, model string) (Price, bool) {
	m := strings.ToLower(model)
	for _, p := range prices {
		if strings.Contains(m, p.Match) {
			return p, true
		}
	}
	return Price{}, false
}

func (p Price) cost(r Record) float64 {
	return (float64(r.TokensIn)*p.In + float64(r.TokensOut)*p.Out +
		float64(r.CacheRead)*p.CacheRead + float64(r.CacheWrite)*p.CacheWrite) / 1e6
}

func providerFor(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"), strings.Contains(m, "codex"):
		return "openai"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	}
	return ""
}
