package retention_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestRetentionOverHTTP(t *testing.T) {
	t.Setenv("SUBSPACE_RETAIN_AUDIT_DAYS", "30")
	t.Setenv("SUBSPACE_RETAIN_MESSAGES_DAYS", "30")
	t.Setenv("SUBSPACE_RETAIN_SEGMENT_ROWS", "4")
	t.Setenv("SUBSPACE_RETAIN_INTERVAL", "0")
	clk := &clock{t: t0}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond,
		SweepEvery: 50 * time.Millisecond, Lease: time.Minute, Now: clk.Now})

	activity := func(prefix string) {
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("%s-%d", prefix, i)
			e.Register(id, "worker", "claude-code")
			if code, out := e.REST("POST", "/api/v1/threads/agent/"+id+"/messages", map[string]any{"body": "hello " + id}); code != 201 {
				t.Fatalf("post message: %d %v", code, out)
			}
		}
	}
	verify := func() map[string]any {
		t.Helper()
		code, out := e.REST("GET", "/api/v1/audit/verify", nil)
		if code != 200 || out["ok"] != true {
			t.Fatalf("audit verify: %d %v", code, out)
		}
		code, arch := e.REST("GET", "/api/v1/retention/verify", nil)
		if code != 200 || arch["ok"] != true {
			t.Fatalf("retention verify: %d %v", code, arch)
		}
		return out
	}

	activity("old")
	old := verify()["events"].(float64)
	clk.Set(t0.Add(40 * day))
	activity("new")
	total := verify()["events"].(float64)

	code, rep := e.REST("POST", "/api/v1/retention/run", map[string]any{})
	if code != 200 {
		t.Fatalf("run: %d %v", code, rep)
	}
	rows := rep["rows"].(map[string]any)
	if rows["audit"].(float64) != old || rows["messages"].(float64) == 0 {
		t.Fatalf("archived rows: %v (old events %v)", rows, old)
	}
	segments := len(rep["archived"].([]any))
	live := verify()["events"].(float64)
	if live != total-old+float64(segments) {
		t.Fatalf("live events %v, want new activity plus %d archive events", live, segments)
	}

	_, st := e.REST("GET", "/api/v1/retention", nil)
	anchor := st["anchor"].(map[string]any)["id"].(float64)
	_, export := e.REST("GET", "/api/v1/audit/export?since=0", nil)
	if got, want := export["chain"].(map[string]any)["prev_hash"], st["anchor"].(map[string]any)["hash"]; got != want {
		t.Fatalf("export prev_hash %v, want anchor %v", got, want)
	}
	_, list := e.REST("GET", "/api/v1/audit?limit=1000", nil)
	for _, ev := range list["events"].([]any) {
		if ev.(map[string]any)["id"].(float64) <= anchor {
			t.Fatalf("archived event %v still listed", ev)
		}
	}
	_, msgs := e.REST("GET", "/api/v1/threads/agent/old-0/messages", nil)
	if n := len(msgs["messages"].([]any)); n != 0 {
		t.Fatalf("old thread still has %d messages", n)
	}

	var audits []string
	for _, s := range st["segments"].([]any) {
		seg := s.(map[string]any)
		id := fmt.Sprint(seg["id"])
		if seg["kind"] == "audit" {
			audits = append(audits, id)
			continue
		}
		if code, out := e.REST("POST", "/api/v1/retention/segments/"+id+"/restore", map[string]any{}); code != 200 {
			t.Fatalf("restore %s: %d %v", id, code, out)
		}
	}
	if code, out := e.REST("POST", "/api/v1/retention/segments/"+audits[0]+"/restore", map[string]any{}); code != 409 {
		t.Fatalf("restoring oldest audit segment first: %d %v", code, out)
	}
	for i := len(audits) - 1; i >= 0; i-- {
		if code, out := e.REST("POST", "/api/v1/retention/segments/"+audits[i]+"/restore", map[string]any{}); code != 200 {
			t.Fatalf("restore %s: %d %v", audits[i], code, out)
		}
	}
	if code, _ := e.REST("POST", "/api/v1/retention/segments/"+audits[0]+"/restore", map[string]any{}); code != 409 {
		t.Fatalf("double restore: %d", code)
	}
	restored := segments + len(st["segments"].([]any))
	if got := verify()["events"].(float64); got != total+float64(restored) {
		t.Fatalf("events after restore %v, want %v", got, total+float64(restored))
	}
	_, msgs = e.REST("GET", "/api/v1/threads/agent/old-0/messages", nil)
	if n := len(msgs["messages"].([]any)); n == 0 {
		t.Fatal("old thread not restored")
	}
}
