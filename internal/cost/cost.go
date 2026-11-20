// Package cost estimates model usage cost from token counts.
//
// The prices in prices.json are APPROXIMATE list prices and will drift; they
// are meant for budget guard rails, not billing. Edit prices.json to update.
package cost

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
)

//go:embed prices.json
var embeddedPrices []byte

const perMillion = 1_000_000.0

// Price is USD per 1M tokens.
type Price struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"`
	Match      []string `json:"match"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  float64  `json:"cache_read"`
	CacheWrite float64  `json:"cache_write"`
}

// Table is a pricing table.
type Table struct {
	Comment   string  `json:"comment"`
	EURPerUSD float64 `json:"eur_per_usd"`
	Models    []Price `json:"models"`
}

// Parse decodes and validates a pricing table.
func Parse(data []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("cost: parse prices: %w", err)
	}
	if t.EURPerUSD <= 0 {
		return nil, fmt.Errorf("cost: eur_per_usd must be > 0")
	}
	if len(t.Models) == 0 {
		return nil, fmt.Errorf("cost: no models")
	}
	for i := range t.Models {
		p := &t.Models[i]
		if p.ID == "" {
			return nil, fmt.Errorf("cost: model %d has no id", i)
		}
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return nil, fmt.Errorf("cost: model %s has a negative price", p.ID)
		}
		for j, m := range p.Match {
			p.Match[j] = strings.ToLower(m)
		}
	}
	return &t, nil
}

var (
	defaultOnce  sync.Once
	defaultTable *Table
)

// Default returns the embedded table. It panics if prices.json is invalid
// (covered by tests).
func Default() *Table {
	defaultOnce.Do(func() {
		t, err := Parse(embeddedPrices)
		if err != nil {
			panic(err)
		}
		defaultTable = t
	})
	return defaultTable
}

// Lookup finds the price for a model: exact id match first, then the entry
// whose longest "match" substring occurs in the lowercased model name.
// A "provider/" prefix on the model name is allowed.
func (t *Table) Lookup(model string) (Price, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return Price{}, false
	}
	bare := name
	if i := strings.LastIndex(bare, "/"); i >= 0 {
		bare = bare[i+1:]
	}
	for _, p := range t.Models {
		if strings.EqualFold(p.ID, bare) {
			return p, true
		}
	}
	best, bestLen := -1, 0
	for i, p := range t.Models {
		for _, m := range p.Match {
			if m != "" && len(m) > bestLen && strings.Contains(name, m) {
				best, bestLen = i, len(m)
			}
		}
	}
	if best < 0 {
		return Price{}, false
	}
	return t.Models[best], true
}

// Estimate returns the USD cost of the given token counts. known is false
// (and usd 0) when the model matches no entry. Negative counts count as 0.
func (t *Table) Estimate(model string, in, out, cacheRead, cacheWrite int64) (usd float64, known bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	usd = (float64(nonNeg(in))*p.Input +
		float64(nonNeg(out))*p.Output +
		float64(nonNeg(cacheRead))*p.CacheRead +
		float64(nonNeg(cacheWrite))*p.CacheWrite) / perMillion
	return usd, true
}

// Estimate uses the embedded table.
func Estimate(model string, in, out, cacheRead, cacheWrite int64) (usd float64, known bool) {
	return Default().Estimate(model, in, out, cacheRead, cacheWrite)
}

// ToEUR converts USD to EUR. A rate <= 0 selects the table default.
func ToEUR(usd, eurPerUSD float64) float64 {
	if eurPerUSD <= 0 {
		eurPerUSD = Default().EURPerUSD
	}
	return usd * eurPerUSD
}

// Remaining returns the budget left, never below 0. maxEUR <= 0 means no
// budget is set and yields +Inf.
func Remaining(maxEUR, usedEUR float64) float64 {
	if maxEUR <= 0 {
		return math.Inf(1)
	}
	if left := maxEUR - usedEUR; left > 0 {
		return left
	}
	return 0
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
