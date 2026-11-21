package breaker_test

import (
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func setup(t *testing.T, cooldown string) (e *servertest.Env, lead, worker, brief string) {
	t.Helper()
	t.Setenv("SUBSPACE_BREAKER_ERROR_RATE", "0.5")
	t.Setenv("SUBSPACE_BREAKER_MIN_SAMPLES", "4")
	t.Setenv("SUBSPACE_BREAKER_COOLDOWN", cooldown)
	e = servertest.New(t)
	lead = e.Register("rex", "leader", "other")
	worker = e.Register("w1", "worker", "claude-code")
	code, out := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}
	return e, lead, worker, out["id"].(string)
}

func spawn(e *servertest.Env, lead, brief string) (map[string]any, bool) {
	return e.Call(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 1})
}

func TestSpawnRefusedWhileOpenAndAllowedAfterReset(t *testing.T) {
	e, lead, worker, brief := setup(t, "1h")

	if out, isErr := spawn(e, lead, brief); isErr {
		t.Fatalf("spawn while closed: %v", out)
	}
	e.Must(worker, "report_usage", map[string]any{"model": "m", "provider": "anthropic", "tokens_in": 10})
	e.Must(worker, "report_provider_error", map[string]any{"status": 503})
	e.Must(worker, "report_provider_error", map[string]any{"provider": "anthropic", "model": "m", "status": 529})
	out := e.Must(worker, "report_usage", map[string]any{"model": "m", "provider": "anthropic", "rate_limited": true})
	if out["ok"] != true {
		t.Fatalf("report_usage: %v", out)
	}
	if got := e.Must(worker, "report_provider_error", map[string]any{})["state"]; got != "open" {
		t.Fatalf("state after 4 samples with 3 errors: %v", got)
	}

	code, body := e.REST("GET", "/api/v1/breakers", nil)
	list, _ := body["breakers"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("breakers: %d %v", code, body)
	}
	b := list[0].(map[string]any)
	if b["errors"] != 3.0 || b["samples"] != 4.0 {
		t.Fatalf("stats at open: %v", b)
	}
	if b["provider"] != "anthropic" || b["model"] != "m" || b["state"] != "open" || b["opened_at"] == nil || b["half_open_at"] == nil {
		t.Fatalf("breaker: %v", b)
	}

	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 1}, "capacity")

	_, dec := e.REST("GET", "/api/v1/decisions", nil)
	found := false
	for _, d := range dec["decisions"].([]any) {
		if d.(map[string]any)["id"] == "breaker:anthropic:m" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no breaker decision: %v", dec)
	}

	if code, _ := e.REST("POST", "/api/v1/breakers/openai/reset", nil); code != 404 {
		t.Fatalf("reset unknown provider: %d", code)
	}
	code, body = e.REST("POST", "/api/v1/breakers/anthropic/reset", nil)
	if code != 200 || body["breakers"].([]any)[0].(map[string]any)["state"] != "closed" {
		t.Fatalf("reset: %d %v", code, body)
	}
	if out, isErr := spawn(e, lead, brief); isErr {
		t.Fatalf("spawn after reset: %v", out)
	}
	_, dec = e.REST("GET", "/api/v1/decisions", nil)
	for _, d := range dec["decisions"].([]any) {
		if d.(map[string]any)["kind"] == "breaker" {
			t.Fatalf("decision should clear after reset: %v", d)
		}
	}
}

func TestHalfOpenAllowsOneProbeSpawn(t *testing.T) {
	e, lead, worker, brief := setup(t, "300ms")
	for range 4 {
		e.Must(worker, "report_provider_error", map[string]any{})
	}
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 1}, "capacity")
	time.Sleep(400 * time.Millisecond)
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 2}, "capacity")
	if out, isErr := spawn(e, lead, brief); isErr {
		t.Fatalf("probe spawn: %v", out)
	}
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 1}, "capacity")
}
