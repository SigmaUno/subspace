package capacity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name    string
		path    string
		wantErr string
		want    Config
	}{
		{
			name: "valid",
			path: write("ok.json", `{"global_max_agents":300,"limits":[{"provider":"anthropic","model":"*","max_concurrency":20,"requests_per_min":600}]}`),
			want: Config{GlobalMaxAgents: 300, Limits: []Limit{{Provider: "anthropic", Model: "*", MaxConcurrency: 20, RequestsPerMin: 600}}},
		},
		{name: "empty object", path: write("empty.json", `{}`), want: Config{}},
		{name: "missing file", path: filepath.Join(dir, "nope.json"), wantErr: "no such file"},
		{name: "bad json", path: write("bad.json", `{not json`), wantErr: "capacity file"},
		{name: "negative global", path: write("neg.json", `{"global_max_agents":-1}`), wantErr: "global_max_agents"},
		{name: "missing provider", path: write("np.json", `{"limits":[{"model":"*","max_concurrency":1}]}`), wantErr: "provider and model"},
		{name: "missing model", path: write("nm.json", `{"limits":[{"provider":"x","max_concurrency":1}]}`), wantErr: "provider and model"},
		{name: "negative concurrency", path: write("nc.json", `{"limits":[{"provider":"x","model":"*","max_concurrency":-1}]}`), wantErr: ">= 0"},
		{name: "negative rpm", path: write("nr.json", `{"limits":[{"provider":"x","model":"*","requests_per_min":-5}]}`), wantErr: ">= 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(tc.path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.GlobalMaxAgents != tc.want.GlobalMaxAgents || len(got.Limits) != len(tc.want.Limits) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got.Limits {
				if got.Limits[i] != tc.want.Limits[i] {
					t.Fatalf("limit %d: got %+v, want %+v", i, got.Limits[i], tc.want.Limits[i])
				}
			}
		})
	}
}

func TestCheck(t *testing.T) {
	cfg := Config{
		GlobalMaxAgents: 10,
		Limits: []Limit{
			{Provider: "anthropic", Model: "sonnet", MaxConcurrency: 3},
			{Provider: "anthropic", Model: "*", MaxConcurrency: 5},
			{Provider: "*", Model: "*", MaxConcurrency: 8},
			{Provider: "openai", Model: "gpt", MaxConcurrency: 0},
		},
	}
	tests := []struct {
		name     string
		provider string
		model    string
		n        int
		usage    Usage
		wantDeny string
	}{
		{name: "empty usage", provider: "anthropic", model: "sonnet", n: 1, usage: Usage{}},
		{name: "nil usage", provider: "anthropic", model: "sonnet", n: 1},
		{name: "exactly at model limit", provider: "anthropic", model: "sonnet", n: 3, usage: Usage{}},
		{name: "model limit exceeded", provider: "anthropic", model: "sonnet", n: 1,
			usage: Usage{{"anthropic", "sonnet"}: 3}, wantDeny: "limit anthropic/sonnet"},
		{name: "provider wildcard limit exceeded", provider: "anthropic", model: "opus", n: 2,
			usage: Usage{{"anthropic", "sonnet"}: 2, {"anthropic", "haiku"}: 2}, wantDeny: "limit anthropic/*"},
		{name: "provider wildcard ok", provider: "anthropic", model: "opus", n: 1,
			usage: Usage{{"anthropic", "sonnet"}: 2, {"anthropic", "haiku"}: 2}},
		{name: "catch-all limit exceeded", provider: "deepseek", model: "chat", n: 1,
			usage: Usage{{"openai", "gpt"}: 8}, wantDeny: "limit */*"},
		{name: "unlimited entry skipped but catch-all applies", provider: "openai", model: "gpt", n: 1,
			usage: Usage{{"openai", "gpt"}: 8}, wantDeny: "limit */*"},
		{name: "unlimited entry allows", provider: "openai", model: "gpt", n: 5, usage: Usage{{"openai", "gpt"}: 2}},
		{name: "global exceeded", provider: "openai", model: "gpt", n: 1,
			usage: Usage{{"openai", "gpt"}: 5, {"deepseek", "chat"}: 5}, wantDeny: "global limit"},
		{name: "global exceeded by n", provider: "openai", model: "gpt", n: 6,
			usage: Usage{{"deepseek", "chat"}: 5}, wantDeny: "global limit"},
		{name: "other provider unaffected by anthropic usage", provider: "deepseek", model: "chat", n: 1,
			usage: Usage{{"anthropic", "sonnet"}: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cfg.Check(tc.provider, tc.model, tc.n, tc.usage)
			if tc.wantDeny == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var me *model.Error
			if !errors.As(err, &me) {
				t.Fatalf("err = %v, want *model.Error", err)
			}
			if me.Code != "capacity" {
				t.Fatalf("code = %q, want capacity", me.Code)
			}
			if !strings.Contains(me.Message, tc.wantDeny) {
				t.Fatalf("message %q does not contain %q", me.Message, tc.wantDeny)
			}
		})
	}
}

func TestCheckNoLimits(t *testing.T) {
	var cfg Config
	if err := cfg.Check("anthropic", "x", 1000, Usage{{"anthropic", "x"}: 1000}); err != nil {
		t.Fatalf("empty config must allow everything: %v", err)
	}
}

func TestReport(t *testing.T) {
	cfg := Config{Limits: []Limit{
		{Provider: "anthropic", Model: "*", MaxConcurrency: 20, RequestsPerMin: 600},
		{Provider: "openai", Model: "gpt", MaxConcurrency: 2},
		{Provider: "*", Model: "*", MaxConcurrency: 0},
	}}
	u := Usage{
		{"anthropic", "sonnet"}: 3,
		{"anthropic", "opus"}:   1,
		{"openai", "gpt"}:       5,
	}
	got := cfg.Report(u)
	want := []struct{ inUse, avail int }{{4, 16}, {5, 0}, {9, -1}}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].InUse != w.inUse || got[i].Available != w.avail {
			t.Errorf("report %d = in_use %d available %d, want %d/%d", i, got[i].InUse, got[i].Available, w.inUse, w.avail)
		}
	}
	if got[0].Limit != cfg.Limits[0] {
		t.Errorf("limit not carried over: %+v", got[0].Limit)
	}
}

func TestReportEmpty(t *testing.T) {
	got := Config{}.Report(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Fatalf("json = %s, want []", b)
	}
}

func TestReportJSON(t *testing.T) {
	cfg := Config{Limits: []Limit{{Provider: "anthropic", Model: "*", MaxConcurrency: 20, RequestsPerMin: 600}}}
	b, err := json.Marshal(cfg.Report(Usage{{"anthropic", "sonnet"}: 4})[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"provider": "anthropic", "model": "*", "max_concurrency": 20.0,
		"requests_per_min": 600.0, "in_use": 4.0, "available": 16.0,
	}
	if len(m) != len(want) {
		t.Fatalf("keys = %v, want %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
}
