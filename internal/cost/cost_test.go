package cost

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDefaultTableValid(t *testing.T) {
	if _, err := Parse(embeddedPrices); err != nil {
		t.Fatal(err)
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"bad json": `{`,
		"no rate":  `{"models":[{"id":"a"}]}`,
		"empty":    `{"eur_per_usd":1,"models":[]}`,
		"no id":    `{"eur_per_usd":1,"models":[{"input":1}]}`,
		"negative": `{"eur_per_usd":1,"models":[{"id":"a","input":-1}]}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		model string
		id    string
	}{
		{"claude-opus-4-1", "claude-opus"},
		{"claude-sonnet-5-5", "claude-sonnet"},
		{"anthropic/claude-haiku-4-5", "claude-haiku"},
		{"Claude-Sonnet-X", "claude-sonnet"},
		{"gpt-5", "gpt"},
		{"gpt-5-codex", "gpt"},
		{"openai/gpt-5-mini", "gpt-mini"},
		{"gpt-4o-mini", "gpt-mini"},
		{"deepseek-v4-pro", "deepseek-v4-pro"},
		{"deepseek/deepseek-v4-pro-0101", "deepseek-v4-pro"},
		{"deepseek-v4-flash", "deepseek-flash"},
		{"claude-sonnet", "claude-sonnet"},
	}
	for _, tc := range tests {
		p, ok := Default().Lookup(tc.model)
		if !ok || p.ID != tc.id {
			t.Errorf("Lookup(%q) = %q, %v; want %q", tc.model, p.ID, ok, tc.id)
		}
	}
	for _, m := range []string{"", "  ", "llama-3", "gemini-flash"} {
		if _, ok := Default().Lookup(m); ok {
			t.Errorf("Lookup(%q) should be unknown", m)
		}
	}
}

func TestEstimate(t *testing.T) {
	tests := []struct {
		name                    string
		model                   string
		in, out, cacheR, cacheW int64
		want                    float64
		known                   bool
	}{
		{"sonnet 1M in", "claude-sonnet-5-5", 1_000_000, 0, 0, 0, 3.0, true},
		{"sonnet all", "claude-sonnet-5-5", 1_000_000, 1_000_000, 1_000_000, 1_000_000, 3 + 15 + 0.3 + 3.75, true},
		{"opus out", "claude-opus-4", 0, 2_000_000, 0, 0, 150, true},
		{"haiku small", "claude-haiku-4-5", 1000, 500, 0, 0, (1000*1.0 + 500*5.0) / 1e6, true},
		{"gpt cache", "gpt-5", 0, 0, 1_000_000, 0, 0.125, true},
		{"gpt mini", "gpt-5-mini", 1_000_000, 1_000_000, 0, 0, 2.25, true},
		{"deepseek pro", "deepseek-v4-pro", 1_000_000, 1_000_000, 0, 0, 0.55 + 2.2, true},
		{"deepseek flash", "deepseek-v4-flash", 1_000_000, 1_000_000, 1_000_000, 0, 0.14 + 0.28 + 0.028, true},
		{"zero tokens", "claude-opus-4", 0, 0, 0, 0, 0, true},
		{"negative counts ignored", "claude-sonnet-5-5", -5, 1_000_000, 0, 0, 15, true},
		{"unknown", "mystery-1", 1_000_000, 1_000_000, 0, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, known := Estimate(tc.model, tc.in, tc.out, tc.cacheR, tc.cacheW)
			if known != tc.known || !near(got, tc.want) {
				t.Fatalf("got (%v, %v), want (%v, %v)", got, known, tc.want, tc.known)
			}
		})
	}
}

func TestCustomTable(t *testing.T) {
	tb, err := Parse([]byte(`{"eur_per_usd":0.5,"models":[{"id":"x","match":["Foo"],"input":2,"output":4}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := tb.Estimate("my-foo-1", 1_000_000, 1_000_000, 0, 0)
	if !ok || !near(got, 6) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := tb.Estimate("claude-opus", 1, 1, 0, 0); ok {
		t.Fatal("custom table must not know opus")
	}
}

func TestToEUR(t *testing.T) {
	tests := []struct {
		usd, rate, want float64
	}{
		{10, 0.5, 5},
		{10, 1, 10},
		{0, 0.9, 0},
		{10, 0, 10 * Default().EURPerUSD},
		{10, -1, 10 * Default().EURPerUSD},
	}
	for _, tc := range tests {
		if got := ToEUR(tc.usd, tc.rate); !near(got, tc.want) {
			t.Errorf("ToEUR(%v,%v) = %v, want %v", tc.usd, tc.rate, got, tc.want)
		}
	}
}

func TestRemaining(t *testing.T) {
	tests := []struct {
		max, used, want float64
	}{
		{10, 3, 7},
		{10, 10, 0},
		{10, 12.5, 0},
		{10, 0, 10},
		{0, 5, math.Inf(1)},
		{-1, 0, math.Inf(1)},
	}
	for _, tc := range tests {
		if got := Remaining(tc.max, tc.used); got != tc.want {
			t.Errorf("Remaining(%v,%v) = %v, want %v", tc.max, tc.used, got, tc.want)
		}
	}
}
