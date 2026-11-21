package drill_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestDrillsOverHTTP(t *testing.T) {
	dir := t.TempDir()
	e := servertest.Start(t, server.Config{
		PublicURL: "http://sub.test", DBPath: filepath.Join(dir, "t.db"), BackupDir: filepath.Join(dir, "backups"),
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	if code, out := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "b", "body": "x"}); code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}

	code, out := e.REST("POST", "/api/v1/drills", map[string]any{})
	if code != 201 || out["status"] != "failed" || out["error"] != "restore: no backup available" {
		t.Fatalf("drill without backup: %d %v", code, out)
	}
	if !hasDecision(t, e, "drill:"+out["id"].(string)) {
		t.Fatal("failed drill did not raise an owner decision")
	}

	if code, out := e.REST("POST", "/api/v1/backup", map[string]any{}); code != 200 {
		t.Fatalf("backup: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/drills", nil)
	if code != 201 || out["status"] != "ok" || out["trigger"] != "manual" || out["id"] != "D-2" {
		t.Fatalf("drill: %d %v", code, out)
	}
	for _, c := range out["checks"].([]any) {
		if c.(map[string]any)["status"] != "ok" {
			t.Fatalf("check: %v", c)
		}
	}
	if hasDecision(t, e, "drill:D-1") {
		t.Fatal("decision stayed after a passing drill")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "drill-*"))
	if len(left) > 0 {
		t.Fatalf("temporary copies left: %v", left)
	}

	code, out = e.REST("GET", "/api/v1/drills", nil)
	list, _ := out["drills"].([]any)
	if code != 200 || len(list) != 2 || list[0].(map[string]any)["id"] != "D-2" || list[1].(map[string]any)["id"] != "D-1" {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := e.REST("GET", "/api/v1/drills?limit=1", nil); code != 200 || len(out["drills"].([]any)) != 1 {
		t.Fatalf("limit: %d %v", code, out)
	}
	if code, _ := e.REST("GET", "/api/v1/drills?limit=0", nil); code != 400 {
		t.Fatalf("limit=0: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/drills", map[string]any{"bogus": 1}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}

	_, audit := e.REST("GET", "/api/v1/audit?tool=restore_drill", nil)
	if n := len(audit["events"].([]any)); n != 2 {
		t.Fatalf("audit events = %d", n)
	}
	code, v := e.REST("GET", "/api/v1/audit/verify", nil)
	if code != 200 || v["ok"] != true {
		t.Fatalf("audit chain: %d %v", code, v)
	}
	_, feed := e.REST("GET", "/api/v1/activity", nil)
	if !feedMentions(feed, "restore drill D-2") {
		t.Fatalf("activity feed lacks the drill: %v", feed)
	}
}

func hasDecision(t *testing.T, e *servertest.Env, id string) bool {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/decisions", nil)
	if code != 200 {
		t.Fatalf("decisions: %d %v", code, out)
	}
	for _, d := range out["decisions"].([]any) {
		if d.(map[string]any)["id"] == id {
			return true
		}
	}
	return false
}

func feedMentions(feed map[string]any, text string) bool {
	items, _ := feed["items"].([]any)
	for _, it := range items {
		if s, _ := it.(map[string]any)["text"].(string); strings.Contains(s, text) {
			return true
		}
	}
	return false
}
