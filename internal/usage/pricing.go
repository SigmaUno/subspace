package usage

import (
	"math"
	"strings"
)

const eurPerUSD = 0.92

type price struct{ in, out float64 }

var modelPrices = map[string]price{
	"claude-opus-5":     {15, 75},
	"claude-sonnet-5":   {3, 15},
	"claude-haiku-5":    {1, 5},
	"deepseek-chat":     {0.27, 1.10},
	"deepseek-reasoner": {0.55, 2.19},
	"gpt-5":             {1.25, 10},
	"gpt-5-mini":        {0.25, 2},
	"gpt-5-nano":        {0.05, 0.40},
	"gpt-4.1":           {2, 8},
	"gpt-4.1-mini":      {0.40, 1.60},
}

var providerPrices = map[string]price{
	"anthropic": modelPrices["claude-sonnet-5"],
	"openai":    modelPrices["gpt-5"],
	"deepseek":  modelPrices["deepseek-chat"],
}

func lookup(provider, model string) (price, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	var best price
	bestLen := 0
	for prefix, p := range modelPrices {
		if len(prefix) > bestLen && strings.HasPrefix(model, prefix) {
			best, bestLen = p, len(prefix)
		}
	}
	if bestLen > 0 {
		return best, true
	}
	p, ok := providerPrices[strings.ToLower(provider)]
	return p, ok
}

func Estimate(provider, model string, inputTokens, outputTokens int64) float64 {
	p, ok := lookup(provider, model)
	if !ok {
		return 0
	}
	usd := (float64(inputTokens)*p.in + float64(outputTokens)*p.out) / 1e6
	return math.Round(usd*eurPerUSD*1e6) / 1e6
}
